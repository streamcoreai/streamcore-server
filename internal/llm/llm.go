package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/streamcoreai/server/internal/config"
)

// ToolDefinition describes a function tool available to the LLM.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolCall represents the LLM requesting a tool invocation.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolResult feeds the output of a tool call back to the LLM.
type ToolResult struct {
	CallID string
	Output string
}

// Client is the interface that all LLM providers must implement.
type Client interface {
	Chat(ctx context.Context, userText string, onChunk func(string), onSentence func(string)) (string, error)
	SetTools(tools []ToolDefinition)
	SetToolHandler(handler func(ctx context.Context, call ToolCall) (string, error))
	AppendSystemPrompt(text string)
	Reset()
}

// NewClient returns an LLM client for the configured provider.
func NewClient(cfg *config.Config) (Client, error) {
	switch cfg.LLM.Provider {
	case "openai":
		if cfg.OpenAI.APIKey == "" {
			return nil, fmt.Errorf("llm provider %q requires [openai] api_key to be set", cfg.LLM.Provider)
		}
		return NewOpenAIClient(cfg.OpenAI.APIKey, cfg.OpenAI.Model, cfg.OpenAI.SystemPrompt), nil
	default:
		return nil, fmt.Errorf("unknown llm provider %q (supported: openai)", cfg.LLM.Provider)
	}
}
