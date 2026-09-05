package codex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"developer/internal/agent"
)

func TestToolSurfaceIsSmall(t *testing.T) {
	manager := newFakeManager(t, "chatgpt", "ok")

	names := map[string]agent.Tool{}
	for _, tool := range manager.Tools() {
		names[tool.Name()] = tool
	}
	for _, want := range []string{"codex.analyze", "codex.fix", "codex.test", "codex.status", "codex.diff", "codex.cancel"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("%s is missing", want)
		}
	}
	if len(names) != 6 {
		t.Fatalf("the tool surface grew to %d tools: %v", len(names), names)
	}

	// Nothing here may hand the voice model a shell.
	for name, tool := range names {
		schema := string(tool.Parameters())
		for _, forbidden := range []string{"\"path\"", "\"cwd\"", "\"shell\"", "\"command\"}"} {
			if strings.Contains(schema, forbidden) && name != "codex.test" {
				t.Fatalf("%s exposes %s in its schema: %s", name, forbidden, schema)
			}
		}
	}
}

// Only the mutating tool is gated, and it is gated by the shared mechanism
// rather than one of its own.
func TestOnlyFixRequiresConfirmation(t *testing.T) {
	manager := newFakeManager(t, "chatgpt", "ok")

	for _, tool := range manager.Tools() {
		want := tool.Name() == "codex.fix"
		if tool.ConfirmationRequired() != want {
			t.Fatalf("%s: confirmation_required is %v, expected %v", tool.Name(), tool.ConfirmationRequired(), want)
		}
	}
}

func TestFixIsNotExecutableWithoutConfirmation(t *testing.T) {
	manager := newFakeManager(t, "chatgpt", "ok")

	var fix agent.Tool
	for _, tool := range manager.Tools() {
		if tool.Name() == "codex.fix" {
			fix = tool
		}
	}
	if fix == nil {
		t.Fatal("codex.fix is missing")
	}
	// The gate itself lives in the server. What has to be true here is that
	// the tool asks for it, and can say what it is about to do.
	if !fix.ConfirmationRequired() {
		t.Fatal("codex.fix is not confirmation gated")
	}

	prompter, ok := fix.(agent.ConfirmationPrompter)
	if !ok {
		t.Fatal("codex.fix cannot describe what it is about to do")
	}
	prompt := prompter.ConfirmationPrompt(json.RawMessage(`{"repository":"a/b","instruction":"Update the failing test"}`))
	if !strings.Contains(prompt, "isolated") {
		t.Fatalf("the prompt does not say the work is isolated: %q", prompt)
	}
	if !strings.Contains(prompt, "update the failing test") {
		t.Fatalf("the prompt does not say what will happen: %q", prompt)
	}
}

// A tool that owns a worktree needs to know whose conversation it is in. Without
// one it must refuse rather than fall back to a shared task.
func TestSessionlessCallsRefuse(t *testing.T) {
	manager := newFakeManager(t, "chatgpt", "ok")

	for _, tool := range manager.Tools() {
		if tool.Name() == "codex.status" {
			continue
		}
		out, err := tool.Execute(json.RawMessage(`{"repository":"a/b","instruction":"x","problem":"y"}`))
		if err != nil {
			t.Fatalf("%s returned a hard error: %v", tool.Name(), err)
		}
		if !strings.Contains(out, "conversation") {
			t.Fatalf("%s ran without a session: %s", tool.Name(), out)
		}
	}
}

// codex.status is the one tool that works without a conversation, because it
// answers a question about the server rather than about a task.
func TestStatusToolNeedsNoSession(t *testing.T) {
	manager := newFakeManager(t, "chatgpt", "ok")

	for _, tool := range manager.Tools() {
		if tool.Name() != "codex.status" {
			continue
		}
		scoped, ok := tool.(agent.SessionTool)
		if !ok {
			t.Fatal("codex.status is not session-aware")
		}
		out, err := scoped.ExecuteInSession(context.Background(), "", nil)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		var status Status
		if err := json.Unmarshal([]byte(out), &status); err != nil {
			t.Fatalf("decode status: %v (%s)", err, out)
		}
		if !status.Authenticated {
			t.Fatalf("status reported %+v", status)
		}
	}
}

// Every codex tool must satisfy the session-aware interface, or the pipeline
// silently falls back to Execute and the task mapping breaks.
func TestToolsAreSessionAware(t *testing.T) {
	manager := newFakeManager(t, "chatgpt", "ok")
	for _, tool := range manager.Tools() {
		if _, ok := tool.(agent.SessionTool); !ok {
			t.Fatalf("%s is not session-aware", tool.Name())
		}
	}
}
