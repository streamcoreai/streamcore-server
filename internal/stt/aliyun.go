package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// Alibaba Cloud Model Studio (DashScope) streaming ASR
// ----------------------------------------------------
//
//	Endpoint:  wss://dashscope.aliyuncs.com/api-ws/v1/inference
//	Auth:      Authorization: Bearer <api key>
//	Audio:     raw 16-bit signed little-endian PCM at 16kHz mono, sent as
//	           binary frames roughly 100ms at a time.
//
// Control messages are JSON text frames and audio is binary, the same split
// Deepgram uses. A session is three steps: send `run-task`, wait for
// `task-started` before any audio, then stream. `finish-task` closes it.
//
// The result shape lines up with this package's contract without translation:
// each `result-generated` carries one sentence, `sentence.text` is that
// sentence alone rather than the conversation so far, and `sentence_end`
// says whether it has settled.
type aliyunClient struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
	taskID string

	// writeMu serialises writes: gorilla/websocket allows a single concurrent
	// writer, and Close races SendAudio when a call ends mid-utterance.
	writeMu sync.Mutex
	closed  bool

	// started closes once task-started arrives. Audio sent before that is
	// discarded by the service, taking the opening words of the call with it.
	started chan struct{}
}

const (
	aliyunURL        = "wss://dashscope.aliyuncs.com/api-ws/v1/inference"
	aliyunSampleRate = 16000
	// paraformer-realtime-v2 is the general-purpose realtime model. Mandarin
	// plus Cantonese and other dialects; fun-asr-realtime is the alternative.
	defaultAliyunModel = "paraformer-realtime-v2"
)

// NewAliyunClient dials Model Studio and completes the run-task handshake
// before returning, so the caller can send audio immediately.
func NewAliyunClient(ctx context.Context, cfg config.AliyunConfig, onResult func(TranscriptResult)) (Client, error) {
	sttCtx, cancel := context.WithCancel(ctx)

	endpoint := cfg.URL
	if endpoint == "" {
		endpoint = aliyunURL
	}
	model := cfg.Model
	if model == "" {
		model = defaultAliyunModel
	}

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+cfg.APIKey)

	conn, resp, err := websocket.DefaultDialer.DialContext(sttCtx, endpoint, hdr)
	if err != nil {
		cancel()
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("aliyun dial: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("aliyun dial: %w", err)
	}

	c := &aliyunClient{
		conn:    conn,
		cancel:  cancel,
		taskID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		started: make(chan struct{}),
	}

	if err := c.sendRunTask(cfg, model); err != nil {
		c.Close()
		return nil, err
	}

	go c.readLoop(sttCtx, onResult)

	// The service drops audio that arrives before it has acknowledged the
	// task, so the handshake is completed here rather than left to race the
	// first frames.
	select {
	case <-c.started:
	case <-sttCtx.Done():
		c.Close()
		return nil, sttCtx.Err()
	}

	log.Printf("[stt] connected to Aliyun (%s)", model)
	return c, nil
}

// aliyunMessage covers every frame in both directions; the discriminator is
// header.action going out and header.event coming back.
type aliyunMessage struct {
	Header  aliyunHeader `json:"header"`
	Payload any          `json:"payload,omitempty"`
}

type aliyunHeader struct {
	Action    string `json:"action,omitempty"`
	Event     string `json:"event,omitempty"`
	TaskID    string `json:"task_id"`
	Streaming string `json:"streaming,omitempty"`

	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type aliyunRunPayload struct {
	TaskGroup  string           `json:"task_group"`
	Task       string           `json:"task"`
	Function   string           `json:"function"`
	Model      string           `json:"model"`
	Parameters aliyunParameters `json:"parameters"`
	Input      struct{}         `json:"input"`
}

type aliyunParameters struct {
	SampleRate int    `json:"sample_rate"`
	Format     string `json:"format"`
	// LanguageHints biases a multilingual model toward a language ("zh",
	// "en"). Empty leaves the model to detect it.
	LanguageHints []string `json:"language_hints,omitempty"`
	// VocabularyID points at a hotword list created in the console, which is
	// how domain terms that keep coming back wrong get fixed.
	VocabularyID string `json:"vocabulary_id,omitempty"`
}

func (c *aliyunClient) sendRunTask(cfg config.AliyunConfig, model string) error {
	var hints []string
	if cfg.Language != "" {
		hints = []string{cfg.Language}
	}
	return c.writeJSON(aliyunMessage{
		Header: aliyunHeader{Action: "run-task", TaskID: c.taskID, Streaming: "duplex"},
		Payload: aliyunRunPayload{
			TaskGroup: "audio", Task: "asr", Function: "recognition",
			Model: model,
			Parameters: aliyunParameters{
				SampleRate:    aliyunSampleRate,
				Format:        "pcm",
				LanguageHints: hints,
				VocabularyID:  cfg.VocabularyID,
			},
		},
	})
}

func (c *aliyunClient) writeJSON(m aliyunMessage) error {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("aliyun marshal: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return fmt.Errorf("aliyun: connection closed")
	}
	return c.conn.WriteMessage(websocket.TextMessage, body)
}

// SendAudio forwards a chunk of PCM. The pipeline calls this every 20ms.
func (c *aliyunClient) SendAudio(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return fmt.Errorf("aliyun: connection closed")
	}
	return c.conn.WriteMessage(websocket.BinaryMessage, data)
}

// aliyunResult is a result-generated frame.
type aliyunResult struct {
	Header  aliyunHeader `json:"header"`
	Payload struct {
		Output struct {
			Sentence struct {
				// Text is this sentence only, not the conversation so far.
				Text string `json:"text"`
				// SentenceEnd marks the sentence settled; until then Text is
				// still being revised.
				SentenceEnd bool `json:"sentence_end"`
			} `json:"sentence"`
		} `json:"output"`
	} `json:"payload"`
}

func (c *aliyunClient) readLoop(ctx context.Context, onResult func(TranscriptResult)) {
	startedOnce := sync.Once{}

	for {
		typ, data, err := c.conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[stt] aliyun read: %v", err)
			}
			return
		}
		if typ != websocket.TextMessage {
			continue
		}

		var res aliyunResult
		if err := json.Unmarshal(data, &res); err != nil {
			continue
		}

		switch res.Header.Event {
		case "task-started":
			startedOnce.Do(func() { close(c.started) })

		case "result-generated":
			text := strings.TrimSpace(res.Payload.Output.Sentence.Text)
			if text == "" {
				continue
			}
			final := res.Payload.Output.Sentence.SentenceEnd
			if final {
				log.Printf("[stt] final: %q", text)
			}
			onResult(TranscriptResult{Text: text, IsFinal: final})

		case "task-failed":
			log.Printf("[stt] aliyun %s: %s", res.Header.ErrorCode, res.Header.ErrorMessage)
			return

		case "task-finished":
			return
		}
	}
}

// Close asks the service to finish the task, then tears down the connection.
func (c *aliyunClient) Close() {
	c.writeMu.Lock()
	already := c.closed
	c.writeMu.Unlock()
	if already {
		return
	}

	// finish-task flushes the sentence still in progress. Closing the socket
	// without it drops whatever the caller said last.
	_ = c.writeJSON(aliyunMessage{
		Header: aliyunHeader{Action: "finish-task", TaskID: c.taskID, Streaming: "duplex"},
	})

	c.writeMu.Lock()
	c.closed = true
	c.writeMu.Unlock()

	_ = c.conn.Close()
	c.cancel()
}
