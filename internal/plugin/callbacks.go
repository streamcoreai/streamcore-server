package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// ProtocolVersion is what the server announces at initialize. A plugin can use
// it to decide whether the newer verbs are available; one that ignores it
// behaves exactly as it did before they existed.
const ProtocolVersion = 2

// Callbacks are what a plugin may ask the server to do. They are the reason a
// plugin no longer has to be compiled in: pushing a packet at a device, calling
// another plugin, and asking the configured model a question were the three
// things only Go code could reach.
type Callbacks struct {
	// Capture asks the conversation's client for something only it has — a
	// camera frame, most often. Named capabilities rather than named tools, so
	// the next plugin that needs a picture works without the server changing.
	Capture  func(ctx context.Context, sessionID, capability string) (json.RawMessage, error)
	Emit     func(ctx context.Context, sessionID string, emission Emission) error
	CallTool func(ctx context.Context, sessionID, name string, args json.RawMessage) (string, error)
	Complete func(ctx context.Context, sessionID string, req CompletionRequest) (string, error)

	// Search queries the deployment's knowledge base, so a plugin can ground
	// an answer in the same corpus the agent uses rather than standing up a
	// second retrieval stack beside it.
	Search func(ctx context.Context, sessionID, query string, limit int) ([]string, error)
}

// CompletionRequest asks the server's own model for a completion, so a plugin
// that needs one does not have to carry a second API key and a second provider
// choice that can drift from the server's.
type CompletionRequest struct {
	System string `json:"system,omitempty"`
	Prompt string `json:"prompt"`
}

// initializeParams is the handshake the server sends. Config is the plugin's
// own table from config.toml, which keeps a plugin's settings in the operator's
// one config file instead of a per-plugin dotenv.
type initializeParams struct {
	Plugin   string          `json:"plugin"`
	Protocol int             `json:"protocol"`
	Config   json.RawMessage `json:"config,omitempty"`
}

// readyParams tells a plugin what the server finished with, so it can decide
// what to offer knowing which peers exist.
type readyParams struct {
	Tools []string `json:"tools"`
}

// eventParams delivers a lifecycle event to a subscribing plugin.
type eventParams struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	TurnSeq   uint64 `json:"turn_seq,omitempty"`
	Data      any    `json:"data,omitempty"`
}

// handleInbound services one plugin-initiated message. A message carrying an id
// gets a reply; one without is a notification and is answered with silence.
func (p *ExternalPlugin) handleInbound(message []byte) {
	var req inboundMessage
	if err := json.Unmarshal(message, &req); err != nil {
		log.Printf("[plugin:%s] unparseable request: %s", p.manifest.Name, message)
		return
	}

	result, err := p.serve(context.Background(), req)
	if len(req.ID) == 0 {
		if err != nil {
			log.Printf("[plugin:%s] %s: %v", p.manifest.Name, req.Method, err)
		}
		return
	}

	reply := inboundReply{JSONRPC: "2.0", ID: req.ID}
	if err != nil {
		reply.Error = &JSONRPCError{Code: -32000, Message: err.Error()}
	} else {
		reply.Result = result
	}
	if writeErr := p.write(reply); writeErr != nil {
		log.Printf("[plugin:%s] reply failed: %v", p.manifest.Name, writeErr)
	}
}

func (p *ExternalPlugin) serve(ctx context.Context, req inboundMessage) (any, error) {
	switch req.Method {
	case "emit":
		return p.serveEmit(ctx, req.Params)
	case "tools/call":
		return p.serveToolCall(ctx, req.Params)
	case "llm/complete":
		return p.serveComplete(ctx, req.Params)
	case "rag/search":
		return p.serveSearch(ctx, req.Params)
	default:
		return nil, fmt.Errorf("unknown method %q", req.Method)
	}
}

func (p *ExternalPlugin) serveEmit(ctx context.Context, raw json.RawMessage) (any, error) {
	if p.callbacks.Emit == nil {
		return nil, fmt.Errorf("emit %w", errNoCallback)
	}
	var emission Emission
	if err := json.Unmarshal(raw, &emission); err != nil {
		return nil, fmt.Errorf("emit: %w", err)
	}
	if emission.Topic == "" {
		return nil, fmt.Errorf("emit: topic is required")
	}
	// An unsolicited emission has no call to inherit a session from, so the
	// plugin has to name the conversation it means.
	if emission.SessionID == "" {
		return nil, fmt.Errorf("emit: session_id is required")
	}
	if err := p.callbacks.Emit(ctx, emission.SessionID, emission); err != nil {
		return nil, err
	}
	return "ok", nil
}

func (p *ExternalPlugin) serveToolCall(ctx context.Context, raw json.RawMessage) (any, error) {
	if p.callbacks.CallTool == nil {
		return nil, fmt.Errorf("tools/call %w", errNoCallback)
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
		SessionID string          `json:"session_id,omitempty"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("tools/call: %w", err)
	}
	if params.Name == "" {
		return nil, fmt.Errorf("tools/call: name is required")
	}
	// A plugin cannot reach itself this way. One that wants its own tool can
	// call the function directly, and allowing it would turn a mistake into a
	// deadlock against its own stdio loop.
	if p.ownsTool(params.Name) {
		return nil, fmt.Errorf("tools/call: %q belongs to this plugin", params.Name)
	}
	return p.callbacks.CallTool(ctx, params.SessionID, params.Name, params.Arguments)
}

func (p *ExternalPlugin) serveComplete(ctx context.Context, raw json.RawMessage) (any, error) {
	if p.callbacks.Complete == nil {
		return nil, fmt.Errorf("llm/complete %w", errNoCallback)
	}
	var params struct {
		CompletionRequest
		SessionID string `json:"session_id,omitempty"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("llm/complete: %w", err)
	}
	if params.Prompt == "" {
		return nil, fmt.Errorf("llm/complete: prompt is required")
	}
	return p.callbacks.Complete(ctx, params.SessionID, params.CompletionRequest)
}

func (p *ExternalPlugin) serveSearch(ctx context.Context, raw json.RawMessage) (any, error) {
	if p.callbacks.Search == nil {
		return nil, fmt.Errorf("rag/search %w", errNoCallback)
	}
	var params struct {
		Query     string `json:"query"`
		Limit     int    `json:"limit,omitempty"`
		SessionID string `json:"session_id,omitempty"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("rag/search: %w", err)
	}
	if strings.TrimSpace(params.Query) == "" {
		return nil, fmt.Errorf("rag/search: query is required")
	}
	return p.callbacks.Search(ctx, params.SessionID, params.Query, params.Limit)
}

func (p *ExternalPlugin) ownsTool(name string) bool {
	for _, tool := range p.ToolSpecs() {
		if tool.Name == name {
			return true
		}
	}
	return false
}
