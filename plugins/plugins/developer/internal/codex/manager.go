package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Developer states. The Codex event stream is reduced to these before anything
// reaches the voice model; the raw stream, and reasoning in particular, never
// leaves this package.
const (
	StateAnalyzing     = "analyzing"
	StateEditing       = "editing"
	StateRunningTests  = "running_tests"
	StateAnalysisReady = "analysis_ready"
	StateFixReady      = "fix_ready"
	StateFailed        = "failed"
	StateCancelled     = "cancelled"
)

// MaxDiffBytes caps what a diff can contribute to a tool result.
const MaxDiffBytes = 24 << 10

// maxSummaryBytes caps the agent's own explanation.
const maxSummaryBytes = 4000

// authCacheTTL is how long an "authenticated" answer is trusted before Codex is
// asked again. Short enough to notice a revoked session, long enough that
// asking is not part of every turn.
const authCacheTTL = 5 * time.Minute

// Options configures the manager. There is deliberately no API key field:
// Codex authenticates with the operator's ChatGPT sign-in and nothing else.
type Options struct {
	Binary        string
	ModelProvider string
	Model         string
	WorkspaceRoot string
	ExtraConfig   []string

	// NetworkAccess lets a task's sandbox reach the network — needed when tests
	// download dependencies, off by default.
	NetworkAccess bool

	// TurnTimeout bounds one Codex turn.
	TurnTimeout time.Duration
}

// Change is what a finished developer task can tell the GitHub side about
// itself. It carries no Codex credential and no Codex internals.
type Change struct {
	WorktreePath string
	Branch       string
	Title        string
	Summary      string
	TestsRun     bool
}

// Status is the safe, credential-free answer to "is Codex usable right now".
type Status struct {
	Available     bool   `json:"available"`
	Authenticated bool   `json:"authenticated"`
	Busy          bool   `json:"busy"`
	Version       string `json:"version,omitempty"`
	Model         string `json:"model,omitempty"`
	Plan          string `json:"plan,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

// Task is one developer investigation: a Codex thread pinned to one isolated
// worktree, owned by one StreamCore session.
type Task struct {
	ID         string
	SessionID  string
	Repository string
	ThreadID   string
	Worktree   string
	Branch     string
	Title      string
	Summary    string
	Diff       string
	TestsRun   bool
	State      string
	CreatedAt  time.Time
	UpdatedAt  time.Time

	active *turnTracker
}

// Manager owns the App Server process, the workspace, and the mapping from
// StreamCore sessions to Codex threads.
type Manager struct {
	opts      Options
	server    *AppServer
	workspace *Workspace
	clone     CloneURLFunc

	mu       sync.Mutex
	tasks    map[string]*Task // by StreamCore session id
	trackers map[string]*turnTracker
	started  bool
	shutdown bool

	authMu      sync.Mutex
	authOK      bool
	authPlan    string
	authDetail  string
	authChecked time.Time
}

// New builds a manager. It does not start Codex; Start does, and a failure
// there leaves the rest of StreamCore running.
func New(opts Options, clone CloneURLFunc) (*Manager, error) {
	workspace, err := NewWorkspace(opts.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	if opts.TurnTimeout <= 0 {
		opts.TurnTimeout = 10 * time.Minute
	}
	return &Manager{
		opts:      opts,
		workspace: workspace,
		clone:     clone,
		server:    NewAppServer(opts.Binary, opts.ModelProvider, opts.Model, opts.ExtraConfig),
		tasks:     make(map[string]*Task),
		trackers:  make(map[string]*turnTracker),
	}, nil
}

// Start launches the App Server and checks the sign-in state.
//
// Every failure here is reported and swallowed: Codex is optional, and GitHub,
// voice, and the display must keep working without it.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.server.Start(ctx, m.onNotify); err != nil {
		return err
	}
	m.mu.Lock()
	m.started = true
	m.mu.Unlock()

	status := m.Status(ctx)
	if !status.Authenticated {
		log.Printf("[codex] not signed in: %s", status.Detail)
		return nil
	}
	log.Printf("[codex] signed in with ChatGPT (plan: %s), model %s", status.Plan, m.opts.Model)
	return nil
}

// Shutdown stops accepting work, cancels in-flight turns, and terminates the
// child process. No Codex work survives a StreamCore restart.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.shutdown = true
	trackers := make([]*turnTracker, 0, len(m.trackers))
	for _, tracker := range m.trackers {
		trackers = append(trackers, tracker)
	}
	m.mu.Unlock()

	for _, tracker := range trackers {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		m.interrupt(ctx, tracker)
		cancel()
	}
	m.server.Shutdown()
}

// Status answers the safe question. It never reports a token, a credential
// path's contents, or an account id — only whether Codex can be used.
func (m *Manager) Status(ctx context.Context) Status {
	status := Status{
		Version: m.server.Version(),
		Model:   m.opts.Model,
		Busy:    m.busy(),
	}
	m.mu.Lock()
	started := m.started && !m.shutdown
	m.mu.Unlock()

	if !started || !m.server.Running() {
		status.Detail = "Codex is not running on this server."
		return status
	}
	status.Available = true

	ok, plan, detail := m.checkAuth(ctx)
	status.Authenticated = ok
	status.Plan = plan
	status.Detail = detail
	return status
}

// checkAuth asks Codex who it is, with a short cache so this is not part of
// every turn. An API-key account is treated as unauthenticated on purpose:
// this integration is meant to consume the operator's ChatGPT plan, and
// silently billing an API key instead would be the wrong kind of working.
func (m *Manager) checkAuth(ctx context.Context) (bool, string, string) {
	m.authMu.Lock()
	if time.Since(m.authChecked) < authCacheTTL && m.authChecked != (time.Time{}) {
		ok, plan, detail := m.authOK, m.authPlan, m.authDetail
		m.authMu.Unlock()
		return ok, plan, detail
	}
	m.authMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var account accountResult
	if err := m.server.Call(ctx, methodAccountRead, map[string]any{}, &account); err != nil {
		return m.rememberAuth(false, "", "Could not read the Codex sign-in state: "+truncate(err.Error(), 160))
	}

	switch {
	case account.Account == nil:
		return m.rememberAuth(false, "",
			"Codex is not signed in. On this server, run `codex login` and complete the Sign in with ChatGPT flow.")
	case account.Account.Type == "chatgpt":
		return m.rememberAuth(true, account.Account.PlanType, "")
	case account.Account.Type == "apiKey":
		return m.rememberAuth(false, "",
			"Codex is authenticated with an API key. StreamCore requires the ChatGPT subscription sign-in; run `codex logout` then `codex login`.")
	default:
		return m.rememberAuth(false, "",
			"Codex is signed in with an unsupported account type ("+account.Account.Type+"); StreamCore requires Sign in with ChatGPT.")
	}
}

func (m *Manager) rememberAuth(ok bool, plan, detail string) (bool, string, string) {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	m.authOK, m.authPlan, m.authDetail, m.authChecked = ok, plan, detail, time.Now()
	return ok, plan, detail
}

// invalidateAuth forces the next status check to ask Codex again. Called when a
// turn fails with an authorization error, so a revoked session is noticed
// without polling.
func (m *Manager) invalidateAuth() {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	m.authChecked = time.Time{}
}

func (m *Manager) busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.trackers) > 0
}

// ensureUsable is the single precondition check every mutating entry point runs.
func (m *Manager) ensureUsable(ctx context.Context) error {
	status := m.Status(ctx)
	if !status.Available {
		return fmt.Errorf("Codex is not available on this server")
	}
	if !status.Authenticated {
		return fmt.Errorf("%s", status.Detail)
	}
	return nil
}

// Task returns the developer task for a session, if one exists.
func (m *Manager) Task(sessionID string) (*Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[sessionID]
	return task, ok
}

// PullRequestSource hands the GitHub side exactly what it needs to publish a
// change: a path, a branch, a description, and whether tests were run. No Codex
// credential, no thread id, no event history.
func (m *Manager) PullRequestSource(sessionID, repository string) (Change, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[sessionID]
	if !ok {
		return Change{}, fmt.Errorf("no Codex task is open in this conversation; ask Codex to investigate first")
	}
	if repository != "" && !strings.EqualFold(task.Repository, repository) {
		return Change{}, fmt.Errorf("the open Codex task is on %s, not %s", task.Repository, repository)
	}
	if task.Worktree == "" {
		return Change{}, fmt.Errorf("the Codex task has no isolated worktree")
	}
	title := task.Title
	if title == "" {
		title = "StreamCore: fix from Codex investigation"
	}
	return Change{
		WorktreePath: task.Worktree,
		Branch:       task.Branch,
		Title:        title,
		Summary:      task.Summary,
		TestsRun:     task.TestsRun,
	}, nil
}

// ensureTask returns the session's task, creating the worktree and the Codex
// thread on first use. Follow-ups reuse both, which is what makes "fix it" and
// "show me the diff" continue the same investigation.
func (m *Manager) ensureTask(ctx context.Context, sessionID, repository, baseRef string) (*Task, error) {
	m.mu.Lock()
	existing, ok := m.tasks[sessionID]
	m.mu.Unlock()

	if ok {
		if repository != "" && !strings.EqualFold(existing.Repository, repository) {
			return nil, fmt.Errorf("this conversation already has a Codex task open on %s; cancel it before starting one on %s",
				existing.Repository, repository)
		}
		return existing, nil
	}
	if repository == "" {
		return nil, fmt.Errorf("repository is required to start a Codex task")
	}

	taskID := NewTaskID()
	tree, err := m.workspace.Prepare(ctx, repository, taskID, baseRef, m.clone)
	if err != nil {
		return nil, err
	}

	var started threadResult
	err = m.server.Call(ctx, methodThreadStart, threadStartParams{
		CWD:            tree,
		Sandbox:        "read-only",
		ApprovalPolicy: "never",
		Model:          m.opts.Model,
		ModelProvider:  m.opts.ModelProvider,
		DeveloperInstructions: "You are working inside an isolated git worktree created for one task. " +
			"Never leave this directory, never push to a remote, and never open a pull request — " +
			"StreamCore handles publishing. Keep your final message short and factual: what is wrong, " +
			"which files are involved, and what you changed.",
	}, &started)
	if err != nil {
		m.noteTurnError(err)
		return nil, fmt.Errorf("start Codex thread: %w", err)
	}

	task := &Task{
		ID:         taskID,
		SessionID:  sessionID,
		Repository: repository,
		ThreadID:   started.Thread.ID,
		Worktree:   tree,
		Branch:     "streamcore/" + taskID,
		State:      StateAnalyzing,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	m.mu.Lock()
	m.tasks[sessionID] = task
	m.mu.Unlock()

	log.Printf("[codex] task %s open for %s on %s", taskID, sessionID, repository)
	return task, nil
}

// Result is the bounded outcome of one Codex turn.
type Result struct {
	State      string   `json:"state"`
	Repository string   `json:"repository"`
	Task       string   `json:"task"`
	Summary    string   `json:"summary,omitempty"`
	Files      []string `json:"files,omitempty"`
	Commands   []string `json:"commands,omitempty"`
	TestsRun   bool     `json:"tests_run"`
	TestsPass  *bool    `json:"tests_passed,omitempty"`
	Diff       string   `json:"diff,omitempty"`
	Truncated  bool     `json:"diff_truncated,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// AnalyzeRequest asks Codex to explain a failure without changing anything.
type AnalyzeRequest struct {
	Repository string
	Problem    string
	Context    string
	BaseRef    string
}

// Analyze is read-only from StreamCore's side: the sandbox is read-only, so
// Codex cannot write even if it decides to.
func (m *Manager) Analyze(ctx context.Context, sessionID string, req AnalyzeRequest) (*Result, error) {
	if err := m.ensureUsable(ctx); err != nil {
		return nil, err
	}
	task, err := m.ensureTask(ctx, sessionID, req.Repository, req.BaseRef)
	if err != nil {
		return nil, err
	}

	var prompt strings.Builder
	prompt.WriteString("Investigate this problem in the repository you are in. Do not modify any files.\n\n")
	prompt.WriteString("Problem: " + req.Problem + "\n")
	if strings.TrimSpace(req.Context) != "" {
		prompt.WriteString("\nEvidence:\n" + req.Context + "\n")
	}
	prompt.WriteString("\nRead AGENTS.md if it exists. Then answer in at most six sentences: " +
		"the root cause, the files involved, and the fix you would make.")

	return m.runTurn(ctx, task, prompt.String(), readOnlySandbox(m.opts.NetworkAccess), StateAnalysisReady)
}

// FixRequest asks Codex to change the worktree and verify it.
type FixRequest struct {
	Repository  string
	Instruction string
	TestCommand string
}

// Fix is the mutating path. It is only ever reached after the confirmation gate
// in the tool layer; the sandbox still pins every write to this task's own
// worktree, so an over-eager fix cannot reach the production checkout.
func (m *Manager) Fix(ctx context.Context, sessionID string, req FixRequest) (*Result, error) {
	if err := m.ensureUsable(ctx); err != nil {
		return nil, err
	}
	task, err := m.ensureTask(ctx, sessionID, req.Repository, "")
	if err != nil {
		return nil, err
	}

	var prompt strings.Builder
	prompt.WriteString("Make the fix in this worktree.\n\n")
	prompt.WriteString("Task: " + req.Instruction + "\n")
	prompt.WriteString("\nAfter editing, run the relevant tests")
	if strings.TrimSpace(req.TestCommand) != "" {
		prompt.WriteString(" using: " + req.TestCommand)
	}
	prompt.WriteString(". Do not commit, do not push, do not open a pull request. " +
		"Finish with at most six sentences: what you changed, and whether the tests passed.")

	return m.runTurn(ctx, task, prompt.String(),
		workspaceWriteSandbox(task.Worktree, m.opts.NetworkAccess), StateFixReady)
}

// Test re-runs the task's tests without asking for further edits.
func (m *Manager) Test(ctx context.Context, sessionID, command string) (*Result, error) {
	if err := m.ensureUsable(ctx); err != nil {
		return nil, err
	}
	task, ok := m.Task(sessionID)
	if !ok {
		return nil, fmt.Errorf("no Codex task is open in this conversation")
	}
	prompt := "Run the tests for the change in this worktree and report the result. Do not make further edits."
	if strings.TrimSpace(command) != "" {
		prompt = "Run `" + command + "` in this worktree and report the result. Do not make further edits."
	}
	return m.runTurn(ctx, task, prompt,
		workspaceWriteSandbox(task.Worktree, m.opts.NetworkAccess), StateFixReady)
}

// Diff returns the task's current change without involving the model at all.
func (m *Manager) Diff(ctx context.Context, sessionID string) (*Result, error) {
	task, ok := m.Task(sessionID)
	if !ok {
		return nil, fmt.Errorf("no Codex task is open in this conversation")
	}
	diff, truncated, err := m.workspace.Diff(ctx, task.Worktree, MaxDiffBytes)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	state, summary, testsRun := task.State, task.Summary, task.TestsRun
	m.mu.Unlock()

	return &Result{
		State:      state,
		Repository: task.Repository,
		Task:       task.ID,
		Summary:    summary,
		TestsRun:   testsRun,
		Diff:       diff,
		Truncated:  truncated,
	}, nil
}

// Cancel interrupts the task's active turn through the App Server's own
// mechanism. It is a deliberate command, never wired to voice barge-in: a
// caller talking over the agent has not asked to abandon a five-minute fix.
func (m *Manager) Cancel(ctx context.Context, sessionID string) (*Result, error) {
	task, ok := m.Task(sessionID)
	if !ok {
		return nil, fmt.Errorf("no Codex task is open in this conversation")
	}
	tracker := m.activeTracker(sessionID)
	if tracker == nil {
		m.mu.Lock()
		state := task.State
		m.mu.Unlock()
		return &Result{State: state, Repository: task.Repository, Task: task.ID,
			Summary: "There is no Codex turn running right now."}, nil
	}
	if err := m.interrupt(ctx, tracker); err != nil {
		return nil, err
	}
	m.setTaskState(task, StateCancelled)
	return &Result{State: StateCancelled, Repository: task.Repository, Task: task.ID,
		Summary: "Cancelled the running Codex turn. The worktree is untouched from here on."}, nil
}

func (m *Manager) interrupt(ctx context.Context, tracker *turnTracker) error {
	return m.server.Call(ctx, methodTurnInterrupt, turnInterruptParams{
		ThreadID: tracker.threadID,
		TurnID:   tracker.turn(),
	}, nil)
}

// setTaskState is the only way task state changes outside applyResult. Both
// hold the same lock, because a cancel and a completing turn genuinely race.
func (m *Manager) setTaskState(task *Task, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task.State = state
	task.UpdatedAt = time.Now()
}

// activeTracker reports the in-flight turn for a session, if any.
func (m *Manager) activeTracker(sessionID string) *turnTracker {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[sessionID]
	if !ok {
		return nil
	}
	return task.active
}

// Cleanup removes the task and its worktree for one session.
func (m *Manager) Cleanup(ctx context.Context, sessionID string) {
	m.mu.Lock()
	task, ok := m.tasks[sessionID]
	if ok {
		delete(m.tasks, sessionID)
	}
	m.mu.Unlock()
	if !ok || task.Worktree == "" {
		return
	}
	if err := m.workspace.Remove(ctx, task.Worktree); err != nil {
		log.Printf("[codex] cleanup %s: %v", task.ID, err)
	}
}

func (m *Manager) noteTurnError(err error) {
	if err == nil {
		return
	}
	if strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
		m.invalidateAuth()
	}
}

func logDebug(format string, args ...any) {
	log.Printf("[codex] "+format, args...)
}

// onNotify runs on the App Server reader goroutine. It must never block, so it
// only routes into a tracker's own buffers and returns.
func (m *Manager) onNotify(method string, params json.RawMessage) {
	switch method {
	case notifyItemStarted, notifyItemCompleted:
		var payload itemNotification
		if json.Unmarshal(params, &payload) != nil {
			return
		}
		if tracker := m.trackerFor(payload.ThreadID); tracker != nil {
			tracker.observeItem(method, payload)
		}
	case notifyTurnDiffUpdated:
		var payload turnDiffParams
		if json.Unmarshal(params, &payload) != nil {
			return
		}
		if tracker := m.trackerFor(payload.ThreadID); tracker != nil {
			tracker.setDiff(payload.Diff)
		}
	case notifyTurnCompleted:
		var payload turnCompletedParams
		if json.Unmarshal(params, &payload) != nil {
			return
		}
		if tracker := m.trackerFor(payload.ThreadID); tracker != nil {
			tracker.complete(payload.Turn)
		}
	case notifyError:
		var payload errorNotification
		if json.Unmarshal(params, &payload) != nil {
			return
		}
		if tracker := m.trackerFor(payload.ThreadID); tracker != nil {
			tracker.observeError(payload.Error.Message)
		}
	}
}

func (m *Manager) trackerFor(threadID string) *turnTracker {
	if threadID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.trackers[threadID]
}

// WorktreeRetention is how long an abandoned worktree survives. It is generous
// on purpose: a worktree behind an open pull request is worth keeping around
// long after the conversation that produced it ended.
const WorktreeRetention = 24 * time.Hour

// StartSweeper removes abandoned worktrees on an interval. A task StreamCore
// still owns is never touched, and neither is anything outside the workspace
// root — Remove re-checks containment on every path.
func (m *Manager) StartSweeper(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Hour
	}
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.sweep(ctx)
			}
		}
	}()
}

func (m *Manager) sweep(ctx context.Context) {
	m.mu.Lock()
	live := make(map[string]bool, len(m.tasks))
	for _, task := range m.tasks {
		live[task.ID] = true
	}
	m.mu.Unlock()

	for _, tree := range m.workspace.Orphans(WorktreeRetention, live) {
		if err := m.workspace.Remove(ctx, tree); err != nil {
			log.Printf("[codex] sweep %s: %v", tree, err)
			continue
		}
		log.Printf("[codex] removed abandoned worktree %s", tree)
	}
}
