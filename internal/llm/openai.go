package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

type openaiClient struct {
	client       *openai.Client
	model        string
	systemPrompt string
	mu           sync.Mutex
	history      []openai.ChatCompletionMessage
	tools        []openai.Tool
	toolHandler  func(ctx context.Context, call ToolCall) (string, error)
	toolNameMap  map[string]string // sanitized name → original name
}

func NewOpenAIClient(apiKey, model, systemPrompt string) Client {
	// Keep a warm connection pool so turns within a call (and the first turn
	// after an idle gap) reuse an established TLS+HTTP/2 connection instead of
	// paying a fresh handshake on the live audio path.
	transport := &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	}
	cfg := openai.DefaultConfig(apiKey)
	cfg.HTTPClient = &http.Client{Transport: transport, Timeout: 60 * time.Second}

	c := &openaiClient{
		client:       openai.NewClientWithConfig(cfg),
		model:        model,
		systemPrompt: systemPrompt,
		history: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		},
	}

	// Prime the connection pool in the background: ListModels is unbilled and
	// establishes the TLS+H2 connection the first real turn will reuse. Errors
	// are non-fatal (offline/dev) — this is best-effort warmup only.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := c.client.ListModels(ctx); err != nil {
			log.Printf("[llm] connection warmup skipped: %v", err)
		}
	}()

	return c
}

// SetTools configures the function-calling tools available to the LLM.
func (c *openaiClient) SetTools(tools []ToolDefinition) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.tools = make([]openai.Tool, 0, len(tools))
	c.toolNameMap = make(map[string]string, len(tools))
	for _, t := range tools {
		var params any
		if t.Parameters != nil {
			json.Unmarshal(t.Parameters, &params)
		}
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}

		// OpenAI requires tool names to match ^[a-zA-Z0-9_-]+$
		sanitized := sanitizeToolName(t.Name)
		c.toolNameMap[sanitized] = t.Name

		c.tools = append(c.tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        sanitized,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	log.Printf("[llm] configured %d tools", len(c.tools))
}

// SetToolHandler registers the callback for executing tool calls.
func (c *openaiClient) SetToolHandler(handler func(ctx context.Context, call ToolCall) (string, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolHandler = handler
}

// AppendSystemPrompt adds additional text to the system prompt (used for skills).
func (c *openaiClient) AppendSystemPrompt(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if text == "" {
		return
	}

	combined := c.systemPrompt + text
	if len(c.history) > 0 && c.history[0].Role == openai.ChatMessageRoleSystem {
		c.history[0].Content = combined
	}
}

// Chat sends a user message and streams back the assistant response.
// If the LLM returns tool calls, they are executed via the tool handler
// and the results are fed back for a follow-up response. This loop
// continues until the LLM produces a text response.
func (c *openaiClient) Chat(ctx context.Context, userText string, onChunk func(string), onSentence func(string)) (string, error) {
	c.mu.Lock()
	c.history = append(c.history, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userText,
	})
	c.mu.Unlock()

	// Tool-call loop: LLM may request tools multiple times before answering.
	const maxToolRounds = 10
	for round := 0; ; round++ {
		result, toolCalls, err := c.streamCompletion(ctx, onChunk, onSentence)
		if err != nil {
			return result, err
		}

		// No tool calls — we have a final text response.
		if len(toolCalls) == 0 {
			return result, nil
		}

		if round >= maxToolRounds {
			log.Printf("[llm] exceeded %d tool-call rounds, stopping", maxToolRounds)
			return result, nil
		}

		// Execute tool calls and feed results back.
		c.mu.Lock()
		handler := c.toolHandler
		c.mu.Unlock()

		if handler == nil {
			log.Printf("[llm] tool calls requested but no handler configured, skipping")
			return result, nil
		}

		for _, tc := range toolCalls {
			log.Printf("[llm] tool call: %s(%s)", tc.Name, truncate(string(tc.Arguments), 100))

			output, err := handler(ctx, tc)
			if err != nil {
				output = fmt.Sprintf("Error: %v", err)
			}

			log.Printf("[llm] tool result: %s", truncate(output, 100))

			c.mu.Lock()
			c.history = append(c.history, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    output,
				ToolCallID: tc.ID,
			})
			c.mu.Unlock()
		}
	}
}

// streamCompletion runs one streaming request and returns either a text response
// or a list of tool calls (never both in practice).
func (c *openaiClient) streamCompletion(ctx context.Context, onChunk func(string), onSentence func(string)) (string, []ToolCall, error) {
	c.mu.Lock()
	messages := make([]openai.ChatCompletionMessage, len(c.history))
	copy(messages, c.history)
	tools := c.tools
	c.mu.Unlock()

	req := openai.ChatCompletionRequest{
		Model:    c.model,
		Messages: messages,
		Stream:   true,
	}
	if len(tools) > 0 {
		req.Tools = tools
	}

	stream, err := c.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return "", nil, fmt.Errorf("create chat stream: %w", err)
	}
	defer stream.Close()

	var fullResponse strings.Builder
	var sentenceBuf strings.Builder

	// Accumulate tool calls across chunks.
	toolCallMap := make(map[int]*ToolCall)

	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fullResponse.String(), nil, fmt.Errorf("stream recv: %w", err)
		}

		if len(resp.Choices) == 0 {
			continue
		}

		delta := resp.Choices[0].Delta

		// Handle tool call deltas
		for _, tc := range delta.ToolCalls {
			idx := *tc.Index
			existing, ok := toolCallMap[idx]
			if !ok {
				existing = &ToolCall{}
				toolCallMap[idx] = existing
			}
			if tc.ID != "" {
				existing.ID = tc.ID
			}
			if tc.Function.Name != "" {
				existing.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				existing.Arguments = append(existing.Arguments, []byte(tc.Function.Arguments)...)
			}
		}

		// Handle text content
		chunk := delta.Content
		if chunk == "" {
			continue
		}

		fullResponse.WriteString(chunk)
		sentenceBuf.WriteString(chunk)

		if onChunk != nil {
			onChunk(chunk)
		}

		if onSentence != nil {
			text := sentenceBuf.String()
			if idx := findSentenceEnd(text); idx >= 0 {
				sentence := strings.TrimSpace(text[:idx+1])
				if sentence != "" {
					onSentence(sentence)
				}
				sentenceBuf.Reset()
				sentenceBuf.WriteString(text[idx+1:])
			}
		}
	}

	// Flush remaining text
	if onSentence != nil {
		remaining := strings.TrimSpace(sentenceBuf.String())
		if remaining != "" {
			onSentence(remaining)
		}
	}

	// Collect tool calls
	if len(toolCallMap) > 0 {
		var toolCalls []ToolCall

		// Build the assistant message with tool calls for history
		assistantMsg := openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant,
		}
		var oaiToolCalls []openai.ToolCall

		for _, tc := range toolCallMap {
			// Keep sanitized name for OpenAI history
			sanitizedName := tc.Name
			oaiToolCalls = append(oaiToolCalls, openai.ToolCall{
				ID:   tc.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      sanitizedName,
					Arguments: string(tc.Arguments),
				},
			})
			// Map sanitized name back to original plugin name for execution
			if orig, ok := c.toolNameMap[tc.Name]; ok {
				tc.Name = orig
			}
			toolCalls = append(toolCalls, *tc)
		}
		assistantMsg.ToolCalls = oaiToolCalls

		c.mu.Lock()
		c.history = append(c.history, assistantMsg)
		c.history = pruneHistory(c.history)
		c.mu.Unlock()

		return fullResponse.String(), toolCalls, nil
	}

	// Text response — add to history
	c.mu.Lock()
	c.history = append(c.history, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleAssistant,
		Content: fullResponse.String(),
	})
	c.history = pruneHistory(c.history)
	c.mu.Unlock()

	log.Printf("[llm] response: %s", truncate(fullResponse.String(), 80))
	return fullResponse.String(), nil, nil
}

// findSentenceEnd returns the index of the latest sentence-terminating
// punctuation in s, or -1 if none. '!' and '?' always terminate. A '.' only
// terminates when it is NOT inside an inline token — an email/domain
// ("gmail.com"), decimal ("39.99"), or abbreviation ("co.uk") — i.e. when the
// next character is not alphanumeric. A trailing '.' (last char so far) is
// ambiguous mid-stream ("gmail." in an address vs end of sentence), so the
// split is deferred until the next chunk disambiguates it; the end-of-stream
// flush emits any remainder.
//
// Without this, the splitter cut "name@gmail.com" at the internal dot, so TTS
// dropped the "dot" and read the address as "gmail com".
func findSentenceEnd(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		switch s[i] {
		case '!', '?':
			return i
		case '.':
			if i+1 >= len(s) {
				continue // trailing dot: ambiguous while streaming, wait for more
			}
			if isAlphaNumByte(s[i+1]) {
				continue // part of an email/domain/decimal token, not a boundary
			}
			return i
		}
	}
	return -1
}

func isAlphaNumByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// sanitizeToolName replaces characters not allowed by OpenAI's tool name
// pattern (^[a-zA-Z0-9_-]+$) with underscores.
func sanitizeToolName(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func (c *openaiClient) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: c.systemPrompt},
	}
}

// pruneHistory keeps the system prompt (history[0]) plus a rolling window
// of recent messages. Critically, when the window cuts mid-conversation
// we MUST NOT leave an orphan `tool` message at the start — OpenAI's API
// rejects a `tool` message that isn't a direct response to a preceding
// `tool_calls` assistant message.
//
// We also try to preserve assistant↔tool pairs by walking backward until
// every `tool` message at the head of the kept window has its matching
// `tool_calls` assistant message included.
//
// Window size: keep the last 30 messages after the system prompt (31
// total).
func pruneHistory(h []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	const window = 30
	if len(h) <= window+1 {
		return h
	}

	// Start with the last 30 messages.
	cut := len(h) - window
	// Move the cut earlier if the first kept message is a `tool` (orphan
	// without its preceding assistant `tool_calls`) — keep extending the
	// window backward until we hit a non-tool message, then drop those
	// orphan tools entirely so the kept window starts on a valid role.
	tail := h[cut:]
	for len(tail) > 0 && tail[0].Role == openai.ChatMessageRoleTool {
		tail = tail[1:]
	}

	kept := make([]openai.ChatCompletionMessage, 0, len(tail)+1)
	if len(h) > 0 && h[0].Role == openai.ChatMessageRoleSystem {
		kept = append(kept, h[0])
	}
	kept = append(kept, tail...)
	return kept
}
