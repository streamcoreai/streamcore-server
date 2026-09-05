package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureManager gives the manager a real local repository to clone, so a task
// can be created without GitHub.
func fixtureManager(t *testing.T, account, turn string) *Manager {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	origin := seedRepo(t)

	t.Setenv(fakeMarker, "1")
	t.Setenv(fakeAccount, account)
	t.Setenv(fakeTurn, turn)

	manager, err := New(Options{
		Binary:        os.Args[0],
		ModelProvider: "openai",
		Model:         "fake-model",
		WorkspaceRoot: t.TempDir(),
		TurnTimeout:   10 * time.Second,
	}, func(context.Context, string) (string, string, error) {
		return "file://" + origin, "unused", nil
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(manager.Shutdown)
	return manager
}

func TestAnalyzeReducesTheEventStream(t *testing.T) {
	manager := fixtureManager(t, "chatgpt", "ok")

	result, err := manager.Analyze(context.Background(), "session-1", AnalyzeRequest{
		Repository: "streamcoreai/fixture",
		Problem:    "CI failed in display_card_test",
		Context:    "expected version 1, received version 2",
	})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	if result.State != StateAnalysisReady {
		t.Fatalf("state is %q", result.State)
	}
	if !strings.Contains(result.Summary, "card version") {
		t.Fatalf("the agent's answer was lost: %q", result.Summary)
	}
	if len(result.Files) != 1 || result.Files[0] != "internal/displayprojector/projector.go" {
		t.Fatalf("touched files are %v", result.Files)
	}
	if !result.TestsRun || result.TestsPass == nil || !*result.TestsPass {
		t.Fatalf("the test run was not recorded: run=%v pass=%v", result.TestsRun, result.TestsPass)
	}

	// Codex's private reasoning must not reach the voice model, in any field.
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "the user probably wants") {
		t.Fatalf("reasoning leaked into the tool result: %s", encoded)
	}
}

// A failed turn reports a readable reason, not a serialised error object.
func TestFailedTurnIsExplained(t *testing.T) {
	manager := fixtureManager(t, "chatgpt", "fail")

	result, err := manager.Analyze(context.Background(), "session-1", AnalyzeRequest{
		Repository: "streamcoreai/fixture", Problem: "x",
	})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if result.State != StateFailed {
		t.Fatalf("state is %q", result.State)
	}
	if !strings.Contains(result.Error, "not supported") {
		t.Fatalf("the failure is not readable: %q", result.Error)
	}
	if strings.Contains(result.Error, `{"type"`) {
		t.Fatalf("the raw error object was passed through: %q", result.Error)
	}
}

// "Fix it" and "show me the diff" have to land on the same investigation.
func TestFollowUpsContinueTheSameTask(t *testing.T) {
	manager := fixtureManager(t, "chatgpt", "ok")
	ctx := context.Background()

	if _, err := manager.Analyze(ctx, "session-1", AnalyzeRequest{
		Repository: "streamcoreai/fixture", Problem: "x",
	}); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	first, _ := manager.Task("session-1")

	if _, err := manager.Fix(ctx, "session-1", FixRequest{
		Repository: "streamcoreai/fixture", Instruction: "update the test",
	}); err != nil {
		t.Fatalf("fix: %v", err)
	}
	second, _ := manager.Task("session-1")

	if first.ThreadID != second.ThreadID || first.Worktree != second.Worktree {
		t.Fatal("the follow-up started a new thread instead of continuing the investigation")
	}
}

// Two callers must never end up sharing a worktree.
func TestSessionsDoNotShareTasks(t *testing.T) {
	manager := fixtureManager(t, "chatgpt", "ok")
	ctx := context.Background()

	for _, session := range []string{"session-a", "session-b"} {
		if _, err := manager.Analyze(ctx, session, AnalyzeRequest{
			Repository: "streamcoreai/fixture", Problem: "x",
		}); err != nil {
			t.Fatalf("analyze %s: %v", session, err)
		}
	}

	a, _ := manager.Task("session-a")
	b, _ := manager.Task("session-b")
	if a.Worktree == b.Worktree || a.ID == b.ID {
		t.Fatal("two sessions were given the same developer task")
	}
	if _, err := manager.PullRequestSource("session-c", "streamcoreai/fixture"); err == nil {
		t.Fatal("a session with no task could publish one")
	}
}

// The GitHub side gets a path, a branch and a verdict — never a credential.
func TestPullRequestSourceCarriesNoCredentials(t *testing.T) {
	manager := fixtureManager(t, "chatgpt", "ok")
	ctx := context.Background()

	if _, err := manager.Fix(ctx, "session-1", FixRequest{
		Repository: "streamcoreai/fixture", Instruction: "update the test",
	}); err != nil {
		t.Fatalf("fix: %v", err)
	}

	change, err := manager.PullRequestSource("session-1", "streamcoreai/fixture")
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	if change.WorktreePath == "" || !change.TestsRun {
		t.Fatalf("the change is missing what GitHub needs: %+v", change)
	}
	if err := manager.workspace.contains(change.WorktreePath); err != nil {
		t.Fatalf("the published worktree is outside the workspace: %v", err)
	}

	encoded, _ := json.Marshal(change)
	for _, forbidden := range []string{"ghs_", "auth.json", "access_token", "chatgpt"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("the change carried %q: %s", forbidden, encoded)
		}
	}

	// A different repository must not be publishable from this task.
	if _, err := manager.PullRequestSource("session-1", "someone/else"); err == nil {
		t.Fatal("a task on one repository could publish to another")
	}
}

func TestCancelInterruptsTheTurn(t *testing.T) {
	manager := fixtureManager(t, "chatgpt", "hang")
	ctx := context.Background()

	done := make(chan *Result, 1)
	go func() {
		result, _ := manager.Analyze(ctx, "session-1", AnalyzeRequest{
			Repository: "streamcoreai/fixture", Problem: "x",
		})
		done <- result
	}()

	// Wait for the turn to be in flight before cancelling it.
	deadline := time.After(10 * time.Second)
	for {
		if manager.activeTracker("session-1") != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the turn never started")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if _, err := manager.Cancel(ctx, "session-1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	select {
	case result := <-done:
		if result == nil || result.State != StateCancelled {
			t.Fatalf("the cancelled turn reported %+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled turn never returned")
	}
}

func TestDiffReadsTheWorktreeDirectly(t *testing.T) {
	manager := fixtureManager(t, "chatgpt", "ok")
	ctx := context.Background()

	if _, err := manager.Analyze(ctx, "session-1", AnalyzeRequest{
		Repository: "streamcoreai/fixture", Problem: "x",
	}); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	task, _ := manager.Task("session-1")
	if err := os.WriteFile(filepath.Join(task.Worktree, "a.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := manager.Diff(ctx, "session-1")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !strings.Contains(result.Diff, "changed") {
		t.Fatalf("the diff missed the change:\n%s", result.Diff)
	}
}

func TestDescribeTurnError(t *testing.T) {
	nested := `{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"The 'x' model is not supported."}}`
	if got := describeTurnError(nested); got != "The 'x' model is not supported." {
		t.Fatalf("unwrapped message is %q", got)
	}
	if got := describeTurnError("  plain  failure  "); got != "plain failure" {
		t.Fatalf("plain message is %q", got)
	}
}
