package pipeline

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	visionToolName = "vision.analyze"
)

type dcImageStart struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	TotalSize int    `json:"total_size"`
	Mime      string `json:"mime"`
}

type dcImageChunk struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Index int    `json:"index"` // 0-based chunk index
	Data  string `json:"data"`  // base64-encoded chunk
}

type dcImageEnd struct {
	Type string `json:"type"` // "image_end"
	ID   string `json:"id"`
}

type dcRequestImage struct {
	Type string `json:"type"`
}

type imageReceiver struct {
	mu       sync.Mutex
	pending  map[string]*imageAssembly
	resultCh chan imageResult
}

type imageAssembly struct {
	id        string
	mime      string
	totalSize int
	chunks    map[int]string
	started   time.Time
}

type imageResult struct {
	Base64 string // concatenated base64 data
	Mime   string
	Err    error
}

func newImageReceiver() *imageReceiver {
	return &imageReceiver{
		pending:  make(map[string]*imageAssembly),
		resultCh: make(chan imageResult, 1),
	}
}

// handleMessage processes a single data-channel JSON message. Returns true
// if the message was an image-protocol message (and was consumed).
func (r *imageReceiver) handleMessage(raw string) bool {
	// Quick sniff: only attempt parsing if it looks like an image message.
	if !strings.Contains(raw, `"image_`) {
		return false
	}

	var base struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(raw), &base); err != nil {
		return false
	}

	switch base.Type {
	case "image_start":
		var msg dcImageStart
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			log.Printf("[vision] bad image_start: %v", err)
			return true
		}
		r.mu.Lock()
		r.pending[msg.ID] = &imageAssembly{
			id:        msg.ID,
			mime:      msg.Mime,
			totalSize: msg.TotalSize,
			chunks:    make(map[int]string),
			started:   time.Now(),
		}
		r.mu.Unlock()
		log.Printf("[vision] image_start id=%s total_size=%d mime=%s", msg.ID, msg.TotalSize, msg.Mime)
		return true

	case "image_chunk":
		var msg dcImageChunk
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			log.Printf("[vision] bad image_chunk: %v", err)
			return true
		}
		r.mu.Lock()
		asm, ok := r.pending[msg.ID]
		if ok {
			asm.chunks[msg.Index] = msg.Data
		}
		r.mu.Unlock()
		if !ok {
			log.Printf("[vision] image_chunk for unknown id=%s", msg.ID)
		}
		return true

	case "image_end":
		var msg dcImageEnd
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			log.Printf("[vision] bad image_end: %v", err)
			return true
		}
		r.mu.Lock()
		asm, ok := r.pending[msg.ID]
		delete(r.pending, msg.ID)
		r.mu.Unlock()

		if !ok {
			log.Printf("[vision] image_end for unknown id=%s", msg.ID)
			return true
		}

		var b strings.Builder
		for i := 0; i < len(asm.chunks); i++ {
			chunk, exists := asm.chunks[i]
			if !exists {
				r.deliver(imageResult{Err: fmt.Errorf("missing chunk %d for image %s", i, msg.ID)})
				return true
			}
			b.WriteString(chunk)
		}

		log.Printf("[vision] image complete id=%s chunks=%d base64_len=%d elapsed=%v",
			msg.ID, len(asm.chunks), b.Len(), time.Since(asm.started).Round(time.Millisecond))
		r.deliver(imageResult{Base64: b.String(), Mime: asm.mime})
		return true

	default:
		return false
	}
}

func (r *imageReceiver) deliver(res imageResult) {
	select {
	case r.resultCh <- res:
	default:
		// Drain stale result and replace.
		select {
		case <-r.resultCh:
		default:
		}
		r.resultCh <- res
	}
}

func (r *imageReceiver) requestAndWait(sendEvent func(interface{}) error) (imageResult, error) {
	const maxRetries = 3
	const retryInterval = 3 * time.Second

	select {
	case <-r.resultCh:
	default:
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := sendEvent(dcRequestImage{Type: "request_image"}); err != nil {
			return imageResult{}, fmt.Errorf("send request_image: %w", err)
		}
		log.Printf("[vision] sent request_image to client (attempt %d/%d)", attempt, maxRetries)

		select {
		case res := <-r.resultCh:
			return res, res.Err
		case <-time.After(retryInterval):
			log.Printf("[vision] no image response after %v, retrying...", retryInterval)
		}
	}

	r.mu.Lock()
	for k := range r.pending {
		delete(r.pending, k)
	}
	r.mu.Unlock()
	return imageResult{}, fmt.Errorf("image capture failed after %d attempts (no response from device)", maxRetries)
}
