package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// DefaultExecuteTimeout bounds one execute call when the manifest does not say.
// A developer agent that thinks for minutes sets its own; the default stays
// short because a hanging tool stalls a live conversation.
const DefaultExecuteTimeout = 30 * time.Second

// stopGrace is how long a plugin gets at each stage of shutdown: first to exit
// on its own after stdin closes, then to honour a SIGTERM. A plugin that
// supervises children of its own — a language server, a harness — needs the
// chance to take them with it.
//
// Two seconds each because the server's own force-exit net is five away, and
// plugins are stopped concurrently so the worst case is one plugin's four, not
// every plugin's.
const stopGrace = 2 * time.Second

// ExternalPlugin is one plugin subprocess. It hosts every tool its manifest
// declares, so a family of related tools costs one process rather than one
// each.
//
// The stdio channel is bidirectional. The server issues requests and matches
// replies by id, and the plugin may at any time send a request or notification
// of its own — to push a packet at a device, call another plugin's tool, or ask
// the server's model a question. Anything carrying "method" is inbound from the
// plugin; anything else is a reply, which is what makes the two directions
// share one pipe unambiguously.
type ExternalPlugin struct {
	manifest  Manifest
	dir       string
	sdkDir    string
	timeout   time.Duration
	callbacks Callbacks

	writeMu   sync.Mutex
	stdin     *json.Encoder
	stdinPipe io.Closer

	cmd      *exec.Cmd
	running  atomic.Bool
	stopping atomic.Bool
	nextID   atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan JSONRPCResponse

	// tools is what this plugin advertises. It starts as the manifest's list
	// and is replaced if the plugin returns its own from initialize.
	toolsMu sync.RWMutex
	tools   []ToolSpec
}

// NewExternalPlugin creates a plugin from a manifest and its directory path.
func NewExternalPlugin(m Manifest, dir string, sdkDir string) *ExternalPlugin {
	return &ExternalPlugin{
		manifest: m,
		dir:      dir,
		sdkDir:   sdkDir,
		timeout:  m.Timeout(),
		pending:  make(map[int64]chan JSONRPCResponse),
		tools:    m.Tools,
	}
}

// SetCallbacks installs what this plugin may ask the server to do. It must be
// called before Start, since a plugin can call back the moment it initializes.
func (p *ExternalPlugin) SetCallbacks(c Callbacks) { p.callbacks = c }

// Manifest returns the manifest this plugin was loaded from.
func (p *ExternalPlugin) Manifest() Manifest { return p.manifest }

// ToolSpecs returns the tools this plugin currently advertises.
func (p *ExternalPlugin) ToolSpecs() []ToolSpec {
	p.toolsMu.RLock()
	defer p.toolsMu.RUnlock()
	return append([]ToolSpec(nil), p.tools...)
}

// Events reports the lifecycle events the manifest subscribed to, making a
// subscribing plugin an EventHandler without any Go on the plugin's side.
func (p *ExternalPlugin) Events() []string { return p.manifest.Events }

// Start builds the plugin if it asked to be built, launches it, and completes
// the initialize handshake. The process stays alive for the lifetime of the
// server to avoid per-call startup latency.
func (p *ExternalPlugin) Start(ctx context.Context) error {
	p.stopping.Store(false)

	if err := p.runBuild(ctx); err != nil {
		return err
	}

	argv := p.manifest.Exec
	if len(argv) == 0 {
		return fmt.Errorf("plugin %s: no exec command", p.manifest.Name)
	}

	env, err := p.environment()
	if err != nil {
		return err
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = p.dir
	cmd.Env = env

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("plugin %s: stdin pipe: %w", p.manifest.Name, err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("plugin %s: stdout pipe: %w", p.manifest.Name, err)
	}
	cmd.Stderr = &logWriter{prefix: fmt.Sprintf("[plugin:%s]", p.manifest.Name)}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("plugin %s: start: %w", p.manifest.Name, err)
	}

	p.cmd = cmd
	p.stdin = json.NewEncoder(stdinPipe)
	p.stdinPipe = stdinPipe
	p.running.Store(true)

	go p.readLoop(stdoutPipe)

	if err := p.initialize(ctx); err != nil {
		p.Stop()
		return err
	}
	return nil
}

// runBuild compiles the plugin or installs its dependencies. It is what lets a
// compiled language sit in the same folder as an interpreted one: Go needs a
// build step, and so, it turns out, does every TypeScript plugin that would
// otherwise have to commit node_modules.
func (p *ExternalPlugin) runBuild(ctx context.Context) error {
	if len(p.manifest.Build) == 0 {
		return nil
	}
	log.Printf("[plugin:%s] building: %v", p.manifest.Name, p.manifest.Build)

	env, err := p.environment()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, p.manifest.Build[0], p.manifest.Build[1:]...)
	cmd.Dir = p.dir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("plugin %s: build failed: %w\n%s", p.manifest.Name, err, output)
	}
	return nil
}

// initialize completes the handshake and adopts any tool list the plugin
// returns. A plugin that answers with a plain string — every plugin written
// before this existed — simply keeps the tools its manifest declared.
func (p *ExternalPlugin) initialize(ctx context.Context) error {
	params, err := json.Marshal(initializeParams{
		Plugin:   p.manifest.Name,
		Config:   p.manifest.config,
		Protocol: ProtocolVersion,
	})
	if err != nil {
		return fmt.Errorf("plugin %s: encode initialize: %w", p.manifest.Name, err)
	}

	// The handshake gets its own bound: a plugin that never answers must not
	// hold up the rest of the server's startup indefinitely.
	initCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	raw, err := p.call(initCtx, JSONRPCRequest{Method: "initialize", Params: params})
	if err != nil {
		return fmt.Errorf("plugin %s: initialize: %w", p.manifest.Name, err)
	}

	var declared struct {
		Tools []ToolSpec `json:"tools"`
	}
	if err := json.Unmarshal(raw, &declared); err == nil && len(declared.Tools) > 0 {
		p.toolsMu.Lock()
		p.tools = declared.Tools
		p.toolsMu.Unlock()
		log.Printf("[plugin:%s] declared %d tools at initialize", p.manifest.Name, len(declared.Tools))
	}

	log.Printf("[plugin:%s] initialized", p.manifest.Name)
	return nil
}

// Execute runs one tool. The tool name and session travel as top-level request
// fields rather than inside params, so a plugin written against the original
// single-tool protocol — which reads only method, params and id — sees exactly
// the arguments it always did.
func (p *ExternalPlugin) Execute(ctx context.Context, tool, sessionID string, params json.RawMessage) (string, error) {
	if !p.running.Load() {
		return "", fmt.Errorf("plugin %s is not running", p.manifest.Name)
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	raw, err := p.call(callCtx, JSONRPCRequest{
		Method:    "execute",
		Params:    params,
		Tool:      tool,
		SessionID: sessionID,
	})
	if err != nil {
		return "", err
	}
	return resultText(raw), nil
}

// HandleEvent delivers a lifecycle event and turns whatever the plugin sends
// back into outbound events for the session's data channel.
func (p *ExternalPlugin) HandleEvent(ctx context.Context, event PluginEvent) ([]OutboundEvent, error) {
	if !p.running.Load() {
		return nil, nil
	}

	payload, err := json.Marshal(eventParams{
		Type:      event.Type,
		SessionID: event.SessionID,
		TurnID:    event.TurnID,
		TurnSeq:   event.TurnSeq,
		Data:      event.Data,
	})
	if err != nil {
		return nil, fmt.Errorf("plugin %s: encode event: %w", p.manifest.Name, err)
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	raw, err := p.call(callCtx, JSONRPCRequest{
		Method:    "event",
		Params:    payload,
		SessionID: event.SessionID,
	})
	if err != nil {
		return nil, err
	}

	result := ParseResult(resultText(raw))
	outbound := make([]OutboundEvent, 0, len(result.Emit))
	for _, emission := range result.Emit {
		outbound = append(outbound, OutboundEvent{
			Type:    emission.Topic,
			TurnID:  event.TurnID,
			TurnSeq: event.TurnSeq,
			Payload: json.RawMessage(emission.Payload),
		})
	}
	return outbound, nil
}

// call sends a request and waits for the matching reply.
func (p *ExternalPlugin) call(ctx context.Context, req JSONRPCRequest) (json.RawMessage, error) {
	req.JSONRPC = "2.0"
	req.ID = p.nextID.Add(1)

	reply := make(chan JSONRPCResponse, 1)
	p.pendingMu.Lock()
	p.pending[req.ID] = reply
	p.pendingMu.Unlock()
	defer func() {
		p.pendingMu.Lock()
		delete(p.pending, req.ID)
		p.pendingMu.Unlock()
	}()

	if err := p.write(req); err != nil {
		return nil, err
	}

	select {
	case resp := <-reply:
		if resp.Error != nil {
			return nil, fmt.Errorf("plugin %s: %s", p.manifest.Name, resp.Error.Message)
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("plugin %s: %s timed out after %s", p.manifest.Name, req.Method, p.timeout)
	}
}

func (p *ExternalPlugin) write(v any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.stdin == nil {
		return fmt.Errorf("plugin %s: not started", p.manifest.Name)
	}
	if err := p.stdin.Encode(v); err != nil {
		return fmt.Errorf("plugin %s: send: %w", p.manifest.Name, err)
	}
	return nil
}

// readLoop demultiplexes everything the plugin writes. It is the only reader of
// stdout, which is what allows several sessions to have calls in flight at once
// instead of serialising behind a shared lock.
func (p *ExternalPlugin) readLoop(stdout io.ReadCloser) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		message := make([]byte, len(line))
		copy(message, line)

		var probe struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(message, &probe); err != nil {
			log.Printf("[plugin:%s] unparseable output: %s", p.manifest.Name, message)
			continue
		}
		if probe.Method != "" {
			go p.handleInbound(message)
			continue
		}

		var resp JSONRPCResponse
		if err := json.Unmarshal(message, &resp); err != nil {
			log.Printf("[plugin:%s] unparseable response: %s", p.manifest.Name, message)
			continue
		}
		p.pendingMu.Lock()
		reply, ok := p.pending[resp.ID]
		p.pendingMu.Unlock()
		if !ok {
			log.Printf("[plugin:%s] reply to unknown request id %d", p.manifest.Name, resp.ID)
			continue
		}
		reply <- resp
	}

	// stdout closed: the process is gone. Fail every waiter rather than
	// leaving them to time out one by one.
	p.running.Store(false)
	p.pendingMu.Lock()
	for id, reply := range p.pending {
		reply <- JSONRPCResponse{ID: id, Error: &JSONRPCError{Code: -32000, Message: "plugin exited"}}
	}
	p.pending = make(map[int64]chan JSONRPCResponse)
	p.pendingMu.Unlock()

	if !p.stopping.Load() {
		go p.restart()
	}
}

// restartAttempts and restartBackoff bound recovery. A plugin that dies once is
// worth restarting; one that dies every time it starts is broken, and retrying
// it forever would bury the reason in a scrolling log.
const (
	restartAttempts    = 5
	restartBackoff     = time.Second
	restartBackoffCeil = 30 * time.Second
)

// restart brings a crashed plugin back.
//
// The tools registered for it hold this host, not the process, so a successful
// restart makes them work again without the manager knowing anything happened.
func (p *ExternalPlugin) restart() {
	backoff := restartBackoff

	for attempt := 1; attempt <= restartAttempts; attempt++ {
		if p.stopping.Load() {
			return
		}
		time.Sleep(backoff)
		if p.stopping.Load() {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
		err := p.Start(ctx)
		cancel()
		if err == nil {
			// Start clears the stopping flag, so a shutdown that landed while
			// it was in flight has to be honoured now rather than leaving a
			// process behind that nothing owns.
			if p.stopping.Load() {
				p.Stop()
				return
			}
			log.Printf("[plugin:%s] restarted after exiting (attempt %d)", p.manifest.Name, attempt)
			return
		}

		log.Printf("[plugin:%s] restart %d/%d failed: %v", p.manifest.Name, attempt, restartAttempts, err)
		if backoff *= 2; backoff > restartBackoffCeil {
			backoff = restartBackoffCeil
		}
	}

	log.Printf("[plugin:%s] gave up restarting; its tools will fail until the server restarts", p.manifest.Name)
}

// environment builds the subprocess environment, adding the SDK paths when a
// developer has pointed PLUGIN_SDK_DIR at a checkout.
func (p *ExternalPlugin) environment() ([]string, error) {
	env := os.Environ()
	if p.sdkDir == "" {
		return env, nil
	}
	abs, err := filepath.Abs(p.sdkDir)
	if err != nil {
		return nil, fmt.Errorf("plugin %s: resolve sdk dir: %w", p.manifest.Name, err)
	}
	env = append(env,
		"PYTHONPATH="+prependPath(os.Getenv("PYTHONPATH"), filepath.Join(abs, "python")),
		"NODE_PATH="+prependPath(os.Getenv("NODE_PATH"), filepath.Join(abs, "typescript", "dist")),
	)
	return env, nil
}

func prependPath(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + string(os.PathListSeparator) + add
}

// Stop shuts the plugin down, giving it a chance to exit cleanly. Closing stdin
// is what both SDK loops treat as "we are done"; the signals are for anything
// that ignores it.
func (p *ExternalPlugin) Stop() {
	// Set before anything else: a readLoop that notices the pipe closing must
	// see a deliberate shutdown rather than a crash worth recovering from.
	p.stopping.Store(true)

	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	p.running.Store(false)

	p.writeMu.Lock()
	if p.stdinPipe != nil {
		p.stdinPipe.Close()
	}
	p.writeMu.Unlock()

	exited := make(chan struct{})
	go func() {
		p.cmd.Wait()
		close(exited)
	}()

	select {
	case <-exited:
		return
	case <-time.After(stopGrace):
	}

	p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
		return
	case <-time.After(stopGrace):
	}

	p.cmd.Process.Kill()
	<-exited
}

// resultText renders a JSON-RPC result as the string a tool returned. A result
// encoded as a JSON string is that string; anything else — an envelope object,
// most often — is passed through as raw JSON for ParseResult to interpret.
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

// logWriter writes plugin stderr output to the server log.
type logWriter struct {
	prefix string
}

func (w *logWriter) Write(p []byte) (int, error) {
	log.Printf("%s %s", w.prefix, string(p))
	return len(p), nil
}

var errNoCallback = errors.New("not available")
