package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"developer/internal/agent"
)

// toolTimeout bounds one tool call. CI log downloads are the slow case; the
// voice pipeline never waits on this goroutine, but the model does.
const toolTimeout = 45 * time.Second

// TaskSource supplies the developer task behind a pull request: which isolated
// worktree holds the change, and whether its tests were actually run.
//
// The Codex manager implements it. Declaring it here rather than importing the
// codex package is what keeps the two credentials apart — this package cannot
// reach Codex's auth state even by accident, and there is no import edge for a
// later change to abuse.
type TaskSource interface {
	PullRequestSource(sessionID, repository string) (TaskChange, error)
}

// TaskChange is what a developer task can tell GitHub about itself.
type TaskChange struct {
	WorktreePath string
	Branch       string
	Title        string
	Summary      string
	TestsRun     bool
}

// tool adapts a Go function to the agent.Tool interface.
type tool struct {
	name        string
	description string
	parameters  json.RawMessage
	confirm     bool
	prompt      func(params json.RawMessage) string
	run         func(ctx context.Context, sessionID string, params json.RawMessage) (any, error)
}

func (t *tool) Name() string                { return t.name }
func (t *tool) Description() string         { return t.description }
func (t *tool) Parameters() json.RawMessage { return t.parameters }
func (t *tool) ConfirmationRequired() bool  { return t.confirm }
func (t *tool) ThinkingSound() bool         { return true }

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
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()

	result, err := t.run(ctx, sessionID, params)
	if err != nil {
		// Tool errors are conversational, not fatal: the agent should say what
		// went wrong rather than the turn collapsing.
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

// Tools returns the GitHub tool set. tasks may be nil, in which case pull
// request creation is not offered at all — there would be no verified change to
// publish.
func (s *Service) Tools(tasks TaskSource) []agent.Tool {
	repoHint := "Repository as owner/name. Allowed: " + strings.Join(s.client.Repositories(), ", ")
	repoParam := `"repository":{"type":"string","description":` + jsonString(repoHint) + `}`

	all := []*tool{
		{
			name:        "github.latest_ci_failure",
			description: "Find why CI failed. Returns the most recent failed workflow run with the failed jobs, the step that failed, and a short excerpt of the relevant log. Use this first whenever someone asks why a build or CI is broken.",
			parameters: json.RawMessage(`{"type":"object","properties":{` + repoParam + `,
				"branch":{"type":"string","description":"Branch to look at. Omit for any branch."},
				"workflow":{"type":"string","description":"Workflow name, for example CI. Omit to match any."}},
				"required":["repository"]}`),
			run: func(ctx context.Context, _ string, params json.RawMessage) (any, error) {
				var args struct {
					Repository string `json:"repository"`
					Branch     string `json:"branch"`
					Workflow   string `json:"workflow"`
				}
				if err := json.Unmarshal(params, &args); err != nil {
					return nil, err
				}
				return s.LatestCIFailure(ctx, args.Repository, args.Branch, args.Workflow)
			},
		},
		{
			name:        "github.repo_status",
			description: "A quick snapshot of a repository: default branch, open issue count, and the state of its latest workflow run.",
			parameters: json.RawMessage(`{"type":"object","properties":{` + repoParam + `},
				"required":["repository"]}`),
			run: func(ctx context.Context, _ string, params json.RawMessage) (any, error) {
				var args struct {
					Repository string `json:"repository"`
				}
				if err := json.Unmarshal(params, &args); err != nil {
					return nil, err
				}
				return s.RepoStatus(ctx, args.Repository)
			},
		},
		{
			name:        "github.workflow_run",
			description: "List recent GitHub Actions runs for a repository, or describe one run by id.",
			parameters: json.RawMessage(`{"type":"object","properties":{` + repoParam + `,
				"branch":{"type":"string","description":"Branch filter."},
				"run_id":{"type":"integer","description":"Describe this run instead of listing."},
				"limit":{"type":"integer","description":"How many runs to list, 1-20."}},
				"required":["repository"]}`),
			run: func(ctx context.Context, _ string, params json.RawMessage) (any, error) {
				var args struct {
					Repository string `json:"repository"`
					Branch     string `json:"branch"`
					RunID      int64  `json:"run_id"`
					Limit      int    `json:"limit"`
				}
				if err := json.Unmarshal(params, &args); err != nil {
					return nil, err
				}
				if args.RunID > 0 {
					return s.RunFailure(ctx, args.Repository, args.RunID)
				}
				if args.Limit == 0 {
					args.Limit = 5
				}
				return s.WorkflowRuns(ctx, args.Repository, args.Branch, args.Limit)
			},
		},
		{
			name:        "github.workflow_logs",
			description: "Read the reduced log for a workflow run. Returns only the lines that explain a failure, never the full log.",
			parameters: json.RawMessage(`{"type":"object","properties":{` + repoParam + `,
				"run_id":{"type":"integer","description":"The workflow run id."},
				"job":{"type":"string","description":"Job name. Omit to read every failed job."}},
				"required":["repository","run_id"]}`),
			run: func(ctx context.Context, _ string, params json.RawMessage) (any, error) {
				var args struct {
					Repository string `json:"repository"`
					RunID      int64  `json:"run_id"`
					Job        string `json:"job"`
				}
				if err := json.Unmarshal(params, &args); err != nil {
					return nil, err
				}
				if args.RunID <= 0 {
					return nil, fmt.Errorf("run_id is required")
				}
				return s.WorkflowLogs(ctx, args.Repository, args.RunID, args.Job)
			},
		},
		{
			name:        "github.pull_request",
			description: "Read one pull request: title, state, author, branches, and size.",
			parameters: json.RawMessage(`{"type":"object","properties":{` + repoParam + `,
				"number":{"type":"integer","description":"Pull request number."}},
				"required":["repository","number"]}`),
			run: func(ctx context.Context, _ string, params json.RawMessage) (any, error) {
				var args struct {
					Repository string `json:"repository"`
					Number     int    `json:"number"`
				}
				if err := json.Unmarshal(params, &args); err != nil {
					return nil, err
				}
				return s.PullRequest(ctx, args.Repository, args.Number)
			},
		},
		{
			name:        "github.commit",
			description: "Read one commit: message, author, size, and which files it touched.",
			parameters: json.RawMessage(`{"type":"object","properties":{` + repoParam + `,
				"ref":{"type":"string","description":"Commit SHA, branch, or tag."}},
				"required":["repository","ref"]}`),
			run: func(ctx context.Context, _ string, params json.RawMessage) (any, error) {
				var args struct {
					Repository string `json:"repository"`
					Ref        string `json:"ref"`
				}
				if err := json.Unmarshal(params, &args); err != nil {
					return nil, err
				}
				return s.Commit(ctx, args.Repository, args.Ref)
			},
		},
		{
			name:        "github.file",
			description: "Read one file from a repository at a given ref.",
			parameters: json.RawMessage(`{"type":"object","properties":{` + repoParam + `,
				"path":{"type":"string","description":"Path inside the repository."},
				"ref":{"type":"string","description":"Branch, tag, or SHA. Omit for the default branch."}},
				"required":["repository","path"]}`),
			run: func(ctx context.Context, _ string, params json.RawMessage) (any, error) {
				var args struct {
					Repository string `json:"repository"`
					Path       string `json:"path"`
					Ref        string `json:"ref"`
				}
				if err := json.Unmarshal(params, &args); err != nil {
					return nil, err
				}
				return s.File(ctx, args.Repository, args.Path, args.Ref)
			},
		},
		{
			name:        "github.diff",
			description: "Compare two refs and return the unified diff between them.",
			parameters: json.RawMessage(`{"type":"object","properties":{` + repoParam + `,
				"base":{"type":"string","description":"Base ref."},
				"head":{"type":"string","description":"Head ref."}},
				"required":["repository","base","head"]}`),
			run: func(ctx context.Context, _ string, params json.RawMessage) (any, error) {
				var args struct {
					Repository string `json:"repository"`
					Base       string `json:"base"`
					Head       string `json:"head"`
				}
				if err := json.Unmarshal(params, &args); err != nil {
					return nil, err
				}
				return s.Diff(ctx, args.Repository, args.Base, args.Head)
			},
		},
	}

	if tasks != nil {
		all = append(all, s.pullRequestTool(tasks, repoParam))
	}

	out := make([]agent.Tool, 0, len(all))
	for _, t := range all {
		out = append(out, t)
	}
	return out
}

// pullRequestTool is the only write path in this package. It is confirmation
// gated, it refuses without a verified developer task, and the token it mints
// never leaves the process.
func (s *Service) pullRequestTool(tasks TaskSource, repoParam string) *tool {
	return &tool{
		name: "github.create_pull_request",
		description: "Push the current developer task's branch and open a pull request. " +
			"Only works after Codex has changed something and its tests have been run. " +
			"Never pushes to a protected branch and never merges.",
		parameters: json.RawMessage(`{"type":"object","properties":{` + repoParam + `,
			"title":{"type":"string","description":"Pull request title."},
			"body":{"type":"string","description":"Pull request description."},
			"branch":{"type":"string","description":"Branch to push. Omit to let StreamCore name it."},
			"base":{"type":"string","description":"Base branch. Omit for the repository default."}},
			"required":["repository"]}`),
		confirm: true,
		prompt: func(params json.RawMessage) string {
			var args struct {
				Repository string `json:"repository"`
			}
			json.Unmarshal(params, &args)
			return fmt.Sprintf("This will push the branch and open a pull request on %s. Shall I do that?", args.Repository)
		},
		run: func(ctx context.Context, sessionID string, params json.RawMessage) (any, error) {
			var args struct {
				Repository string `json:"repository"`
				Title      string `json:"title"`
				Body       string `json:"body"`
				Branch     string `json:"branch"`
				Base       string `json:"base"`
			}
			if err := json.Unmarshal(params, &args); err != nil {
				return nil, err
			}
			change, err := tasks.PullRequestSource(sessionID, args.Repository)
			if err != nil {
				return nil, err
			}
			branch := args.Branch
			if branch == "" {
				branch = change.Branch
			}
			title := args.Title
			if title == "" {
				title = change.Title
			}
			body := args.Body
			if body == "" {
				body = change.Summary
			}
			return s.CreatePullRequest(ctx, PullRequestRequest{
				Repository:   args.Repository,
				WorktreePath: change.WorktreePath,
				Branch:       branch,
				Base:         args.Base,
				Title:        title,
				Body:         body,
				TestsRun:     change.TestsRun,
			})
		},
	}
}

func jsonString(text string) string {
	encoded, _ := json.Marshal(text)
	return string(encoded)
}
