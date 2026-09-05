package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"developer/internal/agent"
)

// analyzeTimeout and fixTimeout bound the tool call, not the Codex turn — the
// turn has its own deadline in the manager. These exist so a wedged App Server
// cannot hold a tool-call goroutine open indefinitely.
const (
	analyzeTimeout = 15 * time.Minute
	quickTimeout   = 30 * time.Second
)

type tool struct {
	name        string
	description string
	parameters  json.RawMessage
	confirm     bool
	timeout     time.Duration
	prompt      func(json.RawMessage) string
	run         func(ctx context.Context, sessionID string, params json.RawMessage) (any, error)
}

func (t *tool) Name() string                { return t.name }
func (t *tool) Description() string         { return t.description }
func (t *tool) Parameters() json.RawMessage { return t.parameters }
func (t *tool) ConfirmationRequired() bool  { return t.confirm }
func (t *tool) ThinkingSound() bool         { return t.timeout > quickTimeout }

func (t *tool) ConfirmationPrompt(params json.RawMessage) string {
	if t.prompt == nil {
		return ""
	}
	return t.prompt(params)
}

func (t *tool) Execute(params json.RawMessage) (string, error) {
	return t.ExecuteInSession(context.Background(), "", params)
}

func (t *tool) ExecuteInSession(ctx context.Context, sessionID string, params json.RawMessage) (string, error) {
	// A developer task belongs to one conversation. Without a session there is
	// nothing to key it to, and two callers would end up sharing a worktree.
	if sessionID == "" && t.name != "codex.status" {
		return encodeJSON(map[string]any{"error": "this Codex tool needs a conversation to attach to"}), nil
	}

	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	result, err := t.run(ctx, sessionID, params)
	if err != nil {
		return encodeJSON(map[string]any{"error": err.Error()}), nil
	}
	return encodeJSON(result), nil
}

func encodeJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(encoded)
}

// Tools is the deliberately small surface the voice model sees. There is no
// shell tool and no path parameter: Codex runs its own tools inside the
// worktree StreamCore assigned it, and nothing outside.
func (m *Manager) Tools() []agent.Tool {
	tools := []*tool{
		{
			name: "codex.status",
			description: "Check whether the Codex developer agent is available, signed in, and idle. " +
				"Use this before promising the user that Codex can look at something.",
			parameters: json.RawMessage(`{"type":"object","properties":{}}`),
			timeout:    quickTimeout,
			run: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
				return m.Status(ctx), nil
			},
		},
		{
			name: "codex.analyze",
			description: "Have Codex investigate a problem in a repository and explain the root cause. " +
				"Read-only: it will not change any files. Pass the CI failure evidence you already have as context.",
			parameters: json.RawMessage(`{"type":"object","properties":{
				"repository":{"type":"string","description":"Repository as owner/name."},
				"problem":{"type":"string","description":"What is wrong, in one or two sentences."},
				"context":{"type":"string","description":"Evidence: failing test names, log excerpt, error messages."},
				"base_ref":{"type":"string","description":"Branch or commit to investigate. Omit for the default branch."}},
				"required":["repository","problem"]}`),
			timeout: analyzeTimeout,
			run: func(ctx context.Context, sessionID string, params json.RawMessage) (any, error) {
				var args struct {
					Repository string `json:"repository"`
					Problem    string `json:"problem"`
					Context    string `json:"context"`
					BaseRef    string `json:"base_ref"`
				}
				if err := json.Unmarshal(params, &args); err != nil {
					return nil, err
				}
				return m.Analyze(ctx, sessionID, AnalyzeRequest{
					Repository: args.Repository,
					Problem:    args.Problem,
					Context:    args.Context,
					BaseRef:    args.BaseRef,
				})
			},
		},
		{
			name: "codex.fix",
			description: "Have Codex change the code in its isolated worktree and run the tests. " +
				"This modifies files, so it needs the user's confirmation first. Nothing is pushed and no pull request is opened.",
			parameters: json.RawMessage(`{"type":"object","properties":{
				"repository":{"type":"string","description":"Repository as owner/name."},
				"instruction":{"type":"string","description":"What to fix, in one or two sentences."},
				"test_command":{"type":"string","description":"Test command to run afterwards, if you know it."}},
				"required":["repository","instruction"]}`),
			confirm: true,
			timeout: analyzeTimeout,
			prompt: func(params json.RawMessage) string {
				var args struct {
					Instruction string `json:"instruction"`
				}
				json.Unmarshal(params, &args)
				if args.Instruction == "" {
					return "Codex will edit the isolated worktree and run the tests. Shall I do that?"
				}
				return fmt.Sprintf("Codex will %s in an isolated copy of the repository and run the tests. Shall I do that?",
					lowerFirst(args.Instruction))
			},
			run: func(ctx context.Context, sessionID string, params json.RawMessage) (any, error) {
				var args struct {
					Repository  string `json:"repository"`
					Instruction string `json:"instruction"`
					TestCommand string `json:"test_command"`
				}
				if err := json.Unmarshal(params, &args); err != nil {
					return nil, err
				}
				return m.Fix(ctx, sessionID, FixRequest{
					Repository:  args.Repository,
					Instruction: args.Instruction,
					TestCommand: args.TestCommand,
				})
			},
		},
		{
			name:        "codex.test",
			description: "Re-run the tests for the change Codex has already made in this conversation.",
			parameters: json.RawMessage(`{"type":"object","properties":{
				"command":{"type":"string","description":"Test command. Omit to let Codex choose."}}}`),
			timeout: analyzeTimeout,
			run: func(ctx context.Context, sessionID string, params json.RawMessage) (any, error) {
				var args struct {
					Command string `json:"command"`
				}
				json.Unmarshal(params, &args)
				return m.Test(ctx, sessionID, args.Command)
			},
		},
		{
			name:        "codex.diff",
			description: "Show the change Codex has made in this conversation's isolated worktree.",
			parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			timeout:     quickTimeout,
			run: func(ctx context.Context, sessionID string, _ json.RawMessage) (any, error) {
				return m.Diff(ctx, sessionID)
			},
		},
		{
			name:        "codex.cancel",
			description: "Stop the Codex turn that is currently running. Use this only when the user explicitly asks to cancel the developer task.",
			parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			timeout:     quickTimeout,
			run: func(ctx context.Context, sessionID string, _ json.RawMessage) (any, error) {
				return m.Cancel(ctx, sessionID)
			},
		},
	}

	out := make([]agent.Tool, 0, len(tools))
	for _, t := range tools {
		out = append(out, t)
	}
	return out
}

func lowerFirst(text string) string {
	if text == "" {
		return text
	}
	runes := []rune(text)
	// Only fold an ordinary capitalised word; an identifier like "CI" or a
	// filename keeps its case.
	if len(runes) > 1 && runes[1] >= 'a' && runes[1] <= 'z' {
		if runes[0] >= 'A' && runes[0] <= 'Z' {
			runes[0] = runes[0] - 'A' + 'a'
		}
	}
	return string(runes)
}
