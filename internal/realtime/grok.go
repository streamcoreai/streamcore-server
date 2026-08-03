package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/streamcoreai/server/internal/audio"
	"github.com/streamcoreai/server/internal/config"
)

// grokDialURL is the Realtime endpoint. It is a variable so tests can point
// the client at a local server; nothing outside tests reassigns it.
var grokDialURL = "wss://api.x.ai/v1/realtime"

const (
	// grokWriteTimeout bounds a single write. Without it a stalled peer
	// blocks the writer forever while holding writeMu, which in turn hangs
	// Close and leaks the call.
	grokWriteTimeout = 10 * time.Second

	// grokPingInterval and grokReadTimeout detect a connection that died
	// without a close frame. The agent side of a call can be silent for a
	// long stretch while the caller talks, so silence alone proves nothing —
	// only a ping round trip does.
	grokPingInterval = 20 * time.Second
	grokReadTimeout  = 60 * time.Second
)

// GrokClient speaks xAI's Realtime API over a WebSocket. The wire protocol is
// OpenAI-Realtime-compatible apart from a few event names, so the event
// vocabulary below will look familiar.
//
// Audio uses binary transport in both directions: raw linear16 frames as
// WebSocket binary messages, with lifecycle events still arriving as JSON
// text messages. That skips base64 on the hot path — a 20ms frame is 640
// bytes of PCM but 856 bytes of base64, on top of the encode/decode cost 50
// times a second.
type GrokClient struct {
	ctx    context.Context
	cancel context.CancelFunc

	conn *websocket.Conn
	// writeMu serialises writes. Audio comes from the pipeline's inbound
	// goroutine while tool results come from tool-handler goroutines, and
	// gorilla permits only one concurrent writer.
	writeMu sync.Mutex

	h     Handlers
	debug bool

	// closeOnce guards teardown, which can be triggered by the caller, by
	// the read loop erroring, or by context cancellation.
	closeOnce sync.Once
}

// NewGrokClient dials the Realtime API, configures the session, and starts
// the read loop. It returns once the connection is established; session
// configuration is sent immediately but confirmed asynchronously.
func NewGrokClient(ctx context.Context, cfg config.GrokConfig, opts Options, h Handlers) (*GrokClient, error) {
	u, err := url.Parse(grokDialURL)
	if err != nil {
		return nil, fmt.Errorf("parse grok realtime url: %w", err)
	}
	q := u.Query()
	q.Set("model", cfg.Model)
	// reasoning.effort is documented as a query parameter on the handshake.
	if cfg.ReasoningEffort != "" {
		q.Set("reasoning.effort", cfg.ReasoningEffort)
	}
	u.RawQuery = q.Encode()

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 15 * time.Second

	conn, resp, err := dialer.DialContext(ctx, u.String(), http.Header{
		"Authorization": []string{"Bearer " + cfg.APIKey},
	})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("grok realtime dial: %w (status %s)", err, resp.Status)
		}
		return nil, fmt.Errorf("grok realtime dial: %w", err)
	}

	cCtx, cancel := context.WithCancel(ctx)
	c := &GrokClient{ctx: cCtx, cancel: cancel, conn: conn, h: h, debug: opts.Debug}

	if err := c.sendJSON(sessionUpdate(cfg, opts)); err != nil {
		conn.Close()
		cancel()
		return nil, fmt.Errorf("grok session.update: %w", err)
	}

	log.Printf("[grok] connected model=%s voice=%s reasoning=%s tools=%d",
		cfg.Model, cfg.Voice, cfg.ReasoningEffort, len(opts.Tools))

	go c.readLoop()
	go c.keepalive()
	go func() {
		<-cCtx.Done()
		c.Close()
	}()

	return c, nil
}

// sessionUpdate builds the session.update payload. Audio is pinned to
// audio.SampleRate in both directions so nothing resamples between the model
// and the Opus codec — Grok is natively 24kHz and resamples on its side.
func sessionUpdate(cfg config.GrokConfig, opts Options) map[string]any {
	instructions := cfg.SystemPrompt
	if opts.InstructionsSuffix != "" {
		instructions += "\n\n" + opts.InstructionsSuffix
	}

	session := map[string]any{
		"instructions": instructions,
		"audio": map[string]any{
			"input": map[string]any{
				"format":    map[string]any{"type": "audio/pcm", "rate": audio.SampleRate},
				"transport": "binary",
			},
			"output": map[string]any{
				"format":    map[string]any{"type": "audio/pcm", "rate": audio.SampleRate},
				"transport": "binary",
			},
		},
	}
	if cfg.Voice != "" {
		session["voice"] = cfg.Voice
	}

	// Server-side VAD owns turn taking and barge-in. Only non-zero tunables
	// are sent so the provider defaults stay in force otherwise.
	td := map[string]any{"type": "server_vad"}
	if cfg.VADThreshold > 0 {
		td["threshold"] = cfg.VADThreshold
	}
	if cfg.SilenceDurationMs > 0 {
		td["silence_duration_ms"] = cfg.SilenceDurationMs
	}
	if cfg.PrefixPaddingMs > 0 {
		td["prefix_padding_ms"] = cfg.PrefixPaddingMs
	}
	if cfg.IdleTimeoutMs > 0 {
		td["idle_timeout_ms"] = cfg.IdleTimeoutMs
	}
	session["turn_detection"] = td

	// The model hears the caller directly; transcription exists only so the
	// client UI still gets a transcript to display.
	if cfg.Transcription != nil && *cfg.Transcription {
		tr := map[string]any{"model": "grok-transcribe"}
		if cfg.LanguageHint != "" {
			tr["language_hint"] = cfg.LanguageHint
		}
		if len(cfg.Keyterms) > 0 {
			tr["keyterms"] = cfg.Keyterms
		}
		in := session["audio"].(map[string]any)["input"].(map[string]any)
		in["transcription"] = tr
	}

	toolList := make([]map[string]any, 0, len(opts.Tools)+2)
	if cfg.WebSearch {
		toolList = append(toolList, map[string]any{"type": "web_search"})
	}
	if cfg.XSearch {
		toolList = append(toolList, map[string]any{"type": "x_search"})
	}
	for _, t := range opts.Tools {
		params := json.RawMessage(t.Parameters)
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		toolList = append(toolList, map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  params,
		})
	}
	if len(toolList) > 0 {
		session["tools"] = toolList
	}

	return map[string]any{"type": "session.update", "session": session}
}

// SendAudio streams caller PCM to the model as a binary frame.
func (c *GrokClient) SendAudio(pcm []byte) error {
	return c.write(websocket.BinaryMessage, pcm)
}

// write serialises access to the connection and bounds how long a single
// write may block. gorilla permits only one concurrent writer, and audio
// (pipeline goroutine) races tool results (handler goroutines).
func (c *GrokClient) write(msgType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(grokWriteTimeout)); err != nil {
		return err
	}
	return c.conn.WriteMessage(msgType, data)
}

// Speak makes the agent utter text unprompted.
//
// This uses a per-response instructions override rather than seeding a user
// message: a user message would make the model *reply to* the text instead of
// saying it, so a configured greeting would come out as an answer to itself.
// The override applies to this response only; the session prompt is back in
// force on the next turn.
func (c *GrokClient) Speak(text string) error {
	return c.sendJSON(map[string]any{
		"type": "response.create",
		"response": map[string]any{
			"instructions": "Open the conversation by saying exactly this, word for word, " +
				"adding nothing before or after it: " + text,
		},
	})
}

func (c *GrokClient) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		// Best-effort close handshake; the peer may already be gone.
		// WriteControl is safe to call alongside an in-flight WriteMessage,
		// so this deliberately does not take writeMu — waiting on a stalled
		// audio write is exactly the hang this is trying to avoid.
		_ = c.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(time.Second))
		c.conn.Close()
		log.Println("[grok] connection closed")
	})
	return nil
}

func (c *GrokClient) sendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.write(websocket.TextMessage, data)
}

// keepalive pings the server so a connection that dies without a close frame
// is noticed. WriteControl is safe alongside the writer goroutines, so this
// deliberately bypasses writeMu — taking it here would let a stalled audio
// write suppress the very pings meant to detect the stall.
func (c *GrokClient) keepalive() {
	ticker := time.NewTicker(grokPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			deadline := time.Now().Add(grokWriteTimeout)
			if err := c.conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				if c.ctx.Err() == nil {
					log.Printf("[grok] keepalive ping failed: %v", err)
				}
				return
			}
		}
	}
}

// grokEvent is the envelope common to every JSON server event. Payload
// fields are pulled per-type since they vary.
type grokEvent struct {
	Type string `json:"type"`

	// Transcript events
	Transcript string `json:"transcript"`
	Delta      string `json:"delta"`

	// Function calling
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	Arguments json.RawMessage `json:"arguments"`

	// Errors
	Error *struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *GrokClient) readLoop() {
	defer c.Close()

	// Any traffic proves the connection is alive, so the deadline is pushed
	// out on every message as well as on pong.
	_ = c.conn.SetReadDeadline(time.Now().Add(grokReadTimeout))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(grokReadTimeout))
	})

	for {
		msgType, data, err := c.conn.ReadMessage()
		if err != nil {
			if c.ctx.Err() == nil {
				log.Printf("[grok] read error: %v", err)
			}
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(grokReadTimeout))

		// Binary frames are agent audio — the hot path, handled first.
		if msgType == websocket.BinaryMessage {
			if c.h.OnAudio != nil && len(data) > 0 {
				c.h.OnAudio(data)
			}
			continue
		}

		var ev grokEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			log.Printf("[grok] malformed event: %v", err)
			continue
		}
		c.handleEvent(&ev, data)
	}
}

func (c *GrokClient) handleEvent(ev *grokEvent, raw []byte) {
	if c.debug {
		// Transcript payloads are included because how a provider splits and
		// revises a turn is exactly what goes wrong here.
		switch {
		case ev.Transcript != "":
			log.Printf("[grok] event %s transcript=%q", ev.Type, ev.Transcript)
		case ev.Delta != "":
			log.Printf("[grok] event %s delta=%q", ev.Type, ev.Delta)
		default:
			log.Printf("[grok] event %s", ev.Type)
		}
	}

	switch ev.Type {
	case "session.created", "conversation.created":
		// Handshake chatter; session.update is already in flight.

	case "response.created":
		if c.h.OnResponseStarted != nil {
			c.h.OnResponseStarted()
		}

	case "session.updated":
		log.Println("[grok] session configured")

	case "input_audio_buffer.speech_started":
		if c.h.OnSpeechStarted != nil {
			c.h.OnSpeechStarted()
		}

	case "input_audio_buffer.speech_stopped":
		if c.h.OnSpeechStopped != nil {
			c.h.OnSpeechStopped()
		}

	// xAI renames OpenAI's ...transcription.delta to .updated, and the
	// payload is the cumulative transcript rather than an increment — it may
	// revise words it emitted earlier.
	case "conversation.item.input_audio_transcription.updated":
		if c.h.OnUserTranscript != nil && ev.Transcript != "" {
			c.h.OnUserTranscript(ev.Transcript, false)
		}

	case "conversation.item.input_audio_transcription.completed":
		if c.h.OnUserTranscript != nil && ev.Transcript != "" {
			c.h.OnUserTranscript(ev.Transcript, true)
		}

	// Both spellings are emitted depending on client compatibility mode.
	case "response.output_audio_transcript.delta", "response.audio_transcript.delta",
		"response.output_text.delta", "response.text.delta":
		if c.h.OnAgentTranscript != nil && ev.Delta != "" {
			c.h.OnAgentTranscript(ev.Delta)
		}

	// Audio only arrives here if the server ignored the binary transport
	// request; decoding it keeps the session working rather than going mute.
	case "response.output_audio.delta", "response.audio.delta":
		c.handleJSONAudio(raw)

	case "response.done":
		if c.h.OnResponseDone != nil {
			c.h.OnResponseDone()
		}

	case "response.function_call_arguments.done":
		go c.runToolCall(ev.Name, ev.CallID, ev.Arguments)

	case "error":
		if ev.Error != nil {
			log.Printf("[grok] api error: type=%s code=%s %s", ev.Error.Type, ev.Error.Code, ev.Error.Message)
		} else {
			log.Printf("[grok] api error: %s", string(raw))
		}
	}
}

// handleJSONAudio decodes a base64 audio delta. Only reached when the server
// falls back to JSON transport despite the binary request.
func (c *GrokClient) handleJSONAudio(raw []byte) {
	var payload struct {
		Audio []byte `json:"audio"` // encoding/json base64-decodes []byte
		Delta []byte `json:"delta"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		log.Printf("[grok] audio delta decode: %v", err)
		return
	}
	pcm := payload.Delta
	if len(pcm) == 0 {
		pcm = payload.Audio
	}
	if c.h.OnAudio != nil && len(pcm) > 0 {
		c.h.OnAudio(pcm)
	}
}

// runToolCall executes a function call and returns its output to the model.
// Errors are handed back as text rather than dropped, so the agent can tell
// the caller something went wrong instead of going silent.
func (c *GrokClient) runToolCall(name, callID string, args json.RawMessage) {
	if c.h.OnToolCall == nil {
		log.Printf("[grok] tool call %q with no handler registered", name)
		return
	}

	log.Printf("[grok] tool call: %s", name)
	output, err := c.h.OnToolCall(c.ctx, name, args)
	if err != nil {
		log.Printf("[grok] tool %q failed: %v", name, err)
		output = "Error: " + err.Error()
	}

	if err := c.sendJSON(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  output,
		},
	}); err != nil {
		if c.ctx.Err() == nil {
			log.Printf("[grok] send tool output: %v", err)
		}
		return
	}

	// The model does not resume on its own after a tool result.
	if err := c.sendJSON(map[string]any{"type": "response.create"}); err != nil {
		if c.ctx.Err() == nil {
			log.Printf("[grok] resume after tool: %v", err)
		}
	}
}
