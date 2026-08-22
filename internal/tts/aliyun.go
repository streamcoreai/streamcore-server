package tts

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
)

// Alibaba Cloud Model Studio (DashScope) streaming TTS
// ----------------------------------------------------
//
//	Endpoint:  wss://dashscope.aliyuncs.com/api-ws/v1/inference
//	Auth:      Authorization: Bearer <api key>
//	Audio:     raw 16-bit signed little-endian PCM at 16kHz mono, arriving as
//	           binary frames.
//
// This is the same endpoint, key and event vocabulary as the ASR client in
// internal/stt, run in the other direction: `run-task` opens the session,
// `continue-task` feeds text, `finish-task` closes it, and audio comes back on
// the binary channel while control messages stay JSON.
//
// The service emits 16kHz PCM directly, so unlike MiMo there is no resampling
// stage here — what arrives on the wire is what the pipeline plays.
const (
	aliyunTTSURL = "wss://dashscope.aliyuncs.com/api-ws/v1/inference"
	// aliyunSampleRate is what the pipeline plays; the service honours it, so
	// no conversion is needed on this path.
	aliyunSampleRate = 16000
	// cosyvoice-v2 is the current general-purpose synthesis model.
	defaultAliyunTTSModel = "cosyvoice-v2"
	// Voices are versioned alongside the model: longxiaochun_v2 belongs to
	// cosyvoice-v2, and cosyvoice-v1 wants plain longxiaochun.
	defaultAliyunVoice = "longxiaochun_v2"
)

type aliyunClient struct {
	apiKey string
	voice  string
	model  string
	url    string
}

// NewAliyunClient creates a Model Studio TTS client. Empty voice, model and
// url fall back to the package defaults.
func NewAliyunClient(apiKey, voice, model, url string) Client {
	if voice == "" {
		voice = defaultAliyunVoice
	}
	if model == "" {
		model = defaultAliyunTTSModel
	}
	if url == "" {
		url = aliyunTTSURL
	}
	return &aliyunClient{apiKey: apiKey, voice: voice, model: model, url: url}
}

// aliyunMessage covers every frame sent to the service.
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
	TaskGroup  string              `json:"task_group"`
	Task       string              `json:"task"`
	Function   string              `json:"function"`
	Model      string              `json:"model"`
	Parameters aliyunTTSParameters `json:"parameters"`
	Input      struct{}            `json:"input"`
}

type aliyunTTSParameters struct {
	TextType   string `json:"text_type"`
	Voice      string `json:"voice"`
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
}

// aliyunTextPayload carries text on continue-task, and is empty on finish-task.
type aliyunTextPayload struct {
	Input struct {
		Text string `json:"text,omitempty"`
	} `json:"input"`
}

// aliyunSession is one synthesis: a connection, its task id, and the write
// lock that keeps the teardown path from racing the send path.
type aliyunSession struct {
	conn    *websocket.Conn
	taskID  string
	writeMu sync.Mutex
	closed  bool
}

func (s *aliyunSession) writeJSON(m aliyunMessage) error {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("aliyun marshal: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return fmt.Errorf("aliyun: connection closed")
	}
	return s.conn.WriteMessage(websocket.TextMessage, body)
}

func (s *aliyunSession) close() {
	s.writeMu.Lock()
	if s.closed {
		s.writeMu.Unlock()
		return
	}
	s.closed = true
	s.writeMu.Unlock()
	_ = s.conn.Close()
}

// open dials and completes the run-task handshake.
//
// It returns only once `task-started` has arrived, because text sent before
// that is discarded — the same rule the ASR path lives by, and the failure is
// just as quiet: a session that synthesizes nothing and reports no error.
//
// One connection per utterance. The pipeline synthesizes sentence by sentence
// while earlier audio is still playing, so the dial for sentence two overlaps
// sentence one's playback and only the first sentence of a turn pays for it.
func (c *aliyunClient) open(ctx context.Context) (*aliyunSession, error) {
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+c.apiKey)

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, c.url, hdr)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("aliyun dial: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("aliyun dial: %w", err)
	}

	s := &aliyunSession{conn: conn, taskID: strings.ReplaceAll(uuid.New().String(), "-", "")}

	if err := s.writeJSON(aliyunMessage{
		Header: aliyunHeader{Action: "run-task", TaskID: s.taskID, Streaming: "duplex"},
		Payload: aliyunRunPayload{
			TaskGroup: "audio", Task: "tts", Function: "SpeechSynthesizer",
			Model: c.model,
			Parameters: aliyunTTSParameters{
				TextType:   "PlainText",
				Voice:      c.voice,
				Format:     "pcm",
				SampleRate: aliyunSampleRate,
			},
		},
	}); err != nil {
		s.close()
		return nil, err
	}

	for {
		typ, data, err := conn.ReadMessage()
		if err != nil {
			s.close()
			return nil, fmt.Errorf("aliyun handshake: %w", err)
		}
		if typ != websocket.TextMessage {
			continue
		}
		var res aliyunMessage
		if err := json.Unmarshal(data, &res); err != nil {
			continue
		}
		switch res.Header.Event {
		case "task-started":
			return s, nil
		case "task-failed":
			s.close()
			return nil, fmt.Errorf("aliyun %s: %s", res.Header.ErrorCode, res.Header.ErrorMessage)
		}
	}
}

func (c *aliyunClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	s, err := c.open(ctx)
	if err != nil {
		return nil, err
	}

	// Text and the end-of-input marker go together: this client is handed a
	// whole sentence, so there is nothing to add after it.
	var textPayload aliyunTextPayload
	textPayload.Input.Text = text
	if err := s.writeJSON(aliyunMessage{
		Header:  aliyunHeader{Action: "continue-task", TaskID: s.taskID, Streaming: "duplex"},
		Payload: textPayload,
	}); err != nil {
		s.close()
		return nil, err
	}
	if err := s.writeJSON(aliyunMessage{
		Header:  aliyunHeader{Action: "finish-task", TaskID: s.taskID, Streaming: "duplex"},
		Payload: aliyunTextPayload{},
	}); err != nil {
		s.close()
		return nil, err
	}

	ch := make(chan StreamChunk, 8)
	go func() {
		defer close(ch)
		defer s.close()
		c.pump(ctx, s, ch)
	}()
	return ch, nil
}

// pump forwards binary frames as PCM until the task finishes.
//
// A barge-in cancels ctx mid-utterance, and ReadMessage does not watch a
// context — so the connection is closed from a second goroutine to break the
// read, rather than leaving it blocked until the service decides to hang up.
func (c *aliyunClient) pump(ctx context.Context, s *aliyunSession, ch chan<- StreamChunk) {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			s.close()
		case <-done:
		}
	}()

	emit := func(chunk StreamChunk) bool {
		select {
		case ch <- chunk:
			return true
		case <-ctx.Done():
			return false
		}
	}

	total := 0
	for {
		typ, data, err := s.conn.ReadMessage()
		if err != nil {
			// A cancelled turn is the expected way this ends, not a fault.
			if ctx.Err() == nil {
				emit(StreamChunk{Err: fmt.Errorf("aliyun stream read: %w", err)})
			}
			return
		}

		if typ == websocket.BinaryMessage {
			total += len(data)
			if !emit(StreamChunk{PCM: data}) {
				return
			}
			continue
		}

		var res aliyunMessage
		if err := json.Unmarshal(data, &res); err != nil {
			continue
		}
		switch res.Header.Event {
		case "task-failed":
			emit(StreamChunk{Err: fmt.Errorf("aliyun %s: %s", res.Header.ErrorCode, res.Header.ErrorMessage)})
			return
		case "task-finished":
			log.Printf("[tts:aliyun] synthesized %d bytes (%.2fs of audio)",
				total, float64(total)/2/float64(aliyunSampleRate))
			return
		}
	}
}

// Synthesize drains the stream into one buffer, for callers that want the
// whole utterance at once.
func (c *aliyunClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	ch, err := c.SynthesizeStream(ctx, text)
	if err != nil {
		return nil, err
	}
	var pcm []byte
	for chunk := range ch {
		if chunk.Err != nil {
			return nil, chunk.Err
		}
		pcm = append(pcm, chunk.PCM...)
	}
	return pcm, nil
}
