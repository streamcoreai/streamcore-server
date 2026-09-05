package codex

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// testCommandPattern recognises a test run in a shell command, so a turn can
// report running_tests rather than a generic "working".
var testCommandPattern = regexp.MustCompile(`(?i)\b(go test|cargo test|pytest|npm (run )?test|yarn test|pnpm test|jest|vitest|ctest|gradle test|mvn test|rspec|tox)\b`)

const (
	maxTrackedFiles    = 12
	maxTrackedCommands = 8
)

// turnTracker collects one Codex turn's notifications and reduces them to the
// small set of facts a voice answer can be built from.
//
// Reasoning items are dropped here, on purpose and without exception: Codex's
// private thinking is not something to read out, log, or hand to another model.
type turnTracker struct {
	threadID string
	done     chan turnState

	mu        sync.Mutex
	turnID    string
	messages  []string
	files     []string
	fileSeen  map[string]bool
	commands  []string
	testsRun  bool
	testsPass *bool
	diff      string
	errors    []string
	state     string
	finished  bool
}

func newTurnTracker(threadID string) *turnTracker {
	return &turnTracker{
		threadID: threadID,
		done:     make(chan turnState, 1),
		fileSeen: make(map[string]bool),
		state:    StateAnalyzing,
	}
}

func (t *turnTracker) observeItem(method string, payload itemNotification) {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch payload.Item.Type {
	case "reasoning", "plan", "webSearch", "imageView", "sleep", "contextCompaction":
		// Never surfaced. Reasoning in particular is private to Codex.
		return

	case "agentMessage":
		if method == notifyItemCompleted && strings.TrimSpace(payload.Item.Text) != "" {
			t.messages = append(t.messages, strings.TrimSpace(payload.Item.Text))
		}

	case "commandExecution":
		command := collapse(payload.Item.Command)
		if command == "" {
			return
		}
		isTest := testCommandPattern.MatchString(command)
		if isTest {
			t.testsRun = true
			t.state = StateRunningTests
		} else if t.state != StateRunningTests {
			t.state = StateAnalyzing
		}
		if method == notifyItemCompleted {
			if len(t.commands) < maxTrackedCommands {
				t.commands = append(t.commands, truncate(command, 160))
			}
			if isTest && payload.Item.ExitCode != nil {
				passed := *payload.Item.ExitCode == 0
				t.testsPass = &passed
			}
		}

	case "fileChange":
		t.state = StateEditing
		for _, change := range payload.Item.Changes {
			if change.Path == "" || t.fileSeen[change.Path] {
				continue
			}
			t.fileSeen[change.Path] = true
			if len(t.files) < maxTrackedFiles {
				t.files = append(t.files, change.Path)
			}
		}
	}
}

func (t *turnTracker) setTurnID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turnID = id
}

func (t *turnTracker) turn() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.turnID
}

func (t *turnTracker) setDiff(diff string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.diff = diff
}

func (t *turnTracker) observeError(message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if message != "" {
		t.errors = append(t.errors, truncate(collapse(message), 300))
	}
}

func (t *turnTracker) complete(state turnState) {
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return
	}
	t.finished = true
	t.mu.Unlock()

	select {
	case t.done <- state:
	default:
	}
}

// snapshot builds the bounded result. finalState is what a clean turn should
// report; a failure or cancellation overrides it.
func (t *turnTracker) snapshot(task *Task, status turnState, finalState string) *Result {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := &Result{
		Repository: task.Repository,
		Task:       task.ID,
		Files:      append([]string(nil), t.files...),
		Commands:   append([]string(nil), t.commands...),
		TestsRun:   t.testsRun,
		TestsPass:  t.testsPass,
	}

	switch status.Status {
	case "completed":
		result.State = finalState
	case "interrupted":
		result.State = StateCancelled
	default:
		result.State = StateFailed
	}
	if status.Error != nil && status.Error.Message != "" {
		result.Error = truncate(describeTurnError(status.Error.Message), 300)
		result.State = StateFailed
	} else if len(t.errors) > 0 && result.State == StateFailed {
		result.Error = t.errors[len(t.errors)-1]
	}

	// The last agent message is the answer; earlier ones are progress.
	if len(t.messages) > 0 {
		result.Summary = truncate(t.messages[len(t.messages)-1], maxSummaryBytes)
	}
	if t.diff != "" {
		if len(t.diff) > MaxDiffBytes {
			result.Diff = t.diff[:MaxDiffBytes]
			result.Truncated = true
		} else {
			result.Diff = t.diff
		}
	}
	return result
}

// describeTurnError unwraps the JSON error body Codex sometimes nests inside a
// turn error, so the agent says "model not supported" rather than reading out a
// serialised object.
func describeTurnError(message string) string {
	trimmed := strings.TrimSpace(message)
	if !strings.HasPrefix(trimmed, "{") {
		return collapse(trimmed)
	}
	if start := strings.Index(trimmed, `"message":"`); start >= 0 {
		rest := trimmed[start+len(`"message":"`):]
		if end := strings.Index(rest, `"`); end > 0 {
			return collapse(rest[:end])
		}
	}
	return collapse(trimmed)
}

// runTurn sends one turn and waits for it to settle.
//
// The wait happens on the caller's goroutine, which is a tool-call goroutine —
// never the media path. No lock held here is ever touched by audio, and the
// pipeline keeps streaming while Codex works.
func (m *Manager) runTurn(ctx context.Context, task *Task, prompt string, sandbox *sandboxPolicy, finalState string) (*Result, error) {
	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return nil, fmt.Errorf("StreamCore is shutting down; not starting a Codex turn")
	}
	if _, busy := m.trackers[task.ThreadID]; busy {
		m.mu.Unlock()
		return nil, fmt.Errorf("this Codex task is already working; wait for it or cancel it")
	}
	tracker := newTurnTracker(task.ThreadID)
	m.trackers[task.ThreadID] = tracker
	task.active = tracker
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.trackers, task.ThreadID)
		task.active = nil
		m.mu.Unlock()
	}()

	var started turnResult
	err := m.server.Call(ctx, methodTurnStart, turnStartParams{
		ThreadID:       task.ThreadID,
		CWD:            task.Worktree,
		Input:          []turnInput{{Type: "text", Text: prompt}},
		SandboxPolicy:  sandbox,
		ApprovalPolicy: "never",
		Model:          m.opts.Model,
		Summary:        "none",
	}, &started)
	if err != nil {
		m.noteTurnError(err)
		return nil, fmt.Errorf("start Codex turn: %w", err)
	}
	tracker.setTurnID(started.Turn.ID)

	timeout := time.NewTimer(m.opts.TurnTimeout)
	defer timeout.Stop()

	select {
	case status := <-tracker.done:
		result := tracker.snapshot(task, status, finalState)
		m.applyResult(task, result)
		if result.State == StateFailed && result.Error != "" {
			m.noteTurnError(fmt.Errorf("%s", result.Error))
		}
		return result, nil

	case <-timeout.C:
		interruptCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		m.interrupt(interruptCtx, tracker)
		cancel()
		m.setTaskState(task, StateFailed)
		return &Result{
			State:      StateFailed,
			Repository: task.Repository,
			Task:       task.ID,
			Error:      fmt.Sprintf("Codex did not finish within %s and was interrupted.", m.opts.TurnTimeout),
		}, nil

	case <-ctx.Done():
		interruptCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		m.interrupt(interruptCtx, tracker)
		cancel()
		return nil, ctx.Err()
	}
}

// applyResult folds one turn's outcome into the task, so a later "show me the
// diff" or "open a PR" sees what actually happened.
func (m *Manager) applyResult(task *Task, result *Result) {
	m.mu.Lock()
	defer m.mu.Unlock()

	task.State = result.State
	task.UpdatedAt = time.Now()
	if result.Summary != "" {
		task.Summary = result.Summary
		if task.Title == "" {
			task.Title = firstSentence(result.Summary)
		}
	}
	if result.Diff != "" {
		task.Diff = result.Diff
	}
	if result.TestsRun {
		task.TestsRun = true
	}
}

func firstSentence(text string) string {
	text = collapse(text)
	if idx := strings.IndexAny(text, ".!?"); idx > 0 && idx < 72 {
		return text[:idx]
	}
	return truncate(text, 72)
}

func collapse(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
