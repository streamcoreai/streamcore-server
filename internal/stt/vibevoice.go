package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// vibevoiceClient connects to the VibeVoice ASR WebSocket server
// (external/vibeVoice/vibeVoiceAsr) and implements the STT Client interface.
type vibevoiceClient struct {
	conn     *websocket.Conn
	cancel   context.CancelFunc
	onResult func(TranscriptResult)
	mu       sync.Mutex
	closed   bool
}

// vibevoiceTranscript matches the JSON sent by the ASR server.
type vibevoiceTranscript struct {
	Text    string `json:"text"`
	IsFinal bool   `json:"is_final"`
}

func NewVibeVoiceClient(ctx context.Context, wsURL string, onResult func(TranscriptResult)) (*vibevoiceClient, error) {
	sttCtx, cancel := context.WithCancel(ctx)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(sttCtx, wsURL, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("vibevoice asr connect %s: %w", wsURL, err)
	}

	c := &vibevoiceClient{
		conn:     conn,
		cancel:   cancel,
		onResult: onResult,
	}

	go c.readLoop()

	log.Printf("[stt] connected to VibeVoice ASR at %s", wsURL)
	return c, nil
}

// readLoop reads transcript results from the WebSocket.
func (c *vibevoiceClient) readLoop() {
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if !closed {
				log.Printf("[stt] vibevoice read error: %v", err)
			}
			return
		}

		var result vibevoiceTranscript
		if err := json.Unmarshal(message, &result); err != nil {
			log.Printf("[stt] vibevoice parse error: %v", err)
			continue
		}

		if result.Text != "" && c.onResult != nil {
			c.onResult(TranscriptResult{
				Text:    result.Text,
				IsFinal: result.IsFinal,
			})
		}
	}
}

// SendAudio sends raw linear16 PCM bytes to the VibeVoice ASR server.
func (c *vibevoiceClient) SendAudio(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("vibevoice: connection closed")
	}
	return c.conn.WriteMessage(websocket.BinaryMessage, data)
}

// Close shuts down the WebSocket connection.
func (c *vibevoiceClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		c.conn.Close()
		c.cancel()
		log.Println("[stt] vibevoice closed")
	}
}
