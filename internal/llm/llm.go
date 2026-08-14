package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/streamcoreai/streamcore-server/internal/config"
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

// Turn is one caller utterance plus whatever context the pipeline gathered for
// it.
//
// The split exists because providers need it in two shapes. A stateless model
// only reads one message, so its context has to be folded into that message —
// Prompt. An external agent keeps its own conversation and stores what it is
// sent, so folding context in would file "[Context: …] User: what's my balance"
// as the caller's words. It gets Text and the pieces separately.
type Turn struct {
	// Text is what the caller said. Never decorated.
	Text string

	// Prompt is Text with the context below folded in, for providers that carry
	// context in the message. Equal to Text when there is none.
	Prompt string

	// InterruptedText is what the agent was saying when the caller cut in.
	// Empty unless this turn follows a barge-in.
	InterruptedText string

	// Context holds retrieved RAG chunks.
	Context []string

	// Summary is the rolling digest of earlier turns, kept so facts from the
	// start of a long call survive past the model's history window.
	Summary string

	// Note is a per-turn instruction, currently the nudge to ask again when the
	// caller's speech arrived garbled. Instruction, not content.
	Note string
}

// Client is the interface that all LLM providers must implement.
type Client interface {
	Chat(ctx context.Context, turn Turn, onChunk func(string), onSentence func(string)) (string, error)
	// OneShot makes a single non-streaming call independent of conversation
	// state. Used for focused transformations such as summarisation.
	OneShot(ctx context.Context, system, user string) (string, error)
	SetTools(tools []ToolDefinition)
	SetToolHandler(handler func(ctx context.Context, call ToolCall) (string, error))
	AppendSystemPrompt(text string)
	Reset()
}

// NewClient returns an LLM client for the configured provider.
//
// resourceID is who is on the call, empty when nobody was identified. Only the
// "agent" provider uses it; the others keep the conversation in-process.
func NewClient(cfg *config.Config, resourceID string) (Client, error) {
	switch cfg.LLM.Provider {
	case "openai":
		if cfg.OpenAI.APIKey == "" {
			return nil, fmt.Errorf("llm provider %q requires [openai] api_key to be set", cfg.LLM.Provider)
		}
		return NewOpenAIClient(cfg.OpenAI.APIKey, cfg.OpenAI.Model, cfg.OpenAI.SystemPrompt, cfg.OpenAI.BaseURL), nil
	case "ollama":
		return NewOllamaClient(cfg.Ollama.BaseURL, cfg.Ollama.Model, cfg.Ollama.SystemPrompt)
	case "agent":
		if cfg.Agent.URL == "" {
			return nil, fmt.Errorf("llm provider %q requires [agent] url to be set", cfg.LLM.Provider)
		}
		return NewAgentClient(cfg.Agent.URL, cfg.Agent.APIKey, cfg.Agent.TimeoutMs, resourceID), nil
	default:
		return nil, fmt.Errorf("unknown llm provider %q (supported: openai, ollama, agent)", cfg.LLM.Provider)
	}
}
