package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/streamcoreai/streamcore-server/internal/plugin"
)

type gateProbe struct {
	confirm bool
	calls   int
	args    json.RawMessage
	session string
}

func (g *gateProbe) Name() string                { return "probe.tool" }
func (g *gateProbe) Description() string         { return "" }
func (g *gateProbe) Parameters() json.RawMessage { return []byte(`{}`) }
func (g *gateProbe) ConfirmationRequired() bool  { return g.confirm }
func (g *gateProbe) ThinkingSound() bool         { return false }

func (g *gateProbe) Execute(params json.RawMessage) (string, error) {
	g.calls++
	g.args = params
	return "ran", nil
}

func (g *gateProbe) ExecuteInSession(_ context.Context, sessionID string, params json.RawMessage) (string, error) {
	g.session = sessionID
	return g.Execute(params)
}

func newGatePipeline(t *testing.T, sessionID string) *Pipeline {
	t.Helper()
	return &Pipeline{
		pluginMgr: plugin.NewManager(""),
		conv:      &ConversationState{SessionID: sessionID},
	}
}

// The gate is additive: everything that existed before it declares
// confirmation_required = false and must be unaffected.
func TestUngatedToolsRunUnchanged(t *testing.T) {
	p := newGatePipeline(t, "session-1")
	tool := &gateProbe{}
	args := json.RawMessage(`{"a":1}`)

	clean, challenge, err := p.gateToolCall(tool, args)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if challenge != "" {
		t.Fatalf("an ungated tool was challenged: %s", challenge)
	}
	if _, err := p.runTool(context.Background(), tool, clean); err != nil {
		t.Fatalf("run: %v", err)
	}
	if tool.calls != 1 || string(tool.args) != `{"a":1}` {
		t.Fatalf("arguments were altered: %d calls, %s", tool.calls, tool.args)
	}
}

func TestGatedToolIsChallengedThenRuns(t *testing.T) {
	p := newGatePipeline(t, "session-1")
	tool := &gateProbe{confirm: true}
	args := json.RawMessage(`{"repository":"a/b"}`)

	_, challenge, err := p.gateToolCall(tool, args)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if challenge == "" {
		t.Fatal("a mutating tool ran on the first call")
	}
	if tool.calls != 0 {
		t.Fatal("the tool executed while being challenged")
	}

	var decoded plugin.ConfirmationChallenge
	if err := json.Unmarshal([]byte(challenge), &decoded); err != nil {
		t.Fatalf("the challenge is not JSON the model can read: %v", err)
	}
	if decoded.Status != "confirmation_required" || decoded.ConfirmToken == "" {
		t.Fatalf("challenge is %+v", decoded)
	}

	confirmed, _ := json.Marshal(map[string]any{
		"repository":             "a/b",
		plugin.ConfirmTokenField: decoded.ConfirmToken,
	})
	clean, challenge2, err := p.gateToolCall(tool, confirmed)
	if err != nil {
		t.Fatalf("second gate: %v", err)
	}
	if challenge2 != "" {
		t.Fatalf("the confirmed call was challenged again: %s", challenge2)
	}
	if _, err := p.runTool(context.Background(), tool, clean); err != nil {
		t.Fatalf("run: %v", err)
	}
	if tool.calls != 1 {
		t.Fatalf("the tool ran %d times", tool.calls)
	}
	if strings.Contains(string(tool.args), plugin.ConfirmTokenField) {
		t.Fatalf("the tool saw the confirmation token: %s", tool.args)
	}
}

// The gate is keyed to the conversation, so one caller's approval cannot be
// spent by another.
func TestConfirmationIsScopedToTheConversation(t *testing.T) {
	manager := plugin.NewManager("")
	first := &Pipeline{pluginMgr: manager, conv: &ConversationState{SessionID: "session-a"}}
	second := &Pipeline{pluginMgr: manager, conv: &ConversationState{SessionID: "session-b"}}
	tool := &gateProbe{confirm: true}

	_, challenge, _ := first.gateToolCall(tool, json.RawMessage(`{}`))
	var decoded plugin.ConfirmationChallenge
	json.Unmarshal([]byte(challenge), &decoded)

	confirmed, _ := json.Marshal(map[string]any{plugin.ConfirmTokenField: decoded.ConfirmToken})
	if _, _, err := second.gateToolCall(tool, confirmed); err == nil {
		t.Fatal("one conversation's confirmation authorised another's mutation")
	}
}

// A session-aware tool must be told which conversation it is running in, or a
// developer task ends up shared between callers.
func TestSessionToolsReceiveTheSession(t *testing.T) {
	p := newGatePipeline(t, "session-42")
	tool := &gateProbe{}

	if _, err := p.runTool(context.Background(), tool, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if tool.session != "session-42" {
		t.Fatalf("the tool was given session %q", tool.session)
	}
}
