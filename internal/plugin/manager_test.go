package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type onlyTool struct{}

func (onlyTool) Name() string                { return "legacy-tool" }
func (onlyTool) Description() string         { return "legacy tool" }
func (onlyTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (onlyTool) Execute(json.RawMessage) (string, error) {
	return "ok", nil
}
func (onlyTool) ConfirmationRequired() bool { return false }
func (onlyTool) ThinkingSound() bool        { return false }

type recordingHandler struct {
	name   string
	events []string
	calls  *[]PluginEvent
	err    error
	output []OutboundEvent
}

func (h *recordingHandler) Events() []string { return h.events }
func (h *recordingHandler) HandleEvent(_ context.Context, event PluginEvent) ([]OutboundEvent, error) {
	*h.calls = append(*h.calls, event)
	return h.output, h.err
}

func TestEventOnlyPluginIsNotExposedAsTool(t *testing.T) {
	manager := NewManager("")
	calls := make([]PluginEvent, 0, 1)
	manager.RegisterEventHandler("observer", &recordingHandler{
		events: []string{"test.event"},
		calls:  &calls,
		output: []OutboundEvent{{Type: "observed", Payload: map[string]string{"ok": "yes"}}},
	})

	if tools := manager.Tools(); len(tools) != 0 {
		t.Fatalf("event-only plugin was exposed to the LLM as %d tools", len(tools))
	}

	output, err := manager.DispatchEvent(context.Background(), PluginEvent{Type: "test.event"})
	if err != nil {
		t.Fatalf("DispatchEvent: %v", err)
	}
	if len(calls) != 1 || calls[0].Type != "test.event" {
		t.Fatalf("handler calls = %+v", calls)
	}
	if len(output) != 1 || output[0].Type != "observed" {
		t.Fatalf("output = %+v", output)
	}
}

func TestExistingToolPluginRemainsCompatible(t *testing.T) {
	manager := NewManager("")
	manager.RegisterTool(onlyTool{})

	tool, ok := manager.GetTool("legacy-tool")
	if !ok {
		t.Fatal("legacy tool disappeared from the tool registry")
	}
	result, err := tool.Execute(nil)
	if err != nil || result != "ok" {
		t.Fatalf("Execute = %q, %v", result, err)
	}
	if _, err := manager.DispatchEvent(context.Background(), PluginEvent{Type: "unrelated"}); err != nil {
		t.Fatalf("DispatchEvent with no handlers: %v", err)
	}
}

func TestDispatchEventContinuesAfterHandlerFailure(t *testing.T) {
	manager := NewManager("")
	calls := make([]PluginEvent, 0, 2)
	manager.RegisterEventHandler("bad", &recordingHandler{
		events: []string{"test.event"},
		calls:  &calls,
		err:    errors.New("boom"),
	})
	manager.RegisterEventHandler("good", &recordingHandler{
		events: []string{"test.event"},
		calls:  &calls,
		output: []OutboundEvent{{Type: "healthy", Payload: map[string]bool{"ok": true}}},
	})

	output, err := manager.DispatchEvent(context.Background(), PluginEvent{Type: "test.event"})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("DispatchEvent error = %v, want boom", err)
	}
	if len(calls) != 2 {
		t.Fatalf("healthy handler was suppressed: %+v", calls)
	}
	if len(output) != 1 || output[0].Type != "healthy" {
		t.Fatalf("output = %+v", output)
	}
}
