package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
)

// SessionSink is one live conversation, as far as a plugin is concerned. The
// pipeline binds itself for the life of a session so a plugin can reach the
// device and the model without holding a reference to either.
type SessionSink interface {
	Emit(ctx context.Context, emission Emission) error
	Complete(ctx context.Context, req CompletionRequest) (string, error)

	// Capture obtains something only the client can supply, such as a frame
	// from its camera, and returns the fields to merge into a tool's arguments.
	Capture(ctx context.Context, capability string) (json.RawMessage, error)

	// Search queries the deployment's knowledge base.
	Search(ctx context.Context, query string, limit int) ([]string, error)
}

// BindSession registers a live conversation under its session id.
func (m *Manager) BindSession(sessionID string, sink SessionSink) {
	if sessionID == "" || sink == nil {
		return
	}
	m.sinkMu.Lock()
	defer m.sinkMu.Unlock()
	m.sinks[sessionID] = sink
}

// UnbindSession drops a conversation that has ended. A plugin that emits to it
// afterwards gets a clear error rather than writing into a closed channel.
func (m *Manager) UnbindSession(sessionID string) {
	m.sinkMu.Lock()
	defer m.sinkMu.Unlock()
	delete(m.sinks, sessionID)
}

func (m *Manager) sink(sessionID string) (SessionSink, error) {
	m.sinkMu.RLock()
	defer m.sinkMu.RUnlock()
	sink, ok := m.sinks[sessionID]
	if !ok {
		return nil, fmt.Errorf("no live session %q", sessionID)
	}
	return sink, nil
}

// SetPluginSettings installs the per-plugin tables from config.toml. Call it
// before LoadAll; each plugin receives its own table at initialize.
func (m *Manager) SetPluginSettings(settings map[string]map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, table := range settings {
		encoded, err := json.Marshal(table)
		if err != nil {
			log.Printf("[plugins] settings for %s are not encodable: %v", name, err)
			continue
		}
		m.settings[name] = encoded
	}
}

// CallTool runs a registered tool on behalf of something that is not the model
// — another plugin, most often, reaching a capability it should not have to
// reimplement.
//
// A tool behind the confirmation gate is refused. The gate exists to put a
// spoken question to a user, and there is nobody to ask on this path; letting
// it through would turn a deliberate checkpoint into a formality.
func (m *Manager) CallTool(ctx context.Context, sessionID, name string, args json.RawMessage) (string, error) {
	tool, ok := m.lookup(name)
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	if tool.ConfirmationRequired() {
		return "", fmt.Errorf("tool %s requires spoken confirmation and cannot be called this way", name)
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if scoped, ok := tool.(SessionTool); ok {
		return scoped.ExecuteInSession(ctx, sessionID, args)
	}
	return tool.Execute(args)
}

// callbacks is what every hosted plugin gets: the three things that previously
// required being compiled into the server.
func (m *Manager) callbacks() Callbacks {
	return Callbacks{
		Emit: func(ctx context.Context, sessionID string, emission Emission) error {
			sink, err := m.sink(sessionID)
			if err != nil {
				return err
			}
			return sink.Emit(ctx, emission)
		},
		CallTool: m.CallTool,
		Search: func(ctx context.Context, sessionID, query string, limit int) ([]string, error) {
			sink, err := m.sink(sessionID)
			if err != nil {
				return nil, err
			}
			return sink.Search(ctx, query, limit)
		},
		Capture: func(ctx context.Context, sessionID, capability string) (json.RawMessage, error) {
			sink, err := m.sink(sessionID)
			if err != nil {
				return nil, err
			}
			return sink.Capture(ctx, capability)
		},
		Complete: func(ctx context.Context, sessionID string, req CompletionRequest) (string, error) {
			sink, err := m.sink(sessionID)
			if err != nil {
				return "", err
			}
			return sink.Complete(ctx, req)
		},
	}
}
