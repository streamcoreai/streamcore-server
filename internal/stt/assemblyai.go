package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/streamcoreai/server/internal/config"
)

// AssemblyAI Universal-Streaming v3 protocol notes
// -----------------------------------------------
//   Endpoint:  wss://streaming.assemblyai.com/v3/ws
//   Auth:      Authorization: <API_KEY>   (no "Bearer" prefix)
//   Audio:     raw 16-bit signed little-endian PCM at 16kHz mono, sent as
//              binary WebSocket frames.
//   Messages:  JSON text frames. Discriminator is the "type" field.
//              - "Begin"        — session started
//              - "Turn"         — partial or final transcript for the current
//                                  turn; `end_of_turn=true` marks the final
//              - "Termination"  — session ended
//   Shutdown:  send {"type":"Terminate"} as a text frame, then close the conn.
//
// The client below mirrors the shape of NewDeepgramClient: it returns a value
// that satisfies the stt.Client interface (SendAudio + Close) and forwards
// transcript events through the provided onResult callback.

const (
	assemblyAIStreamURL  = "wss://streaming.assemblyai.com/v3/ws"
	assemblyAISampleRate = 16000

	// Websocket write deadline guards against a hung remote — if a single
	// audio frame takes longer than this to flush, something is wrong and
	// we'd rather error than wedge the audio pipeline.
	assemblyAIWriteTimeout = 5 * time.Second
)

type assemblyAIClient struct {
	conn     *websocket.Conn
	onResult func(TranscriptResult)
	ctx      context.Context
	cancel   context.CancelFunc

	writeMu sync.Mutex // gorilla/websocket requires single concurrent writer
	closed  atomic.Bool
	readWG  sync.WaitGroup

	// lastFinal/lastFinalAt deduplicate identical finals emitted within a
	// short window, matching deepgram.go's recentFinalSuppressWindow guard.
	mu                sync.Mutex
	lastEmittedText   string
	lastEmittedFinal  string
	lastFinalAt       time.Time
	suppressDuplicate time.Duration
}

func NewAssemblyAIClient(ctx context.Context, cfg config.AssemblyAIConfig, onResult func(TranscriptResult)) (*assemblyAIClient, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("assemblyai: api_key is required")
	}

	sttCtx, cancel := context.WithCancel(ctx)

	endpoint := buildAssemblyAIURL(cfg)
	header := http.Header{}
	header.Set("Authorization", cfg.APIKey)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(sttCtx, endpoint, header)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("assemblyai: dial: %w", err)
	}

	c := &assemblyAIClient{
		conn:              conn,
		onResult:          onResult,
		ctx:               sttCtx,
		cancel:            cancel,
		suppressDuplicate: 5 * time.Second,
	}

	c.readWG.Add(1)
	go c.readLoop()

	log.Printf("[stt:assemblyai] connected (model=%s)", cfg.Model)
	if len(cfg.Keyterms) > 0 {
		log.Printf("[stt:assemblyai] keyterms loaded: count=%d", len(cfg.Keyterms))
	}
	return c, nil
}

// assemblyAILanguageCode maps a tenant BCP-47 locale (e.g. "en-IN", "es-US")
// to AssemblyAI's `language_code` query parameter, which expects ISO 639-1
// (e.g. "en", "es"). Empty input returns empty so we omit the parameter and
// let the model auto-detect.
func assemblyAILanguageCode(locale string) string {
	base := strings.ToLower(strings.TrimSpace(locale))
	if idx := strings.Index(base, "-"); idx > 0 {
		base = base[:idx]
	}
	return base
}

// buildAssemblyAIURL composes the wss endpoint with query parameters.
// Kept as a free function so it's trivial to unit-test if needed.
func buildAssemblyAIURL(cfg config.AssemblyAIConfig) string {
	q := url.Values{}
	q.Set("sample_rate", strconv.Itoa(assemblyAISampleRate))
	q.Set("encoding", "pcm_s16le")
	if cfg.Model != "" {
		q.Set("speech_model", cfg.Model)
	}
	if code := assemblyAILanguageCode(cfg.Language); code != "" {
		q.Set("language_code", code)
	}
	formatTurns := true
	if cfg.FormatTurns != nil {
		formatTurns = *cfg.FormatTurns
	}
	if formatTurns {
		q.Set("format_turns", "true")
	} else {
		q.Set("format_turns", "false")
	}
	if cfg.EndOfTurnSilenceMs > 0 {
		q.Set("end_of_turn_confidence_threshold_silence_duration_ms", strconv.Itoa(cfg.EndOfTurnSilenceMs))
	}
	if len(cfg.Keyterms) > 0 {
		// AssemblyAI accepts repeated keyterm_prompt params.
		for _, kw := range cfg.Keyterms {
			if strings.TrimSpace(kw) != "" {
				q.Add("keyterm_prompt", kw)
			}
		}
	}
	return assemblyAIStreamURL + "?" + q.Encode()
}

// SendAudio forwards raw linear16 PCM bytes to AssemblyAI as a binary frame.
// Returns an error if the connection is closed or the write times out.
func (c *assemblyAIClient) SendAudio(data []byte) error {
	if c.closed.Load() {
		return fmt.Errorf("assemblyai: client closed")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(assemblyAIWriteTimeout)); err != nil {
		return fmt.Errorf("assemblyai: set write deadline: %w", err)
	}
	if err := c.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		return fmt.Errorf("assemblyai: write audio: %w", err)
	}
	return nil
}

// Close gracefully shuts down the AssemblyAI session: sends Terminate, closes
// the socket, cancels the context, and waits for the read loop to exit.
func (c *assemblyAIClient) Close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}

	c.writeMu.Lock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(assemblyAIWriteTimeout))
	_ = c.conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"Terminate"}`))
	c.writeMu.Unlock()

	_ = c.conn.Close()
	c.cancel()
	c.readWG.Wait()
	log.Println("[stt:assemblyai] closed")
}

// assemblyAITurnMsg is the subset of the v3 Turn message we care about.
// Unknown fields are silently ignored by encoding/json.
type assemblyAITurnMsg struct {
	Type            string           `json:"type"`
	TurnOrder       int              `json:"turn_order"`
	Transcript      string           `json:"transcript"`
	EndOfTurn       bool             `json:"end_of_turn"`
	TurnIsFormatted bool             `json:"turn_is_formatted"`
	EndOfTurnConf   float64          `json:"end_of_turn_confidence"`
	Words           []assemblyAIWord `json:"words"`
}

// assemblyAIWord is one element of the v3 Turn `words` array. We only consume
// the confidence; the text/timestamps are already reflected in `transcript`.
type assemblyAIWord struct {
	Confidence float64 `json:"confidence"`
}

// transcriptConfidence returns the mean per-word confidence for the turn, or
// 0 if no per-word confidences are available. AssemblyAI's v3 Turn message
// does not carry an aggregate transcript confidence, so we synthesise one
// from the word array (analogous to Deepgram's Alternative.Confidence).
func (t assemblyAITurnMsg) transcriptConfidence() float64 {
	if len(t.Words) == 0 {
		return 0
	}
	var sum float64
	var n int
	for _, w := range t.Words {
		if w.Confidence > 0 {
			sum += w.Confidence
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

type assemblyAIErrorMsg struct {
	Type    string `json:"type"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

func (c *assemblyAIClient) readLoop() {
	defer c.readWG.Done()
	for {
		if c.ctx.Err() != nil {
			return
		}

		msgType, payload, err := c.conn.ReadMessage()
		if err != nil {
			if !c.closed.Load() {
				log.Printf("[stt:assemblyai] read error: %v", err)
			}
			return
		}
		if msgType != websocket.TextMessage {
			continue
		}

		// Peek at the discriminator without unmarshalling the full payload twice.
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &head); err != nil {
			log.Printf("[stt:assemblyai] bad json: %v", err)
			continue
		}

		switch head.Type {
		case "Begin":
			log.Println("[stt:assemblyai] session started")
		case "Turn":
			var turn assemblyAITurnMsg
			if err := json.Unmarshal(payload, &turn); err != nil {
				log.Printf("[stt:assemblyai] bad turn payload: %v", err)
				continue
			}
			c.handleTurn(turn)
		case "Termination":
			log.Println("[stt:assemblyai] session terminated by server")
			return
		case "Error":
			var e assemblyAIErrorMsg
			_ = json.Unmarshal(payload, &e)
			log.Printf("[stt:assemblyai] error message: error=%q message=%q", e.Error, e.Message)
		default:
			// Unknown event — log once at trace level. Don't spam.
			log.Printf("[stt:assemblyai] unhandled event type=%q", head.Type)
		}
	}
}

func (c *assemblyAIClient) handleTurn(t assemblyAITurnMsg) {
	text := strings.TrimSpace(t.Transcript)
	if text == "" {
		return
	}

	confidence := t.transcriptConfidence()

	if t.EndOfTurn {
		// AssemblyAI emits the same end-of-turn twice when format_turns=true:
		// once unformatted, once formatted. Prefer the formatted version when
		// present. Either way, suppress duplicates of the same final within
		// the recent-final window.
		c.mu.Lock()
		if sameTranscript(text, c.lastEmittedFinal) && time.Since(c.lastFinalAt) < c.suppressDuplicate {
			c.mu.Unlock()
			return
		}
		c.lastEmittedFinal = text
		c.lastFinalAt = time.Now()
		c.lastEmittedText = ""
		c.mu.Unlock()
		log.Printf("[stt:assemblyai] final: %q (formatted=%v eot_conf=%.2f conf=%.2f)", text, t.TurnIsFormatted, t.EndOfTurnConf, confidence)
		c.onResult(TranscriptResult{Text: text, IsFinal: true, Confidence: confidence})
		return
	}

	// Partial — drop dupes so barge-in/UI logic doesn't see repeats.
	c.mu.Lock()
	if sameTranscript(text, c.lastEmittedText) {
		c.mu.Unlock()
		return
	}
	c.lastEmittedText = text
	c.mu.Unlock()
	log.Printf("[stt:assemblyai] partial: %q (conf=%.2f)", text, confidence)
	c.onResult(TranscriptResult{Text: text, IsFinal: false, Confidence: confidence})
}
