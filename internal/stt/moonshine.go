package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	msginterfaces "github.com/deepgram/deepgram-go-sdk/v3/pkg/api/listen/v1/websocket/interfaces"
)

// moonshineClient connects to the Moonshine STT WebSocket server
// (external/moonshine/moonshineStt) and implements the STT Client interface.
//
// The sidecar answers in Deepgram's live-transcription frames, so they are
// decoded into the SDK's own types and handed to deepgramCallback instead of
// a second accumulator. Merging overlapping finals, suppressing an immediate
// repeat, and flushing on UtteranceEnd are decisions this pipeline already
// made once for Deepgram; a local model does not need them made differently.
type moonshineClient struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
	cb     *deepgramCallback
	mu     sync.Mutex
	closed bool
}

// moonshineErrorFrame is the sidecar's error event. Deepgram's own error type
// carries more fields than a local process has anything to put in.
type moonshineErrorFrame struct {
	Description string `json:"description"`
	Message     string `json:"message"`
}

func NewMoonshineClient(ctx context.Context, wsURL string, onResult func(TranscriptResult)) (*moonshineClient, error) {
	sttCtx, cancel := context.WithCancel(ctx)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(sttCtx, wsURL, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("moonshine stt connect %s: %w", wsURL, err)
	}

	c := &moonshineClient{
		conn:   conn,
		cancel: cancel,
		cb:     &deepgramCallback{onResult: onResult},
	}

	go c.readLoop()

	log.Printf("[stt] connected to Moonshine STT at %s", wsURL)
	return c, nil
}

func (c *moonshineClient) readLoop() {
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if !closed {
				log.Printf("[stt] moonshine read error: %v", err)
			}
			return
		}

		if err := routeMoonshineSTTFrame(message, c.cb); err != nil {
			log.Printf("[stt] moonshine parse error: %v", err)
		}
	}
}

// routeMoonshineSTTFrame decodes one sidecar frame and dispatches it to cb.
// An unrecognised type is dropped rather than reported: the sidecar sends
// Metadata the pipeline has no use for, and the Deepgram SDK likewise ignores
// events with no callback behind them.
func routeMoonshineSTTFrame(data []byte, cb *deepgramCallback) error {
	var header msginterfaces.MessageType
	if err := json.Unmarshal(data, &header); err != nil {
		return fmt.Errorf("frame is not JSON: %w", err)
	}

	switch header.Type {
	case "Results":
		var msg msginterfaces.MessageResponse
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("parse Results: %w", err)
		}
		return cb.Message(&msg)

	case "SpeechStarted":
		var msg msginterfaces.SpeechStartedResponse
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("parse SpeechStarted: %w", err)
		}
		return cb.SpeechStarted(&msg)

	case "UtteranceEnd":
		var msg msginterfaces.UtteranceEndResponse
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("parse UtteranceEnd: %w", err)
		}
		return cb.UtteranceEnd(&msg)

	case "Error":
		var msg moonshineErrorFrame
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("parse Error: %w", err)
		}
		log.Printf("[stt] moonshine sidecar error: %s (%s)", msg.Description, msg.Message)
	}

	return nil
}

// SendAudio sends raw linear16 PCM bytes to the Moonshine STT server.
func (c *moonshineClient) SendAudio(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("moonshine: connection closed")
	}
	return c.conn.WriteMessage(websocket.BinaryMessage, data)
}

// Close shuts down the WebSocket connection.
func (c *moonshineClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true

	c.conn.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
	)
	c.conn.Close()
	c.cancel()
	log.Println("[stt] moonshine closed")
}
