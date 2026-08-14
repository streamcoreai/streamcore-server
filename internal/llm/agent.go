package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// agentClient forwards each user turn to an external "bring your own agent"
// HTTP endpoint and streams the reply back into the voice pipeline. The
// external service owns the conversation — memory, prompting, and tool use —
// keyed by the session_id sent with every request; the server contributes
// only speech-to-text in and text-to-speech out.
//
// Request (POST <url>, application/json):
//
//	{
//	  "session_id":  "9f2c…",  // stable for the conversation; rotates on Reset
//	  "resource_id": "…",      // who is on the call; omitted when unknown
//	  "type":        "chat",   // "chat" for a user turn, "oneshot" for a stateless transform
//	  "text":        "…",      // the transcribed user turn (or the oneshot user text)
//	  "system":      "…"       // skill text appended by the server; oneshot system prompt
//	}
//
// session_id scopes one conversation, resource_id the person behind it, so an
// agent can remember a caller between calls. It comes from a verified JWT claim
// or a trusted server-side caller, and survives a resume unchanged.
//
// Accepted responses, by Content-Type:
//
//	text/event-stream   "data:" lines, each either raw text or JSON
//	                    {"delta": "…"}; "[DONE]" ends the stream early
//	text/plain          chunked text, spoken as it arrives
//	application/json    {"text": "…"} (or {"response": "…"}), buffered
type agentClient struct {
	url    string
	apiKey string
	http   *http.Client

	// Fixed for the life of the client. Unlike sessionID, a Reset leaves it
	// alone: the conversation starts over, the caller has not changed.
	resourceID string

	mu          sync.Mutex
	sessionID   string
	extraSystem string // accumulated skill text, forwarded on every turn
}

// NewAgentClient returns a Client that proxies turns to an external agent
// endpoint. timeoutMs bounds a whole turn including streaming the reply;
// zero means 60s. An empty resourceID is omitted from every request.
func NewAgentClient(url, apiKey string, timeoutMs int, resourceID string) Client {
	if timeoutMs <= 0 {
		timeoutMs = 60000
	}
	// Keep a warm connection to the agent — a cold TLS handshake on the
	// first turn is user-audible latency.
	transport := &http.Transport{
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	}
	return &agentClient{
		url:        url,
		apiKey:     apiKey,
		http:       &http.Client{Transport: transport, Timeout: time.Duration(timeoutMs) * time.Millisecond},
		sessionID:  newAgentSessionID(),
		resourceID: resourceID,
	}
}

func newAgentSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type agentRequest struct {
	SessionID  string `json:"session_id"`
	ResourceID string `json:"resource_id,omitempty"`
	Type       string `json:"type"`
	Text       string `json:"text"`
	System     string `json:"system,omitempty"`
}

func (c *agentClient) Chat(ctx context.Context, userText string, onChunk func(string), onSentence func(string)) (string, error) {
	c.mu.Lock()
	req := agentRequest{
		SessionID:  c.sessionID,
		ResourceID: c.resourceID,
		Type:       "chat",
		Text:       userText,
		System:     c.extraSystem,
	}
	c.mu.Unlock()

	result, err := c.do(ctx, req, onChunk, onSentence)
	if err != nil {
		return result, err
	}
	log.Printf("[llm] agent response: %s", truncate(result, 80))
	return result, nil
}

func (c *agentClient) OneShot(ctx context.Context, system, user string) (string, error) {
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()

	out, err := c.do(ctx, agentRequest{
		SessionID:  sessionID,
		ResourceID: c.resourceID,
		Type:       "oneshot",
		Text:       user,
		System:     system,
	}, nil, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// do posts one request and streams the reply through the chunk/sentence
// callbacks, returning the full concatenated text.
func (c *agentClient) do(ctx context.Context, payload agentRequest, onChunk func(string), onSentence func(string)) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("agent request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("agent request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream, text/plain, application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("agent endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("agent endpoint returned %d: %s", resp.StatusCode, truncate(string(snippet), 200))
	}

	var fullResponse strings.Builder
	var sentenceBuf strings.Builder
	emit := func(chunk string) {
		if chunk == "" {
			return
		}
		fullResponse.WriteString(chunk)
		sentenceBuf.WriteString(chunk)
		if onChunk != nil {
			onChunk(chunk)
		}
		if onSentence != nil {
			text := sentenceBuf.String()
			if idx := findSentenceEnd(text); idx >= 0 {
				if sentence := strings.TrimSpace(text[:idx+1]); sentence != "" {
					onSentence(sentence)
				}
				sentenceBuf.Reset()
				sentenceBuf.WriteString(text[idx+1:])
			}
		}
	}

	contentType := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(contentType, "text/event-stream"):
		err = readSSE(resp.Body, emit)
	case strings.HasPrefix(contentType, "application/json"):
		err = readJSONReply(resp.Body, emit)
	default:
		err = readTextStream(resp.Body, emit)
	}
	if err != nil {
		return fullResponse.String(), fmt.Errorf("agent reply: %w", err)
	}

	if onSentence != nil {
		if remaining := strings.TrimSpace(sentenceBuf.String()); remaining != "" {
			onSentence(remaining)
		}
	}
	return fullResponse.String(), nil
}

// readSSE emits the payload of each "data:" line. A line is treated as JSON
// {"delta": "…"} when it parses as an object; anything else is raw text, so
// trivial agents can just print data lines without JSON-encoding.
func readSSE(r io.Reader, emit func(string)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // event:/id:/retry:/comments/blank separators
		}
		data = strings.TrimPrefix(data, " ")
		if data == "[DONE]" {
			return nil
		}
		if strings.HasPrefix(data, "{") {
			var event struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &event) == nil {
				emit(event.Delta)
				continue
			}
		}
		emit(data)
	}
	return scanner.Err()
}

// readTextStream emits raw bytes as they arrive, holding back any trailing
// partial UTF-8 rune so multi-byte characters split across reads stay intact.
func readTextStream(r io.Reader, emit func(string)) error {
	buf := make([]byte, 4096)
	var pending []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			valid := len(pending)
			for valid > 0 && !utf8.Valid(pending[:valid]) {
				valid--
			}
			emit(string(pending[:valid]))
			pending = pending[valid:]
		}
		if err == io.EOF {
			if len(pending) > 0 {
				emit(string(pending))
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func readJSONReply(r io.Reader, emit func(string)) error {
	body, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return err
	}
	var reply struct {
		Text     string `json:"text"`
		Response string `json:"response"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return fmt.Errorf("parse json reply: %w", err)
	}
	if reply.Text != "" {
		emit(reply.Text)
	} else {
		emit(reply.Response)
	}
	return nil
}

// SetTools is a no-op: the external agent owns its own tools. Server-side
// plugins and skills are not forwarded in agent mode.
func (c *agentClient) SetTools(tools []ToolDefinition) {
	if len(tools) > 0 {
		log.Printf("[llm] agent provider ignores %d server-side tools (the agent owns tool use)", len(tools))
	}
}

func (c *agentClient) SetToolHandler(handler func(ctx context.Context, call ToolCall) (string, error)) {
}

func (c *agentClient) AppendSystemPrompt(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.extraSystem += text
}

// Reset rotates the session ID so the agent starts a fresh conversation.
func (c *agentClient) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = newAgentSessionID()
}
