package msbridge

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ProtooClient implements the protoo request/response protocol over WebSocket,
// as used by the mediasoup demo server for regular peers.
//
// Protocol wire format:
//
//	Request:      {"request":true, "id":<n>, "method":"<m>", "data":{...}}
//	Response OK:  {"response":true, "id":<n>, "ok":true, "data":{...}}
//	Response ERR: {"response":true, "id":<n>, "ok":false, "errorCode":<n>, "errorReason":"..."}
//	Notification: {"notification":true, "method":"<m>", "data":{...}}
type ProtooClient struct {
	conn *websocket.Conn

	reqID    atomic.Uint64
	mu       sync.Mutex
	pending  map[uint64]chan protooResponse
	closed   atomic.Bool
	closedCh chan struct{}

	// OnNotification is called for server-pushed notifications.
	OnNotification func(method string, data json.RawMessage)

	// OnRequest is called for server-initiated requests (e.g. "newConsumer").
	// It receives the method and data, and should return response data (or nil)
	// and an error. If nil, the request is auto-accepted with empty data.
	OnRequest func(method string, data json.RawMessage) (json.RawMessage, error)
}

type protooRequest struct {
	Request bool            `json:"request"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type protooResponse struct {
	Response    bool            `json:"response"`
	ID          uint64          `json:"id"`
	OK          bool            `json:"ok"`
	Data        json.RawMessage `json:"data,omitempty"`
	ErrorCode   int             `json:"errorCode,omitempty"`
	ErrorReason string          `json:"errorReason,omitempty"`
}

type protooNotification struct {
	Notification bool            `json:"notification"`
	Method       string          `json:"method"`
	Data         json.RawMessage `json:"data,omitempty"`
}

// protooMessage is used for initial dispatch.
type protooMessage struct {
	Request      bool            `json:"request"`
	Response     bool            `json:"response"`
	Notification bool            `json:"notification"`
	ID           uint64          `json:"id"`
	OK           bool            `json:"ok"`
	Method       string          `json:"method"`
	Data         json.RawMessage `json:"data,omitempty"`
	ErrorCode    int             `json:"errorCode,omitempty"`
	ErrorReason  string          `json:"errorReason,omitempty"`
}

// NewProtooClient connects to the mediasoup server's WebSocket endpoint.
// URL format: wss://host:port/?roomId=xxx&peerId=xxx
func NewProtooClient(signalingURL, roomID, peerID, originHeader string) (*ProtooClient, error) {
	// Build WebSocket URL from the HTTP signaling URL.
	u, err := url.Parse(signalingURL)
	if err != nil {
		return nil, fmt.Errorf("parse signaling URL: %w", err)
	}

	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
		// already correct
	default:
		u.Scheme = "wss"
	}

	q := u.Query()
	q.Set("roomId", roomID)
	q.Set("peerId", peerID)
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
		HandshakeTimeout: 10 * time.Second,
		Subprotocols:     []string{"protoo"},
	}

	header := http.Header{}
	if originHeader != "" {
		header.Set("Origin", originHeader)
	}

	conn, resp, err := dialer.Dial(u.String(), header)
	if err != nil {
		if resp != nil {
			body := make([]byte, 512)
			n, _ := resp.Body.Read(body)
			resp.Body.Close()
			return nil, fmt.Errorf("protoo ws dial %s: %w (HTTP %d: %s)", u.String(), err, resp.StatusCode, string(body[:n]))
		}
		return nil, fmt.Errorf("protoo ws dial %s: %w", u.String(), err)
	}

	pc := &ProtooClient{
		conn:     conn,
		pending:  make(map[uint64]chan protooResponse),
		closedCh: make(chan struct{}),
	}

	go pc.readLoop()

	return pc, nil
}

// Request sends a protoo request and waits for the response.
func (c *ProtooClient) Request(method string, data interface{}) (json.RawMessage, error) {
	if c.closed.Load() {
		return nil, fmt.Errorf("protoo client closed")
	}

	id := c.reqID.Add(1)

	var rawData json.RawMessage
	if data != nil {
		var err error
		rawData, err = json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("marshal request data: %w", err)
		}
	}

	req := protooRequest{
		Request: true,
		ID:      id,
		Method:  method,
		Data:    rawData,
	}

	ch := make(chan protooResponse, 1)

	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.conn.WriteJSON(req); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	select {
	case resp := <-ch:
		if !resp.OK {
			return nil, fmt.Errorf("protoo error %d: %s", resp.ErrorCode, resp.ErrorReason)
		}
		return resp.Data, nil
	case <-c.closedCh:
		return nil, fmt.Errorf("protoo connection closed while waiting for response")
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("protoo request %q timed out", method)
	}
}

// Close shuts down the WebSocket connection.
func (c *ProtooClient) Close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	close(c.closedCh)
	c.conn.Close()
}

func (c *ProtooClient) readLoop() {
	defer c.Close()

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if !c.closed.Load() {
				log.Printf("[protoo] read error: %v", err)
			}
			return
		}

		var msg protooMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("[protoo] unmarshal error: %v", err)
			continue
		}

		if msg.Response {
			c.mu.Lock()
			ch, ok := c.pending[msg.ID]
			c.mu.Unlock()
			if ok {
				ch <- protooResponse{
					Response:    true,
					ID:          msg.ID,
					OK:          msg.OK,
					Data:        msg.Data,
					ErrorCode:   msg.ErrorCode,
					ErrorReason: msg.ErrorReason,
				}
			}
		} else if msg.Notification {
			if c.OnNotification != nil {
				c.OnNotification(msg.Method, msg.Data)
			}
		} else if msg.Request {
			// Server-side requests (e.g. "newConsumer").
			if c.OnRequest != nil {
				go c.handleServerRequest(msg.ID, msg.Method, msg.Data)
			} else {
				c.acceptServerRequest(msg.ID, nil)
			}
		}
	}
}

// handleServerRequest dispatches to OnRequest and sends the response.
func (c *ProtooClient) handleServerRequest(id uint64, method string, data json.RawMessage) {
	respData, err := c.OnRequest(method, data)
	if err != nil {
		c.rejectServerRequest(id, 500, err.Error())
		return
	}
	c.acceptServerRequest(id, respData)
}

// acceptServerRequest sends an accept response for server-initiated requests.
func (c *ProtooClient) acceptServerRequest(id uint64, data json.RawMessage) {
	resp := map[string]interface{}{
		"response": true,
		"id":       id,
		"ok":       true,
	}
	if data != nil {
		resp["data"] = data
	}
	if err := c.conn.WriteJSON(resp); err != nil {
		log.Printf("[protoo] failed to accept server request %d: %v", id, err)
	}
}

// rejectServerRequest sends a reject response for server-initiated requests.
func (c *ProtooClient) rejectServerRequest(id uint64, code int, reason string) {
	resp := map[string]interface{}{
		"response":    true,
		"id":          id,
		"ok":          false,
		"errorCode":   code,
		"errorReason": reason,
	}
	if err := c.conn.WriteJSON(resp); err != nil {
		log.Printf("[protoo] failed to reject server request %d: %v", id, err)
	}
}
