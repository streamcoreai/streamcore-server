package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"strings"
	"sync"

	"github.com/ollama/ollama/api"
)

type ollamaClient struct {
	client       *api.Client
	model        string
	systemPrompt string
	mu           sync.Mutex
	history      []api.Message
	tools        []api.Tool
	toolNameMap  map[string]string // sanitized name → original name
	toolHandler  func(ctx context.Context, call ToolCall) (string, error)
}

func NewOllamaClient(baseURL, model, systemPrompt string) (Client, error) {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "llama3.2"
	}

	// Create client with custom base URL if provided
	var client *api.Client
	var err error
	if baseURL != "" && baseURL != "http://localhost:11434" {
		parsedURL, err := url.Parse(baseURL)
		if err != nil {
			return nil, fmt.Errorf("parse ollama base URL: %w", err)
		}
		client = api.NewClient(parsedURL, nil)
	} else {
		client, err = api.ClientFromEnvironment()
		if err != nil {
			return nil, fmt.Errorf("create ollama client: %w", err)
		}
	}

	return &ollamaClient{
		client:       client,
		model:        model,
		systemPrompt: systemPrompt,
		history: []api.Message{
			{Role: "system", Content: systemPrompt},
		},
	}, nil
}

func (c *ollamaClient) SetTools(tools []ToolDefinition) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.tools = make([]api.Tool, 0, len(tools))
	c.toolNameMap = make(map[string]string, len(tools))
	for _, t := range tools {
		var params map[string]any
		if t.Parameters != nil {
			json.Unmarshal(t.Parameters, &params)
		}
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}

		// Unmarshal into ToolFunctionParameters
		var tfp api.ToolFunctionParameters
		paramsJSON, _ := json.Marshal(params)
		json.Unmarshal(paramsJSON, &tfp)

		// Sanitize tool names — dots confuse model tool-call templates.
		sanitized := sanitizeToolName(t.Name)
		c.toolNameMap[sanitized] = t.Name

		c.tools = append(c.tools, api.Tool{
			Type: "function",
			Function: api.ToolFunction{
				Name:        sanitized,
				Description: t.Description,
				Parameters:  tfp,
			},
		})
	}
	log.Printf("[llm] configured %d tools for ollama", len(c.tools))
}

func (c *ollamaClient) SetToolHandler(handler func(ctx context.Context, call ToolCall) (string, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolHandler = handler
}

func (c *ollamaClient) AppendSystemPrompt(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if text == "" {
		return
	}

	combined := c.systemPrompt + text
	if len(c.history) > 0 && c.history[0].Role == "system" {
		c.history[0].Content = combined
	}
}

func (c *ollamaClient) Chat(ctx context.Context, userText string, onChunk func(string), onSentence func(string)) (string, error) {
	c.mu.Lock()
	c.history = append(c.history, api.Message{
		Role:    "user",
		Content: userText,
	})
	c.mu.Unlock()

	const maxToolRounds = 10
	for round := 0; ; round++ {
		result, toolCalls, err := c.streamCompletion(ctx, onChunk, onSentence)
		if err != nil {
			return result, err
		}

		if len(toolCalls) == 0 {
			return result, nil
		}

		if round >= maxToolRounds {
			log.Printf("[llm] exceeded %d tool-call rounds, stopping", maxToolRounds)
			return result, nil
		}

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
			c.history = append(c.history, api.Message{
				Role:    "tool",
				Content: output,
			})
			c.mu.Unlock()
		}
	}
}

func (c *ollamaClient) streamCompletion(ctx context.Context, onChunk func(string), onSentence func(string)) (string, []ToolCall, error) {
	c.mu.Lock()
	messages := make([]api.Message, len(c.history))
	copy(messages, c.history)
	tools := c.tools
	c.mu.Unlock()

	// Disable streaming when tools are present — some Ollama models
	// (e.g. gemma4) don't reliably return tool calls in streaming mode.
	streaming := len(tools) == 0
	req := &api.ChatRequest{
		Model:    c.model,
		Messages: messages,
		Stream:   func(b bool) *bool { return &b }(streaming),
	}
	if len(tools) > 0 {
		req.Tools = tools
	}

	var fullResponse strings.Builder
	var sentenceBuf strings.Builder
	var toolCalls []ToolCall

	err := c.client.Chat(ctx, req, func(resp api.ChatResponse) error {
		// Handle tool calls
		if len(resp.Message.ToolCalls) > 0 {
			for _, tc := range resp.Message.ToolCalls {
				args, _ := json.Marshal(tc.Function.Arguments)
				// Map sanitized name back to the original plugin name.
				name := tc.Function.Name
				if orig, ok := c.toolNameMap[name]; ok {
					name = orig
				}
				toolCalls = append(toolCalls, ToolCall{
					ID:        fmt.Sprintf("call_%d", len(toolCalls)),
					Name:      name,
					Arguments: args,
				})
			}
			return nil
		}

		// Handle text content
		chunk := resp.Message.Content
		if chunk == "" {
			return nil
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

		return nil
	})

	if err != nil && err != io.EOF {
		return fullResponse.String(), nil, fmt.Errorf("ollama chat: %w", err)
	}

	// Flush remaining text
	if onSentence != nil {
		remaining := strings.TrimSpace(sentenceBuf.String())
		if remaining != "" {
			onSentence(remaining)
		}
	}

	// Handle tool calls
	if len(toolCalls) > 0 {
		assistantMsg := api.Message{
			Role: "assistant",
		}
		for _, tc := range toolCalls {
			var tcfa api.ToolCallFunctionArguments
			json.Unmarshal(tc.Arguments, &tcfa)
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, api.ToolCall{
				Function: api.ToolCallFunction{
					Name:      tc.Name,
					Arguments: tcfa,
				},
			})
		}

		c.mu.Lock()
		c.history = append(c.history, assistantMsg)
		c.mu.Unlock()

		return fullResponse.String(), toolCalls, nil
	}

	// Text response — add to history
	c.mu.Lock()
	c.history = append(c.history, api.Message{
		Role:    "assistant",
		Content: fullResponse.String(),
	})
	if len(c.history) > 31 {
		kept := []api.Message{c.history[0]}
		kept = append(kept, c.history[len(c.history)-30:]...)
		c.history = kept
	}
	c.mu.Unlock()

	log.Printf("[llm] response: %s", truncate(fullResponse.String(), 80))
	return fullResponse.String(), nil, nil
}

func (c *ollamaClient) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = []api.Message{
		{Role: "system", Content: c.systemPrompt},
	}
}
