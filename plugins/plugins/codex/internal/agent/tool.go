// Package agent holds the contract the server's plugin host expects of a tool.
//
// It is a copy of the shape rather than an import: this plugin is a separate
// program, and the point of moving it out of the server was that it should not
// have to be built together with it.
package agent

import (
	"context"
	"encoding/json"
)

// Tool is anything the model can call.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Execute(params json.RawMessage) (string, error)
	ConfirmationRequired() bool
	ThinkingSound() bool
}

// SessionTool is a tool whose work belongs to one conversation rather than to
// the process — a developer task pinned to an isolated worktree, say.
type SessionTool interface {
	Tool
	ExecuteInSession(ctx context.Context, sessionID string, params json.RawMessage) (string, error)
}

// ConfirmationPrompter lets a tool describe a pending action in the words the
// agent should say out loud before doing it.
type ConfirmationPrompter interface {
	ConfirmationPrompt(params json.RawMessage) string
}
