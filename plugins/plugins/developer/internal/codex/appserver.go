package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TestedVersion is the Codex CLI this integration was built and verified
// against. Anything older is refused; anything newer is allowed with a warning,
// because the App Server protocol is additive but not frozen.
const TestedVersion = "0.144.1"

// MinimumVersion is the oldest Codex known to expose thread/turn/approval
// methods in the shape protocol.go encodes.
const MinimumVersion = "0.144.0"

var versionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// handshakeTimeout bounds startup. A Codex that has not answered initialize by
// then is not going to.
const handshakeTimeout = 30 * time.Second

// ErrNotRunning is returned once the App Server has exited or been shut down.
var ErrNotRunning = errors.New("codex app server is not running")

// AppServer owns one long-lived `codex app-server` child process and the
// JSON-RPC conversation with it. Threads are multiplexed over this single
// process; a new one is not started per request.
type AppServer struct {
	binary  string
	args    []string
	version string

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	pending  map[string]chan jsonrpcMessage
	running  bool
	exitErr  error
	closed   chan struct{}
	nextID   atomic.Int64
	codexDir string

	// onNotify receives every server notification. It runs on the reader
	// goroutine, so it must not block: the manager forwards into buffered
	// per-turn channels and returns.
	onNotify func(method string, params json.RawMessage)
}

// NewAppServer prepares, but does not start, the child process.
//
// The provider and model are pinned on the command line rather than left to
// ~/.codex/config.toml. An operator whose Codex defaults point at a third-party
// provider would otherwise get a StreamCore that silently never touches their
// ChatGPT subscription.
func NewAppServer(binary, modelProvider, model string, extraConfig []string) *AppServer {
	if binary == "" {
		binary = "codex"
	}
	args := []string{"app-server", "--stdio"}
	if modelProvider != "" {
		args = append(args, "-c", "model_provider="+modelProvider)
	}
	if model != "" {
		args = append(args, "-c", "model="+model)
	}
	for _, override := range extraConfig {
		if strings.TrimSpace(override) != "" {
			args = append(args, "-c", override)
		}
	}
	return &AppServer{
		binary:  binary,
		args:    args,
		pending: make(map[string]chan jsonrpcMessage),
		closed:  make(chan struct{}),
	}
}

// Version reports the detected Codex CLI version, empty before Start.
func (a *AppServer) Version() string { return a.version }

// CodexHome reports where Codex keeps its own state, including the credential
// file it owns. StreamCore only ever displays this path; it never reads it.
func (a *AppServer) CodexHome() string { return a.codexDir }

// DetectVersion runs `codex --version` and checks it against the supported
// range. It is the first thing Start does, so an incompatible or missing binary
// produces a clear message instead of a protocol error later.
func (a *AppServer) DetectVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, a.binary, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w", a.binary, err)
	}
	match := versionPattern.FindString(string(out))
	if match == "" {
		return "", fmt.Errorf("could not read a version from %q", strings.TrimSpace(string(out)))
	}
	if compareVersions(match, MinimumVersion) < 0 {
		return match, fmt.Errorf("codex %s is older than the supported minimum %s; upgrade with `codex update`", match, MinimumVersion)
	}
	if compareVersions(match, TestedVersion) > 0 {
		log.Printf("[codex] version %s is newer than the tested %s; protocol drift is possible", match, TestedVersion)
	}
	return match, nil
}

// Start launches the process and completes the initialize handshake.
func (a *AppServer) Start(ctx context.Context, onNotify func(string, json.RawMessage)) error {
	version, err := a.DetectVersion(ctx)
	if err != nil {
		return err
	}
	a.version = version
	a.onNotify = onNotify

	// The child is deliberately not tied to a request context: it outlives any
	// single turn, and Shutdown is the only thing that stops it.
	cmd := exec.Command(a.binary, a.args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s app-server: %w", a.binary, err)
	}

	a.mu.Lock()
	a.cmd = cmd
	a.stdin = stdin
	a.running = true
	a.mu.Unlock()

	go a.readLoop(stdout)
	go drainStderr(stderr)
	go a.reap()

	handshakeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	var result initializeResult
	err = a.Call(handshakeCtx, methodInitialize, initializeParams{
		ClientInfo: clientInfo{Name: "streamcore", Title: "StreamCore", Version: "1"},
	}, &result)
	if err != nil {
		a.Shutdown()
		return fmt.Errorf("codex initialize: %w", err)
	}
	a.codexDir = result.CodexHome

	if err := a.Notify(methodInitialized, nil); err != nil {
		a.Shutdown()
		return err
	}
	log.Printf("[codex] app-server %s ready (codex home: %s)", version, result.CodexHome)
	return nil
}

// Running reports whether the child process is alive.
func (a *AppServer) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// Call sends a request and waits for its response.
func (a *AppServer) Call(ctx context.Context, method string, params any, out any) error {
	id := strconv.FormatInt(a.nextID.Add(1), 10)
	reply := make(chan jsonrpcMessage, 1)

	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return ErrNotRunning
	}
	a.pending[id] = reply
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	payload := map[string]any{"jsonrpc": "2.0", "id": json.Number(id), "method": method}
	if params != nil {
		payload["params"] = params
	}
	if err := a.write(payload); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.closed:
		return a.exitError()
	case message := <-reply:
		if message.Error != nil {
			return fmt.Errorf("codex %s: %s", method, message.Error.Message)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(message.Result, out)
	}
}

// Notify sends a request that expects no reply.
func (a *AppServer) Notify(method string, params any) error {
	payload := map[string]any{"method": method}
	if params != nil {
		payload["params"] = params
	}
	return a.write(payload)
}

func (a *AppServer) respond(id json.Number, result any) {
	if err := a.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		log.Printf("[codex] respond to %s: %v", id, err)
	}
}

func (a *AppServer) respondError(id json.Number, code int, message string) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	}
	if err := a.write(payload); err != nil {
		log.Printf("[codex] respond error to %s: %v", id, err)
	}
}

func (a *AppServer) write(payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running || a.stdin == nil {
		return ErrNotRunning
	}
	if _, err := a.stdin.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write to codex app-server: %w", err)
	}
	return nil
}

// readLoop consumes one JSON message per line, routing responses to their
// waiter, server requests to the approval policy, and notifications to the
// manager.
func (a *AppServer) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 128*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var message jsonrpcMessage
		if err := json.Unmarshal(line, &message); err != nil {
			log.Printf("[codex] unreadable message: %v", err)
			continue
		}

		switch {
		case message.ID != nil && message.Method != "":
			a.handleServerRequest(*message.ID, message.Method, message.Params)
		case message.ID != nil:
			a.mu.Lock()
			waiter, ok := a.pending[message.ID.String()]
			a.mu.Unlock()
			if ok {
				waiter <- message
			}
		case message.Method != "":
			if a.onNotify != nil {
				a.onNotify(message.Method, message.Params)
			}
		}
	}
}

// handleServerRequest answers everything Codex asks of StreamCore by policy.
//
// Approvals are declined without exception. A StreamCore confirmation
// authorises edits and test commands inside one assigned worktree, and the
// sandbox already grants exactly that — so an approval request means Codex
// wants something outside it: a wider filesystem, the network, a command the
// sandbox refused. None of those are things a spoken "yes" agreed to.
func (a *AppServer) handleServerRequest(id json.Number, method string, params json.RawMessage) {
	switch method {
	case requestCommandApproval, requestFileChangeApproval,
		requestExecCommandApproval, requestApplyPatchApproval:
		log.Printf("[codex] declined %s — outside the sandbox this task was granted", method)
		a.respond(id, approvalResponse{Decision: approvalDecline})

	case requestPermissionsApproval:
		// The permissions response has no decline verb; granting nothing is
		// the refusal.
		log.Printf("[codex] declined %s — no additional permissions are granted to a developer task", method)
		a.respond(id, map[string]any{"permissions": map[string]any{}})

	case requestAuthTokenRefresh:
		// Codex owns its credential state. Supplying tokens here would make
		// StreamCore a holder of ChatGPT credentials, which this integration
		// deliberately is not.
		log.Printf("[codex] declined %s — Codex owns its own ChatGPT credentials", method)
		a.respondError(id, -32601, "streamcore does not hold ChatGPT credentials; codex manages its own auth state")

	default:
		a.respondError(id, -32601, "streamcore does not implement "+method)
	}
}

func drainStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 8*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Codex logs at INFO on stderr; only surface real problems, and never
		// echo a line that could carry a credential.
		if strings.Contains(line, "ERROR") && !strings.Contains(line, "auth.json") {
			log.Printf("[codex] %s", truncate(line, 300))
		}
	}
}

// reap waits for the child and wakes every in-flight caller when it dies.
func (a *AppServer) reap() {
	a.mu.Lock()
	cmd := a.cmd
	a.mu.Unlock()
	if cmd == nil {
		return
	}
	err := cmd.Wait()

	a.mu.Lock()
	a.running = false
	if a.exitErr == nil && err != nil {
		a.exitErr = err
	}
	a.mu.Unlock()

	select {
	case <-a.closed:
	default:
		close(a.closed)
	}
}

func (a *AppServer) exitError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.exitErr != nil {
		return fmt.Errorf("codex app server exited: %w", a.exitErr)
	}
	return ErrNotRunning
}

// Shutdown closes stdin and gives Codex a moment to exit on its own before
// killing it. Called from the server's shutdown path so no child is left behind.
func (a *AppServer) Shutdown() {
	a.mu.Lock()
	stdin := a.stdin
	cmd := a.cmd
	a.running = false
	a.stdin = nil
	a.mu.Unlock()

	if stdin != nil {
		stdin.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return
	}

	select {
	case <-a.closed:
		return
	case <-time.After(2 * time.Second):
		log.Printf("[codex] app-server did not exit after stdin closed; terminating")
		cmd.Process.Kill()
		<-a.closed
	}
}

// compareVersions orders two dotted versions. Non-numeric suffixes are ignored;
// this only has to separate a supported range from an unsupported one.
func compareVersions(left, right string) int {
	lp := versionParts(left)
	rp := versionParts(right)
	for i := 0; i < 3; i++ {
		if lp[i] != rp[i] {
			if lp[i] < rp[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(version string) [3]int {
	var out [3]int
	match := versionPattern.FindStringSubmatch(version)
	if match == nil {
		return out
	}
	for i := 0; i < 3; i++ {
		out[i], _ = strconv.Atoi(match[i+1])
	}
	return out
}

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
