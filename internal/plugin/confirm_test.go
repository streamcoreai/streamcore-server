package plugin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type fakeTool struct {
	name    string
	confirm bool
	prompt  string
}

func (f fakeTool) Name() string                { return f.name }
func (f fakeTool) Description() string         { return "" }
func (f fakeTool) Parameters() json.RawMessage { return []byte(`{}`) }
func (f fakeTool) ConfirmationRequired() bool  { return f.confirm }
func (f fakeTool) ThinkingSound() bool         { return false }
func (f fakeTool) Execute(json.RawMessage) (string, error) {
	return "", nil
}
func (f fakeTool) ConfirmationPrompt(json.RawMessage) string { return f.prompt }

func TestGatePassesUnconfirmedTools(t *testing.T) {
	store := NewConfirmationStore(0)
	args := json.RawMessage(`{"repository":"a/b"}`)

	clean, challenge, err := store.Gate("session-1", fakeTool{name: "github.file"}, args)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if challenge != nil {
		t.Fatalf("a tool that does not require confirmation was challenged")
	}
	if string(clean) != string(args) {
		t.Fatalf("arguments changed: %s", clean)
	}
}

func TestGateChallengesThenExecutes(t *testing.T) {
	store := NewConfirmationStore(0)
	tool := fakeTool{name: "codex.fix", confirm: true, prompt: "Shall I?"}
	args := json.RawMessage(`{"repository":"a/b","instruction":"fix the test"}`)

	_, challenge, err := store.Gate("session-1", tool, args)
	if err != nil {
		t.Fatalf("first gate: %v", err)
	}
	if challenge == nil {
		t.Fatal("a confirmation-required tool ran without being challenged")
	}
	if challenge.Status != "confirmation_required" || challenge.Prompt != "Shall I?" {
		t.Fatalf("unexpected challenge: %+v", challenge)
	}

	confirmed := withToken(t, args, challenge.ConfirmToken)
	clean, challenge2, err := store.Gate("session-1", tool, confirmed)
	if err != nil {
		t.Fatalf("second gate: %v", err)
	}
	if challenge2 != nil {
		t.Fatal("a confirmed call was challenged again")
	}
	if strings.Contains(string(clean), ConfirmTokenField) {
		t.Fatalf("confirm_token leaked into tool arguments: %s", clean)
	}
}

func TestGateRejectsReplay(t *testing.T) {
	store := NewConfirmationStore(0)
	tool := fakeTool{name: "codex.fix", confirm: true}
	args := json.RawMessage(`{"repository":"a/b"}`)

	_, challenge, _ := store.Gate("session-1", tool, args)
	confirmed := withToken(t, args, challenge.ConfirmToken)

	if _, _, err := store.Gate("session-1", tool, confirmed); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, _, err := store.Gate("session-1", tool, confirmed); err == nil {
		t.Fatal("a confirmation token was accepted twice")
	}
}

func TestGateRejectsForeignSession(t *testing.T) {
	store := NewConfirmationStore(0)
	tool := fakeTool{name: "codex.fix", confirm: true}
	args := json.RawMessage(`{"repository":"a/b"}`)

	_, challenge, _ := store.Gate("session-1", tool, args)
	confirmed := withToken(t, args, challenge.ConfirmToken)

	if _, _, err := store.Gate("session-2", tool, confirmed); err == nil {
		t.Fatal("one session's confirmation authorised another session's call")
	}
}

func TestGateRejectsChangedArguments(t *testing.T) {
	store := NewConfirmationStore(0)
	tool := fakeTool{name: "github.create_pull_request", confirm: true}

	_, challenge, _ := store.Gate("session-1", tool, json.RawMessage(`{"repository":"a/b"}`))
	swapped := withToken(t, json.RawMessage(`{"repository":"other/repo"}`), challenge.ConfirmToken)

	_, _, err := store.Gate("session-1", tool, swapped)
	if err == nil {
		t.Fatal("the confirmed arguments were swapped after approval")
	}
	if !strings.Contains(err.Error(), "arguments changed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Key order must not matter: the model re-emits the object, not the bytes.
func TestGateIgnoresKeyOrder(t *testing.T) {
	store := NewConfirmationStore(0)
	tool := fakeTool{name: "codex.fix", confirm: true}

	_, challenge, _ := store.Gate("session-1", tool, json.RawMessage(`{"a":1,"b":2}`))
	reordered := withToken(t, json.RawMessage(`{"b":2,"a":1}`), challenge.ConfirmToken)

	if _, _, err := store.Gate("session-1", tool, reordered); err != nil {
		t.Fatalf("reordered keys were treated as different arguments: %v", err)
	}
}

func TestGateRejectsExpiredToken(t *testing.T) {
	store := NewConfirmationStore(time.Nanosecond)
	tool := fakeTool{name: "codex.fix", confirm: true}
	args := json.RawMessage(`{"repository":"a/b"}`)

	_, challenge, _ := store.Gate("session-1", tool, args)
	time.Sleep(2 * time.Millisecond)

	if _, _, err := store.Gate("session-1", tool, withToken(t, args, challenge.ConfirmToken)); err == nil {
		t.Fatal("an expired confirmation was accepted")
	}
}

func TestGateRejectsInventedToken(t *testing.T) {
	store := NewConfirmationStore(0)
	tool := fakeTool{name: "codex.fix", confirm: true}
	args := withToken(t, json.RawMessage(`{"repository":"a/b"}`), "confirm_deadbeef")

	if _, _, err := store.Gate("session-1", tool, args); err == nil {
		t.Fatal("the model authorised its own mutating call")
	}
}

func withToken(t *testing.T, args json.RawMessage, token string) json.RawMessage {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(args, &fields); err != nil {
		t.Fatalf("decode args: %v", err)
	}
	fields[ConfirmTokenField] = token
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	return encoded
}
