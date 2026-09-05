// Command github is the GitHub App plugin: read a repository's CI, find why a
// build broke, and — when something can produce a branch — open a pull request.
//
// It authenticates as a GitHub App. The private key is exchanged for
// short-lived installation tokens, scoped per repository and per operation. No
// personal access token is involved, and the key never reaches the model, a
// tool result, the data channel, or a log line.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"streamcore-plugin-github/internal/agent"
	"streamcore-plugin-github/internal/github"

	streamcore "github.com/streamcoreai/plugin-sdk/go"
)

// codexTaskTool is the tool this plugin calls to find the worktree a pull
// request should be opened from. It is internal to the Codex plugin: the model
// never sees it, and this plugin does not care what implements it, only that
// something answering to that name is loaded.
const codexTaskTool = "codex.pull_request_source"

// cloneCredentialsTool is what this plugin offers back. Codex needs a clone
// credential and has no GitHub App of its own; this is the join.
const cloneCredentialsTool = "github.clone_credentials"

type settings struct {
	Enabled        bool     `json:"enabled"`
	AppID          string   `json:"app_id"`
	InstallationID string   `json:"installation_id"`
	PrivateKeyPath string   `json:"private_key_path"`
	Repositories   []string `json:"repositories"`
	APIBaseURL     string   `json:"api_base_url"`
}

func main() {
	plugin := streamcore.New()

	var service *github.Service
	tools := map[string]agent.Tool{}

	plugin.OnInitialize(func(init streamcore.Init) ([]streamcore.Tool, error) {
		var config settings
		if err := init.Bind(&config); err != nil {
			return nil, fmt.Errorf("read settings: %w", err)
		}
		if !config.Enabled {
			return nil, nil
		}

		auth, err := github.NewAppAuth(config.AppID, config.InstallationID, config.PrivateKeyPath, config.APIBaseURL)
		if err != nil {
			// A credential problem is a missing feature, not an outage: the
			// plugin loads, declares nothing, and the call carries on.
			log.Printf("[github] disabled: %v", err)
			return nil, nil
		}
		service = github.NewService(auth, config.Repositories, config.APIBaseURL)
		log.Printf("[github] GitHub App %s ready for %v", config.AppID, config.Repositories)

		// Without a task source there is no pull request tool, which is what
		// the nil argument means here.
		return declare(tools, service, nil), nil
	})

	// Once everything has loaded, the pull request tool becomes possible if
	// something is there to produce a branch. That cannot be decided at
	// initialize, because the Codex plugin may not have started yet.
	plugin.OnReady(func(ready streamcore.Ready) ([]streamcore.Tool, error) {
		if service == nil || !ready.Has(codexTaskTool) {
			return nil, nil
		}
		log.Printf("[github] %s is available; offering pull request creation", codexTaskTool)
		return declare(tools, service, remoteTasks{plugin}), nil
	})

	plugin.OnExecute(func(call streamcore.Call) (any, error) {
		if call.Tool == cloneCredentialsTool {
			return cloneCredentials(service, call)
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

	if err := plugin.Run(); err != nil {
		log.Fatalf("github: %v", err)
	}
}

// declare rebuilds the tool set and the name lookup behind it.
func declare(tools map[string]agent.Tool, service *github.Service, tasks github.TaskSource) []streamcore.Tool {
	for name := range tools {
		delete(tools, name)
	}

	declared := []streamcore.Tool{{
		Name: cloneCredentialsTool,
		Description: "Internal: a clone URL and a short-lived installation token for an allowlisted repository. " +
			"Not for the model — this is how a developer agent fetches a repository it has no credential for.",
		Internal:   true,
		Parameters: json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string"}},"required":["repository"]}`),
	}}

	for _, tool := range service.Tools(tasks) {
		tools[tool.Name()] = tool
		declared = append(declared, streamcore.Tool{
			Name:                 tool.Name(),
			Description:          tool.Description(),
			Parameters:           json.RawMessage(tool.Parameters()),
			ConfirmationRequired: tool.ConfirmationRequired(),
			ThinkingSound:        tool.ThinkingSound(),
		})
	}
	return declared
}

// cloneCredentials answers the internal credential tool.
//
// The token it returns is short-lived, scoped to one repository, and read-only.
// It is reachable only through the plugin-to-plugin path, never by the model,
// and the allowlist is enforced before a token is minted at all.
func cloneCredentials(service *github.Service, call streamcore.Call) (any, error) {
	if service == nil {
		return nil, fmt.Errorf("github is not configured")
	}
	var args struct {
		Repository string `json:"repository"`
	}
	if err := call.Bind(&args); err != nil {
		return nil, err
	}

	url, token, err := service.CloneCredentials(context.Background(), args.Repository)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(map[string]string{"url": url, "token": token})
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

// remoteTasks reaches the Codex plugin for the worktree a pull request should
// be opened from.
//
// It replaces the in-process adapter the two had when they shared a binary. The
// boundary is stronger, not weaker: this side learns a worktree path, a branch,
// a summary and whether tests ran, and there is no longer even an address space
// in common through which anything else could be reached.
type remoteTasks struct {
	plugin *streamcore.Plugin
}

func (r remoteTasks) PullRequestSource(sessionID, repository string) (github.TaskChange, error) {
	raw, err := r.plugin.CallTool(sessionID, codexTaskTool, map[string]any{"repository": repository})
	if err != nil {
		return github.TaskChange{}, err
	}

	var wire taskChange
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return github.TaskChange{}, fmt.Errorf("%s answered with %q", codexTaskTool, raw)
	}
	// A tool that fails answers conversationally rather than erroring, so the
	// failure arrives as a field.
	if wire.Error != "" {
		return github.TaskChange{}, fmt.Errorf("%s", wire.Error)
	}

	return github.TaskChange{
		WorktreePath: wire.WorktreePath,
		Branch:       wire.Branch,
		Title:        wire.Title,
		Summary:      wire.Summary,
		TestsRun:     wire.TestsRun,
	}, nil
}

// taskChange is the wire form of what the Codex plugin reports about the task
// open in a conversation. Keep in sync with the Codex plugin's own copy.
type taskChange struct {
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	TestsRun     bool   `json:"tests_run"`
	Error        string `json:"error,omitempty"`
}
