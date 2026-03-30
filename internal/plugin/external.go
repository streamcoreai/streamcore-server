package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// ExternalPlugin is a subprocess-based plugin that communicates via JSON-RPC
// over stdio. It supports Python, TypeScript, and JavaScript runtimes.
type ExternalPlugin struct {
	manifest Manifest
	dir      string
	sdkDir   string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   *json.Encoder
	stdout  *bufio.Scanner
	running atomic.Bool
	nextID  atomic.Int64
}

// NewExternalPlugin creates a plugin from a manifest and its directory path.
func NewExternalPlugin(m Manifest, dir string, sdkDir string) *ExternalPlugin {
	return &ExternalPlugin{
		manifest: m,
		dir:      dir,
		sdkDir:   sdkDir,
	}
}

func (p *ExternalPlugin) Name() string                { return p.manifest.Name }
func (p *ExternalPlugin) Description() string         { return p.manifest.Description }
func (p *ExternalPlugin) Parameters() json.RawMessage { return p.manifest.Parameters() }
func (p *ExternalPlugin) ConfirmationRequired() bool  { return p.manifest.ConfirmationRequired }

// Start launches the plugin subprocess. The process stays alive for the
// lifetime of the server to avoid per-call startup latency.
func (p *ExternalPlugin) Start(ctx context.Context) error {
	cmdName, args, err := p.buildCommand()
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	cmd := exec.CommandContext(ctx, cmdName, args...)
	cmd.Dir = p.dir

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("plugin %s: stdin pipe: %w", p.manifest.Name, err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("plugin %s: stdout pipe: %w", p.manifest.Name, err)
	}

	// stderr goes to server logs
	cmd.Stderr = &logWriter{prefix: fmt.Sprintf("[plugin:%s]", p.manifest.Name)}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("plugin %s: start: %w", p.manifest.Name, err)
	}

	p.cmd = cmd
	p.stdin = json.NewEncoder(stdinPipe)
	p.stdout = bufio.NewScanner(stdoutPipe)
	p.stdout.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max line
	p.running.Store(true)

	// Init handshake — send initialize and wait for a response.
	// If the process crashes (e.g. import error), stdout closes and Scan returns false.
	initReq := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		ID:      int(p.nextID.Add(1)),
	}
	if err := p.stdin.Encode(initReq); err != nil {
		p.running.Store(false)
		cmd.Process.Kill()
		cmd.Wait()
		return fmt.Errorf("plugin %s: init send: %w", p.manifest.Name, err)
	}
	if !p.stdout.Scan() {
		p.running.Store(false)
		waitErr := cmd.Wait()
		return fmt.Errorf("plugin %s: process exited during init: %v", p.manifest.Name, waitErr)
	}
	log.Printf("[plugin:%s] initialized", p.manifest.Name)

	return nil
}

// Execute sends a JSON-RPC execute request and returns the result.
func (p *ExternalPlugin) Execute(params json.RawMessage) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running.Load() {
		return "", fmt.Errorf("plugin %s is not running", p.manifest.Name)
	}

	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "execute",
		Params:  params,
		ID:      int(p.nextID.Add(1)),
	}

	if err := p.stdin.Encode(req); err != nil {
		return "", fmt.Errorf("plugin %s: send: %w", p.manifest.Name, err)
	}

	// Read response with timeout
	done := make(chan struct{})
	var resp JSONRPCResponse
	var scanErr error

	go func() {
		defer close(done)
		if p.stdout.Scan() {
			scanErr = json.Unmarshal(p.stdout.Bytes(), &resp)
		} else {
			scanErr = fmt.Errorf("plugin %s: stdout closed", p.manifest.Name)
		}
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		return "", fmt.Errorf("plugin %s: execute timeout (30s)", p.manifest.Name)
	}

	if scanErr != nil {
		return "", fmt.Errorf("plugin %s: parse response: %w", p.manifest.Name, scanErr)
	}

	if resp.Error != nil {
		return "", fmt.Errorf("plugin %s: %s", p.manifest.Name, resp.Error.Message)
	}

	return resp.Result, nil
}

// Stop terminates the plugin subprocess.
func (p *ExternalPlugin) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.running.Store(false)
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
		p.cmd.Wait()
	}
}

// buildCommand returns the command and args for the plugin's language runtime.
func (p *ExternalPlugin) buildCommand() (string, []string, error) {
	if p.sdkDir != "" {
		p.injectSDKPaths()
	}

	switch p.manifest.Language {
	case "python":
		return "python3", []string{p.manifest.Entrypoint}, nil
	case "typescript":
		return "npx", []string{"tsx", p.manifest.Entrypoint}, nil
	case "javascript":
		return "node", []string{p.manifest.Entrypoint}, nil
	default:
		return "", nil, fmt.Errorf("unsupported plugin language: %s", p.manifest.Language)
	}
}

// injectSDKPaths adds the plugin SDK directories to PYTHONPATH and NODE_PATH
// so plugin subprocesses can import the SDK helpers.
func (p *ExternalPlugin) injectSDKPaths() {
	pythonPath := p.sdkDir + "/python"
	if existing := os.Getenv("PYTHONPATH"); existing != "" {
		pythonPath = existing + ":" + pythonPath
	}
	os.Setenv("PYTHONPATH", pythonPath)

	nodePath := p.sdkDir + "/typescript/dist"
	if existing := os.Getenv("NODE_PATH"); existing != "" {
		nodePath = existing + ":" + nodePath
	}
	os.Setenv("NODE_PATH", nodePath)
}

// logWriter writes plugin stderr output to the server log.
type logWriter struct {
	prefix string
}

func (w *logWriter) Write(p []byte) (int, error) {
	log.Printf("%s %s", w.prefix, string(p))
	return len(p), nil
}
