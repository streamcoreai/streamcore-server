package tts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	cartesiaDefaultWSURL = "wss://api.cartesia.ai/tts/websocket"
	cartesiaWSVersion    = "2025-04-16"
	// cartesiaWSPendingBuf is the per-context response channel buffer.
	// 64 slots is enough for even very long sentences while keeping
	// memory modest (64 × pointer size).
	cartesiaWSPendingBuf = 64
)

// cartesiaWSClient implements the Client interface using Cartesia's WebSocket
// TTS API. It maintains a single persistent connection, eliminating per-request
// TLS handshake overhead (~65-195 ms per sentence). The connection is
// established lazily on first use and auto-reconnects on failure.
type cartesiaWSClient struct {
	apiKey  string
	voiceID string
	wsURL   string

	// connMu protects conn and connected. It is also held during writes
	// to serialise WebSocket write operations (gorilla/websocket requires
	// at most one concurrent writer).
	connMu    sync.Mutex
	conn      *websocket.Conn
	connected bool

	// pendingMu protects the pending map which routes incoming WebSocket
	// messages to the correct SynthesizeStream caller via context_id.
	pendingMu sync.Mutex
	pending   map[string]chan *wsResponse

	// closeCh is closed when Close() is called, signalling the readLoop
	// and bridge goroutines to exit.
	closeCh   chan struct{}
	closeOnce sync.Once
}

// wsResponse represents a message received from the Cartesia WebSocket API.
type wsResponse struct {
	Type       string `json:"type"`
	Data       string `json:"data"`
	Done       bool   `json:"done"`
	ContextID  string `json:"context_id"`
	StatusCode int    `json:"status_code"`

	// Error fields (present when type == "error")
	Title     string `json:"title"`
	Message   string `json:"message"`
	ErrorCode string `json:"error_code"`
}

// wsGenerationRequest is the JSON payload sent to request speech synthesis.
type wsGenerationRequest struct {
	ModelID          string              `json:"model_id"`
	Transcript       string              `json:"transcript"`
	Voice            wsVoiceSpecifier    `json:"voice"`
	OutputFormat     wsOutputFormat      `json:"output_format"`
	GenerationConfig *wsGenerationConfig `json:"generation_config,omitempty"`
	ContextID        string              `json:"context_id"`
	Continue         bool                `json:"continue"`
}

// wsGenerationConfig controls sonic-3 audio attributes. Speed is explicitly
// pinned to 1.0 to prevent the model from varying playback speed across
// requests.
type wsGenerationConfig struct {
	Speed float64 `json:"speed"`
}

type wsVoiceSpecifier struct {
	Mode string `json:"mode"`
	ID   string `json:"id"`
}

type wsOutputFormat struct {
	Container  string `json:"container"`
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sample_rate"`
}

// NewCartesiaWSClient creates a Cartesia TTS client backed by the WebSocket
// API. The connection is established lazily on first synthesis request.
// voiceID defaults to Katie if empty. wsURL overrides the WebSocket
// endpoint (intended for local dev — see config.CartesiaConfig.WSURL).
// Empty wsURL means production wss://api.cartesia.ai/tts/websocket.
func NewCartesiaWSClient(apiKey, voiceID, wsURL string) Client {
	if voiceID == "" {
		voiceID = defaultCartesiaVoiceID
	}
	endpoint := cartesiaDefaultWSURL
	if wsURL != "" {
		endpoint = wsURL
		log.Printf("[tts:cartesia-ws] ws URL override active: %s", endpoint)
	}
	return &cartesiaWSClient{
		apiKey:  apiKey,
		voiceID: voiceID,
		wsURL:   endpoint,
		pending: make(map[string]chan *wsResponse),
		closeCh: make(chan struct{}),
	}
}

// Close shuts down the WebSocket connection and stops the read loop.
// It is safe to call multiple times. The Client interface does not require
// Close, but callers can use it via type assertion for explicit cleanup.
func (c *cartesiaWSClient) Close() {
	c.closeOnce.Do(func() {
		close(c.closeCh)

		// Unblock any bridge goroutines waiting for responses.
		c.notifyAllError(fmt.Errorf("cartesia ws: client closed"))

		c.connMu.Lock()
		defer c.connMu.Unlock()

		if c.conn != nil {
			// Best-effort close frame.
			_ = c.conn.WriteMessage(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			)
			c.conn.Close()
			c.conn = nil
			c.connected = false
		}

		log.Println("[tts:cartesia-ws] closed")
	})
}

// ---------------------------------------------------------------------------
// Connection management
// ---------------------------------------------------------------------------

// ensureConnected dials the Cartesia WebSocket if no connection is active.
// It is safe to call concurrently; the mutex serialises dial attempts.
func (c *cartesiaWSClient) ensureConnected(ctx context.Context) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.connected && c.conn != nil {
		return nil
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.apiKey)
	header.Set("Cartesia-Version", cartesiaWSVersion)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, c.wsURL, header)
	if err != nil {
		return fmt.Errorf("cartesia ws dial: %w", err)
	}

	c.conn = conn
	c.connected = true

	go c.readLoop(conn)

	log.Printf("[tts:cartesia-ws] connected to %s", c.wsURL)
	return nil
}

// readLoop reads messages from the WebSocket and dispatches them to the
// correct pending context channel. It runs in its own goroutine and exits
// when the connection drops or Close() is called.
//
// The caller's conn is captured so that the deferred cleanup only resets
// c.conn when it still points to *our* connection (not a newer one created
// by a subsequent reconnect).
func (c *cartesiaWSClient) readLoop(conn *websocket.Conn) {
	defer func() {
		// Only clear the shared pointer if it still refers to our connection.
		c.connMu.Lock()
		if c.conn == conn {
			c.conn = nil
			c.connected = false
		}
		c.connMu.Unlock()
		conn.Close()
	}()

	for {
		// Exit early if the client is shutting down.
		select {
		case <-c.closeCh:
			return
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if !isNormalClose(err) {
				log.Printf("[tts:cartesia-ws] read error: %v", err)
			}
			// Notify every pending caller so their bridge goroutines unblock.
			c.notifyAllError(fmt.Errorf("cartesia ws: connection lost: %w", err))
			return
		}

		var resp wsResponse
		if err := json.Unmarshal(message, &resp); err != nil {
			log.Printf("[tts:cartesia-ws] parse error: %v", err)
			continue
		}

		ch := c.getPending(resp.ContextID)
		if ch == nil {
			// No pending caller for this context_id (already cleaned up).
			continue
		}

		// Forward the response. Use a timeout so a stuck consumer cannot
		// block the readLoop indefinitely.
		select {
		case ch <- &resp:
		case <-time.After(3 * time.Second):
			log.Printf("[tts:cartesia-ws] timeout dispatching to context %s", resp.ContextID)
		case <-c.closeCh:
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Client interface
// ---------------------------------------------------------------------------

// SynthesizeStream produces PCM audio incrementally via a channel. It sends a
// generation request over the persistent WebSocket and returns a channel that
// yields PCM chunks as Cartesia generates them.
//
// If ctx is cancelled (e.g. barge-in), a cancel message is sent to Cartesia so
// it stops generating on the server side.
func (c *cartesiaWSClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	return c.SynthesizeStreamWithControls(ctx, text, VoiceControls{})
}

// SynthesizeStreamWithControls is SynthesizeStream with per-utterance voice
// controls applied. Cartesia exposes a playback-speed multiplier on the
// generation config; tone presets map onto it (warm/empathetic slightly
// slower, excited slightly faster). Speed is clamped to a conversational
// band so a bad tag can never produce comedy audio.
func (c *cartesiaWSClient) SynthesizeStreamWithControls(ctx context.Context, text string, vc VoiceControls) (<-chan StreamChunk, error) {
	if err := c.ensureConnected(ctx); err != nil {
		return nil, fmt.Errorf("cartesia ws connect: %w", err)
	}

	speed := vc.Speed
	if speed == 0 {
		speed = 1.0
	}
	if speed < 0.8 {
		speed = 0.8
	}
	if speed > 1.2 {
		speed = 1.2
	}

	contextID := uuid.New().String()
	respCh := make(chan *wsResponse, cartesiaWSPendingBuf)
	c.registerPending(contextID, respCh)

	req := wsGenerationRequest{
		ModelID:    "sonic-3",
		Transcript: text,
		Voice: wsVoiceSpecifier{
			Mode: "id",
			ID:   c.voiceID,
		},
		OutputFormat: wsOutputFormat{
			Container:  "raw",
			Encoding:   "pcm_s16le",
			SampleRate: 16000,
		},
		GenerationConfig: &wsGenerationConfig{Speed: speed},
		ContextID:        contextID,
		Continue:         false,
	}

	if err := c.writeJSON(req); err != nil {
		c.unregisterPending(contextID)
		return nil, fmt.Errorf("cartesia ws send: %w", err)
	}

	outCh := make(chan StreamChunk, 16)

	// Bridge goroutine: convert wsResponse messages into StreamChunk values
	// for the pipeline consumer.
	go func() {
		defer close(outCh)
		defer c.unregisterPending(contextID)

		totalBytes := 0
		for {
			select {
			case resp, ok := <-respCh:
				if !ok || resp == nil {
					return
				}

				if resp.Type == "error" {
					msg := resp.Message
					if msg == "" {
						msg = resp.Title
					}
					outCh <- StreamChunk{Err: fmt.Errorf("cartesia ws [%s]: %s", resp.ErrorCode, msg)}
					return
				}

				if resp.Type == "chunk" && resp.Data != "" {
					pcm, err := base64.StdEncoding.DecodeString(resp.Data)
					if err != nil {
						outCh <- StreamChunk{Err: fmt.Errorf("cartesia ws base64 decode: %w", err)}
						return
					}
					if len(pcm) > 0 {
						totalBytes += len(pcm)
						outCh <- StreamChunk{PCM: pcm}
					}
				}

				if resp.Done {
					// Log total bytes, expected duration, and actual chunk
					// count for diagnosing speed/sample-rate issues. At
					// 16kHz mono 16-bit: 32000 bytes/sec → 32 bytes/ms.
					durationMs := float64(totalBytes) / 32.0
					log.Printf("[tts:cartesia-ws] streamed %d bytes (%.0f ms expected) for %d chars of text",
						totalBytes, durationMs, len(text))
					return
				}

			case <-ctx.Done():
				// Pipeline cancelled (e.g. barge-in). Tell Cartesia to
				// stop generating for this context.
				c.sendCancel(contextID)
				return
			}
		}
	}()

	return outCh, nil
}

// Synthesize produces the full audio for text in one shot by collecting all
// chunks from SynthesizeStream.
func (c *cartesiaWSClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	stream, err := c.SynthesizeStream(ctx, text)
	if err != nil {
		return nil, err
	}

	var buf []byte
	for chunk := range stream {
		if chunk.Err != nil {
			return nil, chunk.Err
		}
		buf = append(buf, chunk.PCM...)
	}

	log.Printf("[tts:cartesia-ws] synthesized %d bytes for %d chars of text", len(buf), len(text))
	return buf, nil
}

// ---------------------------------------------------------------------------
// WebSocket helpers
// ---------------------------------------------------------------------------

// writeJSON marshals v and writes it as a text message. connMu must NOT be
// held by the caller; this method acquires it to serialise writes.
func (c *cartesiaWSClient) writeJSON(v interface{}) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("cartesia ws: not connected")
	}

	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("cartesia ws marshal: %w", err)
	}

	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		// Connection is likely broken; clean up so the next call reconnects.
		c.connected = false
		c.conn.Close()
		c.conn = nil
		return fmt.Errorf("cartesia ws write: %w", err)
	}

	return nil
}

// sendCancel sends a best-effort cancel request for the given context ID.
func (c *cartesiaWSClient) sendCancel(contextID string) {
	msg := struct {
		ContextID string `json:"context_id"`
		Cancel    bool   `json:"cancel"`
	}{
		ContextID: contextID,
		Cancel:    true,
	}
	if err := c.writeJSON(msg); err != nil {
		log.Printf("[tts:cartesia-ws] cancel send failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Pending context management
// ---------------------------------------------------------------------------

func (c *cartesiaWSClient) registerPending(id string, ch chan *wsResponse) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	c.pending[id] = ch
}

func (c *cartesiaWSClient) unregisterPending(id string) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	delete(c.pending, id)
}

func (c *cartesiaWSClient) getPending(id string) chan *wsResponse {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	return c.pending[id]
}

// notifyAllError sends an error response to every pending context and clears
// the map. Used when the WebSocket connection is lost or the client is closed.
func (c *cartesiaWSClient) notifyAllError(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	for id, ch := range c.pending {
		select {
		case ch <- &wsResponse{
			Type:      "error",
			Done:      true,
			ContextID: id,
			Title:     "Connection error",
			Message:   err.Error(),
		}:
		default:
			// Channel full — bridge goroutine is behind or gone.
		}
		delete(c.pending, id)
	}
}

// isNormalClose returns true if the error is a normal WebSocket close.
func isNormalClose(err error) bool {
	return websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
	)
}
