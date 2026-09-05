package plugin

import (
	"context"
	"encoding/json"
)

// Tool is the interface for anything callable by the LLM via function calling.
// Both external (subprocess) plugins and native Go plugins implement this.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Execute(params json.RawMessage) (string, error)
	ConfirmationRequired() bool
	ThinkingSound() bool
}

// SessionTool is an optional capability for tools whose work belongs to one
// conversation rather than the process — a developer task pinned to an isolated
// worktree, say. The pipeline calls ExecuteInSession when a tool implements it
// and falls back to Execute otherwise, so nothing existing has to change.
type SessionTool interface {
	Tool
	ExecuteInSession(ctx context.Context, sessionID string, params json.RawMessage) (string, error)
}

// PluginEvent is a server lifecycle event. Unlike Tool, which is invoked by
// the LLM as part of a response, EventHandler is invoked by StreamCore when a
// lifecycle transition occurs.
type PluginEvent struct {
	Type      string
	SessionID string
	TurnID    string
	TurnSeq   uint64
	Data      any
}

// OutboundEvent is a plugin-produced event payload. Payload is the value that
// should be marshalled and sent on the session's existing events DataChannel;
// OutboundEvent itself is deliberately not a wire envelope.
type OutboundEvent struct {
	Type    string
	TurnID  string
	TurnSeq uint64
	Payload any
}

// EventHandler is an optional plugin capability. Existing Tool implementations
// do not have to implement it, and event-only plugins do not have to be exposed
// to the LLM as tools.
type EventHandler interface {
	Events() []string
	HandleEvent(context.Context, PluginEvent) ([]OutboundEvent, error)
}

// AssistantResponseCompleted is the payload for AssistantResponseCompletedEvent.
// It is emitted once for a settled, non-interrupted logical assistant turn.
type AssistantResponseCompleted struct {
	Transcript  string `json:"transcript"`
	Response    string `json:"response"`
	Interrupted bool   `json:"interrupted"`
}

// AssistantResponseCompletedEvent is subscribed to by plugins that need the
// final result of a voice turn, such as persistent-display projection.
const AssistantResponseCompletedEvent = "assistant.response.completed"

// Skill is a parsed markdown skill file that augments the system prompt.
type Skill struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Version     int      `yaml:"version"`
	Triggers    []string `yaml:"triggers"`
	Plugins     []string `yaml:"plugins"`
	Content     string   // The markdown content after frontmatter
}

// JSONRPCRequest is a request sent to a plugin subprocess.
//
// Tool and SessionID sit alongside Params rather than inside it. A plugin
// written against the original single-tool protocol reads only method, params
// and id, so it receives exactly the arguments it always did and never sees
// the routing fields it has no use for.
type JSONRPCRequest struct {
	JSONRPC   string          `json:"jsonrpc"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	ID        int64           `json:"id"`
}

// JSONRPCResponse is a plugin's reply. Result is raw so a plugin may answer
// with either a plain string, as the SDKs have always done, or a Result
// envelope carrying packets for the device.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
	ID      int64           `json:"id"`
}

// JSONRPCError represents a JSON-RPC error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// inboundMessage is something the plugin initiated. Its id is left raw because
// a plugin numbers its own requests however it likes, and an absent id means a
// notification that wants no reply.
type inboundMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
}

// inboundReply answers an inboundMessage that carried an id.
type inboundReply struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  any             `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}
