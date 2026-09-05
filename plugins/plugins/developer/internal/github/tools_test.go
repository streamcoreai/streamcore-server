package github

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"developer/internal/agent"
)

type stubTasks struct {
	change TaskChange
	err    error
}

func (s stubTasks) PullRequestSource(string, string) (TaskChange, error) {
	return s.change, s.err
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	return &Service{
		client: NewClient(&stubTokens{}, []string{"streamcoreai/streamcore-server"}, ""),
		git:    newGitRunner(),
	}
}

func TestReadToolsAreNotGated(t *testing.T) {
	service := newTestService(t)

	tools := map[string]agent.Tool{}
	for _, tool := range service.Tools(nil) {
		tools[tool.Name()] = tool
	}
	for _, want := range []string{
		"github.repo_status", "github.latest_ci_failure", "github.workflow_run",
		"github.workflow_logs", "github.pull_request", "github.commit",
		"github.file", "github.diff",
	} {
		tool, ok := tools[want]
		if !ok {
			t.Fatalf("%s is missing", want)
		}
		if tool.ConfirmationRequired() {
			t.Fatalf("%s asks for confirmation; reading is not a mutation", want)
		}
	}
	if _, present := tools["github.create_pull_request"]; present {
		t.Fatal("the pull request tool appeared with no developer task to publish")
	}
}

func TestPullRequestToolIsGated(t *testing.T) {
	service := newTestService(t)
	tools := service.Tools(stubTasks{change: TaskChange{WorktreePath: "/tmp/x", TestsRun: true}})

	var pr agent.Tool
	for _, tool := range tools {
		if tool.Name() == "github.create_pull_request" {
			pr = tool
		}
	}
	if pr == nil {
		t.Fatal("the pull request tool is missing")
	}
	if !pr.ConfirmationRequired() {
		t.Fatal("pull request creation is not confirmation gated")
	}

	// The gate itself lives in the server. What has to be true here is that
	// the tool asks for it, and names the repository it is about to push to.
	prompter, ok := pr.(agent.ConfirmationPrompter)
	if !ok {
		t.Fatal("the pull request tool cannot describe what it is about to do")
	}
	prompt := prompter.ConfirmationPrompt(json.RawMessage(`{"repository":"streamcoreai/streamcore-server"}`))
	if !strings.Contains(prompt, "streamcoreai/streamcore-server") {
		t.Fatalf("the prompt does not name the repository: %q", prompt)
	}
}

// A tool that fails should say so conversationally. A hard error aborts the
// whole voice turn, which is a much worse outcome than "I couldn't reach that".
func TestToolErrorsAreConversational(t *testing.T) {
	service := newTestService(t)
	var status agent.Tool
	for _, tool := range service.Tools(nil) {
		if tool.Name() == "github.repo_status" {
			status = tool
		}
	}

	out, err := status.Execute(json.RawMessage(`{"repository":"attacker/evil"}`))
	if err != nil {
		t.Fatalf("a disallowed repository aborted the turn: %v", err)
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("tool output is not JSON: %s", out)
	}
	if !strings.Contains(payload.Error, "allowlist") {
		t.Fatalf("the model is not told why: %q", payload.Error)
	}
}

// The allowlist is in every schema, so the model asks for a repository the
// server will actually accept.
func TestSchemasNameTheAllowedRepositories(t *testing.T) {
	service := newTestService(t)
	for _, tool := range service.Tools(nil) {
		schema := string(tool.Parameters())
		if !strings.Contains(schema, "streamcoreai/streamcore-server") {
			t.Fatalf("%s does not tell the model which repositories are allowed: %s", tool.Name(), schema)
		}
		if !json.Valid([]byte(schema)) {
			t.Fatalf("%s has an invalid parameter schema: %s", tool.Name(), schema)
		}
	}
}

func TestPullRequestToolRefusesUnverifiedChange(t *testing.T) {
	service := newTestService(t)
	tools := service.Tools(stubTasks{err: fmt.Errorf("no Codex task is open in this conversation")})

	for _, tool := range tools {
		if tool.Name() != "github.create_pull_request" {
			continue
		}
		out, err := tool.Execute(json.RawMessage(`{"repository":"streamcoreai/streamcore-server"}`))
		if err != nil {
			t.Fatalf("hard error: %v", err)
		}
		if !strings.Contains(out, "no Codex task") {
			t.Fatalf("unexpected result: %s", out)
		}
	}
}
