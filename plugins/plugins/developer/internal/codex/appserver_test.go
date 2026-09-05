package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The tests run against a fake App Server rather than the real Codex: they must
// pass on a machine with no Codex installed and no ChatGPT subscription.
//
// The fake is this test binary re-executed with an environment marker, which is
// the cheapest way to get a real child process on the other end of a real pipe —
// the framing and the process lifecycle are half of what is being tested.
const (
	fakeMarker  = "STREAMCORE_FAKE_CODEX"
	fakeAccount = "STREAMCORE_FAKE_CODEX_ACCOUNT"
	fakeTurn    = "STREAMCORE_FAKE_CODEX_TURN"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeMarker) != "" {
		runFakeCodex()
		return
	}
	os.Exit(m.Run())
}

func runFakeCodex() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--version" {
		fmt.Println("codex-cli " + TestedVersion)
		os.Exit(0)
	}

	out := json.NewEncoder(os.Stdout)
	send := func(payload map[string]any) { out.Encode(payload) }

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var message struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			continue
		}
		id := any(nil)
		if message.ID != nil {
			json.Unmarshal(*message.ID, &id)
		}

		switch message.Method {
		case "initialize":
			send(map[string]any{"id": id, "result": map[string]any{
				"userAgent": "fake", "codexHome": "/tmp/fake-codex",
				"platformOs": "test", "platformFamily": "unix",
			}})
		case "initialized":
		case "account/read":
			send(map[string]any{"id": id, "result": fakeAccountPayload()})
		case "thread/start":
			send(map[string]any{"id": id, "result": map[string]any{
				"thread": map[string]any{"id": "thread-1"}, "model": "fake", "modelProvider": "openai", "cwd": "/tmp",
			}})
		case "turn/start":
			send(map[string]any{"id": id, "result": map[string]any{
				"turn": map[string]any{"id": "turn-1", "status": "inProgress"},
			}})
			if os.Getenv(fakeTurn) == "hang" {
				continue
			}
			emitFakeTurn(send)
		case "turn/interrupt":
			send(map[string]any{"id": id, "result": map[string]any{}})
			send(map[string]any{"method": "turn/completed", "params": map[string]any{
				"threadId": "thread-1",
				"turn":     map[string]any{"id": "turn-1", "status": "interrupted"},
			}})
		default:
			send(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": "unknown"}})
		}
	}
	os.Exit(0)
}

func fakeAccountPayload() map[string]any {
	switch os.Getenv(fakeAccount) {
	case "apikey":
		return map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": true}
	case "none":
		return map[string]any{"account": nil, "requiresOpenaiAuth": false}
	default:
		return map[string]any{
			"account":            map[string]any{"type": "chatgpt", "email": "dev@example.com", "planType": "plus"},
			"requiresOpenaiAuth": true,
		}
	}
}

func emitFakeTurn(send func(map[string]any)) {
	item := func(itemType string, extra map[string]any) map[string]any {
		payload := map[string]any{"id": "i1", "type": itemType}
		for key, value := range extra {
			payload[key] = value
		}
		return map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": payload}
	}

	// Private reasoning: the manager must drop this on the floor.
	send(map[string]any{"method": "item/completed", "params": item("reasoning", map[string]any{
		"summary": []string{"the user probably wants me to look at the test"},
	})})
	send(map[string]any{"method": "item/completed", "params": item("fileChange", map[string]any{
		"status":  "completed",
		"changes": []map[string]any{{"path": "internal/displayprojector/projector.go"}},
	})})
	send(map[string]any{"method": "item/completed", "params": item("commandExecution", map[string]any{
		"command": "go test ./internal/displayprojector/", "exitCode": 0, "status": "completed",
	})})
	send(map[string]any{"method": "turn/diff/updated", "params": map[string]any{
		"threadId": "thread-1", "turnId": "turn-1", "diff": "--- a\n+++ b\n",
	}})
	send(map[string]any{"method": "item/completed", "params": item("agentMessage", map[string]any{
		"text": "The card version was bumped without updating the test.", "phase": "final_answer",
	})})

	status := map[string]any{"id": "turn-1", "status": "completed"}
	if os.Getenv(fakeTurn) == "fail" {
		status = map[string]any{"id": "turn-1", "status": "failed", "error": map[string]any{
			"message":        `{"type":"error","status":400,"error":{"message":"The 'x' model is not supported when using Codex with a ChatGPT account."}}`,
			"codexErrorInfo": "other",
		}}
	}
	send(map[string]any{"method": "turn/completed", "params": map[string]any{
		"threadId": "thread-1", "turn": status,
	}})
}

// newFakeManager wires a Manager onto the fake App Server.
func newFakeManager(t *testing.T, account, turn string) *Manager {
	t.Helper()
	t.Setenv(fakeMarker, "1")
	t.Setenv(fakeAccount, account)
	t.Setenv(fakeTurn, turn)

	manager, err := New(Options{
		Binary:        os.Args[0],
		ModelProvider: "openai",
		Model:         "fake-model",
		WorkspaceRoot: t.TempDir(),
		TurnTimeout:   5 * time.Second,
	}, func(context.Context, string) (string, string, error) {
		return "", "", fmt.Errorf("the fake manager does not clone")
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

func TestVersionRange(t *testing.T) {
	if compareVersions("0.144.1", "0.144.0") <= 0 {
		t.Fatal("0.144.1 should sort after 0.144.0")
	}
	if compareVersions("0.143.9", MinimumVersion) >= 0 {
		t.Fatal("0.143.9 should sort before the minimum")
	}
	if compareVersions(TestedVersion, TestedVersion) != 0 {
		t.Fatal("a version should equal itself")
	}
}

func TestDetectVersionRejectsOldBinaries(t *testing.T) {
	server := NewAppServer("/bin/echo", "", "", nil)
	if _, err := server.DetectVersion(context.Background()); err == nil {
		t.Fatal("a binary that reports no version was accepted")
	}
}

func TestStartHandshakeAndShutdown(t *testing.T) {
	manager := newFakeManager(t, "chatgpt", "ok")
	if !manager.server.Running() {
		t.Fatal("the app server is not running after Start")
	}
	if manager.server.Version() != TestedVersion {
		t.Fatalf("version is %q", manager.server.Version())
	}
	if manager.server.CodexHome() != "/tmp/fake-codex" {
		t.Fatalf("codex home is %q", manager.server.CodexHome())
	}

	manager.Shutdown()
	if manager.server.Running() {
		t.Fatal("the child process outlived Shutdown")
	}
}

// Every approval is declined. A spoken "fix the test" authorises work inside
// one worktree; anything Codex has to ask about is outside it by definition.
func TestApprovalsAreDeclined(t *testing.T) {
	manager := newFakeManager(t, "chatgpt", "ok")

	for _, method := range []string{
		requestCommandApproval, requestFileChangeApproval,
		requestExecCommandApproval, requestApplyPatchApproval,
	} {
		var response approvalResponse
		captured := captureResponse(t, manager.server, method, &response)
		if !captured {
			t.Fatalf("%s produced no response", method)
		}
		if response.Decision != approvalDecline {
			t.Fatalf("%s was answered %q", method, response.Decision)
		}
	}
}

// StreamCore does not hold ChatGPT credentials, so it cannot answer a request
// for them — and must say so rather than inventing an empty token.
func TestAuthTokenRefreshIsRefused(t *testing.T) {
	manager := newFakeManager(t, "chatgpt", "ok")
	var response approvalResponse
	if captureResponse(t, manager.server, requestAuthTokenRefresh, &response) {
		t.Fatal("StreamCore answered a request for ChatGPT tokens")
	}
}

// captureResponse drives handleServerRequest and reports whether a non-error
// result came back, decoding it into out.
func captureResponse(t *testing.T, server *AppServer, method string, out any) bool {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	server.mu.Lock()
	previous := server.stdin
	server.stdin = writer
	server.mu.Unlock()
	defer func() {
		server.mu.Lock()
		server.stdin = previous
		server.mu.Unlock()
	}()

	server.handleServerRequest(json.Number("9"), method, json.RawMessage(`{}`))
	writer.Close()

	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() {
		t.Fatalf("%s produced nothing", method)
	}
	var message struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(message.Error) > 0 {
		return false
	}
	if out != nil && len(message.Result) > 0 {
		json.Unmarshal(message.Result, out)
	}
	return true
}

func TestStatusReportsChatGPTSignIn(t *testing.T) {
	manager := newFakeManager(t, "chatgpt", "ok")
	status := manager.Status(context.Background())

	if !status.Available || !status.Authenticated {
		t.Fatalf("a signed-in Codex reported %+v", status)
	}
	if status.Plan != "plus" {
		t.Fatalf("plan is %q", status.Plan)
	}
	encoded, _ := json.Marshal(status)
	for _, forbidden := range []string{"dev@example.com", "token", "auth.json", "refresh"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("codex.status leaked %q: %s", forbidden, encoded)
		}
	}
}

// An API key is treated as unauthenticated on purpose. This integration exists
// to spend the operator's ChatGPT plan; quietly billing an API key instead
// would be the wrong kind of working.
func TestAPIKeyIsNotAcceptedAsAFallback(t *testing.T) {
	manager := newFakeManager(t, "apikey", "ok")
	status := manager.Status(context.Background())

	if status.Authenticated {
		t.Fatal("an API-key account was accepted")
	}
	if !strings.Contains(status.Detail, "ChatGPT") {
		t.Fatalf("the message does not explain the requirement: %q", status.Detail)
	}
	if _, err := manager.Analyze(context.Background(), "session-1", AnalyzeRequest{
		Repository: "a/b", Problem: "x",
	}); err == nil {
		t.Fatal("a Codex task ran on API-key billing")
	}
}

func TestUnauthenticatedCodexStillReportsCleanly(t *testing.T) {
	manager := newFakeManager(t, "none", "ok")
	status := manager.Status(context.Background())

	if status.Authenticated {
		t.Fatal("a signed-out Codex reported as authenticated")
	}
	if !status.Available {
		t.Fatal("a running but signed-out Codex should still be available")
	}
	if !strings.Contains(status.Detail, "codex login") {
		t.Fatalf("the operator is not told how to fix it: %q", status.Detail)
	}
}
