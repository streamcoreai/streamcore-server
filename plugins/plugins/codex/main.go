// Command codex is the Codex developer-agent plugin: investigate a problem in a
// repository, fix it in an isolated worktree, and run the tests.
//
// Codex authenticates with the operator's ChatGPT sign-in, which it owns
// entirely — StreamCore never holds those credentials. What it does not have is
// a way to fetch a repository, so it asks the GitHub plugin for a clone
// credential. Without one it declares nothing and does nothing.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"streamcore-plugin-codex/internal/agent"
	"streamcore-plugin-codex/internal/codex"

	streamcore "github.com/streamcoreai/plugin-sdk/go"
)

// cloneCredentialsTool is the GitHub plugin's internal tool. Codex has no
// GitHub App of its own, and this is the only thing it needs one for.
const cloneCredentialsTool = "github.clone_credentials"

// taskSourceTool is what this plugin offers back: the worktree a pull request
// should be opened from. Internal, because a model asking for a worktree path
// is a model doing something it should not.
const taskSourceTool = "codex.pull_request_source"

type settings struct {
	Enabled       bool     `json:"enabled"`
	Binary        string   `json:"binary"`
	ModelProvider string   `json:"model_provider"`
	Model         string   `json:"model"`
	WorkspaceRoot string   `json:"workspace_root"`
	TurnTimeoutMs int      `json:"turn_timeout_ms"`
	NetworkAccess bool     `json:"network_access"`
	Config        []string `json:"config"`
}

// taskChange is the wire form of what this plugin reports about the task open
// in a conversation. Keep in sync with the GitHub plugin's own copy.
type taskChange struct {
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	TestsRun     bool   `json:"tests_run"`
}

func main() {
	plugin := streamcore.New()

	var manager *codex.Manager
	tools := map[string]agent.Tool{}

	plugin.OnInitialize(func(init streamcore.Init) ([]streamcore.Tool, error) {
		var config settings
		if err := init.Bind(&config); err != nil {
			return nil, fmt.Errorf("read settings: %w", err)
		}
		if !config.Enabled {
			return nil, nil
		}

		created, err := codex.New(codex.Options{
			Binary:        config.Binary,
			ModelProvider: config.ModelProvider,
			Model:         config.Model,
			WorkspaceRoot: config.WorkspaceRoot,
			ExtraConfig:   config.Config,
			NetworkAccess: config.NetworkAccess,
			TurnTimeout:   time.Duration(config.TurnTimeoutMs) * time.Millisecond,
		}, cloneCredentials(plugin))
		if err != nil {
			// A Codex that will not start is a missing feature, not an outage.
			log.Printf("[codex] disabled: %v", err)
			return nil, nil
		}
		if err := created.Start(context.Background()); err != nil {
			log.Printf("[codex] disabled: %v", err)
			return nil, nil
		}

		manager = created
		manager.StartSweeper(context.Background(), time.Hour)

		declared := []streamcore.Tool{{
			Name: taskSourceTool,
			Description: "Internal: the worktree, branch and summary of the Codex task open in this conversation. " +
				"Not for the model — this is how a pull request finds the change it should publish.",
			Internal:   true,
			Parameters: json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string"}}}`),
		}}
		for _, tool := range manager.Tools() {
			tools[tool.Name()] = tool
			declared = append(declared, streamcore.Tool{
				Name:                 tool.Name(),
				Description:          tool.Description(),
				Parameters:           json.RawMessage(tool.Parameters()),
				ConfirmationRequired: tool.ConfirmationRequired(),
				ThinkingSound:        tool.ThinkingSound(),
			})
		}
		return declared, nil
	})

	// Codex cannot fetch anything without a clone credential, and the GitHub
	// plugin is what supplies one. Saying so once at startup beats every tool
	// failing later with the same reason.
	plugin.OnReady(func(ready streamcore.Ready) ([]streamcore.Tool, error) {
		if manager != nil && !ready.Has(cloneCredentialsTool) {
			log.Printf("[codex] %s is not loaded; repositories cannot be fetched", cloneCredentialsTool)
		}
		return nil, nil
	})

	plugin.OnExecute(func(call streamcore.Call) (any, error) {
		if call.Tool == taskSourceTool {
			return pullRequestSource(manager, call)
		}
		tool, ok := tools[call.Tool]
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", call.Tool)
		}
		if scoped, ok := tool.(agent.SessionTool); ok {
			return scoped.ExecuteInSession(context.Background(), call.SessionID, call.Params)
		}
		return tool.Execute(call.Params)
	})

	plugin.OnConfirm(func(call streamcore.Call) (string, error) {
		tool, ok := tools[call.Tool]
		if !ok {
			return "", fmt.Errorf("unknown tool: %s", call.Tool)
		}
		if prompter, ok := tool.(agent.ConfirmationPrompter); ok {
			return prompter.ConfirmationPrompt(call.Params), nil
		}
		return "", nil
	})

	// Run returns when the server closes stdin, which is the signal to take the
	// App Server and its worktrees down rather than being killed with children
	// still running.
	err := plugin.Run()
	if manager != nil {
		manager.Shutdown()
	}
	if err != nil {
		log.Printf("codex: %v", err)
		os.Exit(1)
	}
}

// cloneCredentials asks the GitHub plugin for a clone URL and a short-lived
// installation token.
//
// The token crosses a process boundary, which it did not when these two shared
// a binary. It is scoped to one repository, expires on its own, and is reachable
// only through the plugin-to-plugin path — the model is never offered the tool
// that mints it.
func cloneCredentials(plugin *streamcore.Plugin) codex.CloneURLFunc {
	return func(_ context.Context, repository string) (string, string, error) {
		raw, err := plugin.CallTool("", cloneCredentialsTool, map[string]any{"repository": repository})
		if err != nil {
			return "", "", fmt.Errorf("clone credentials for %s: %w", repository, err)
		}

		var credentials struct {
			URL   string `json:"url"`
			Token string `json:"token"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &credentials); err != nil {
			return "", "", fmt.Errorf("clone credentials for %s: unreadable answer", repository)
		}
		if credentials.Error != "" {
			return "", "", fmt.Errorf("clone credentials for %s: %s", repository, credentials.Error)
		}
		return credentials.URL, credentials.Token, nil
	}
}

// pullRequestSource answers the internal task tool.
func pullRequestSource(manager *codex.Manager, call streamcore.Call) (any, error) {
	if manager == nil {
		return nil, fmt.Errorf("codex is not configured")
	}
	var args struct {
		Repository string `json:"repository"`
	}
	if err := call.Bind(&args); err != nil {
		return nil, err
	}

	change, err := manager.PullRequestSource(call.SessionID, args.Repository)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(taskChange{
		WorktreePath: change.WorktreePath,
		Branch:       change.Branch,
		Title:        change.Title,
		Summary:      change.Summary,
		TestsRun:     change.TestsRun,
	})
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}
