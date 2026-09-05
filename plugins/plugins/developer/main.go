// Command developer is the developer-agent plugin: a GitHub App for reading a
// repository's CI, and the Codex harness for investigating and fixing what it
// finds.
//
// The two are optional and independent, exactly as they were when this lived in
// the server. A GitHub misconfiguration leaves Codex and voice working; an
// unauthenticated Codex leaves GitHub and voice working; with both off the
// plugin declares no tools and does nothing. Nothing here can fail a call.
//
// They share one process because they are one feature. GitHub's pull-request
// tool needs the worktree Codex prepared, and Codex needs GitHub's clone
// credential. Neither package imports the other — the adapter below is the only
// join, which keeps Codex's credential state unreachable from the GitHub side.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"developer/internal/agent"
	"developer/internal/codex"
	"developer/internal/github"

	streamcore "github.com/streamcoreai/plugin-sdk/go"
)

// settings is this plugin's table from the server's config.toml, under
// [plugins.config.developer].
type settings struct {
	GitHub struct {
		Enabled        bool     `json:"enabled"`
		AppID          string   `json:"app_id"`
		InstallationID string   `json:"installation_id"`
		PrivateKeyPath string   `json:"private_key_path"`
		Repositories   []string `json:"repositories"`
		APIBaseURL     string   `json:"api_base_url"`
	} `json:"github"`

	Codex struct {
		Enabled       bool     `json:"enabled"`
		Binary        string   `json:"binary"`
		ModelProvider string   `json:"model_provider"`
		Model         string   `json:"model"`
		WorkspaceRoot string   `json:"workspace_root"`
		TurnTimeoutMs int      `json:"turn_timeout_ms"`
		NetworkAccess bool     `json:"network_access"`
		Config        []string `json:"config"`
	} `json:"codex"`
}

func main() {
	plugin := streamcore.New()

	// tools is the name-to-implementation map the execute handler dispatches
	// through. It is written once during initialize and only read afterwards.
	tools := map[string]agent.Tool{}
	var manager *codex.Manager

	plugin.OnInitialize(func(init streamcore.Init) ([]streamcore.Tool, error) {
		var config settings
		if err := init.Bind(&config); err != nil {
			return nil, fmt.Errorf("read settings: %w", err)
		}

		ctx := context.Background()
		var declared []streamcore.Tool

		gh := startGitHub(config)
		manager = startCodex(ctx, config, gh)

		if manager != nil {
			for _, tool := range manager.Tools() {
				tools[tool.Name()] = tool
				declared = append(declared, describe(tool))
			}
		}

		if gh != nil {
			// The pull request tool only appears when there is a Codex task
			// that could produce a change to publish.
			var source github.TaskSource
			if manager != nil {
				source = codexTaskSource{manager}
			}
			for _, tool := range gh.Tools(source) {
				tools[tool.Name()] = tool
				declared = append(declared, describe(tool))
			}
		}

		if len(declared) == 0 {
			plugin.Logf("developer: neither github nor codex is configured; declaring no tools")
		}
		return declared, nil
	})

	plugin.OnExecute(func(call streamcore.Call) (any, error) {
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

	// Run returns when the server closes stdin, which is the signal to take
	// the App Server and its worktrees down cleanly rather than being killed
	// with children still running.
	err := plugin.Run()
	if manager != nil {
		manager.Shutdown()
	}
	if err != nil {
		log.Printf("developer: %v", err)
		os.Exit(1)
	}
}

// describe renders a tool for the server's tool list.
func describe(tool agent.Tool) streamcore.Tool {
	return streamcore.Tool{
		Name:                 tool.Name(),
		Description:          tool.Description(),
		Parameters:           json.RawMessage(tool.Parameters()),
		ConfirmationRequired: tool.ConfirmationRequired(),
		ThinkingSound:        tool.ThinkingSound(),
	}
}

// startGitHub returns nil when GitHub is off or misconfigured. A credential
// problem is a missing feature, not an outage.
func startGitHub(config settings) *github.Service {
	if !config.GitHub.Enabled {
		return nil
	}
	auth, err := github.NewAppAuth(
		config.GitHub.AppID,
		config.GitHub.InstallationID,
		config.GitHub.PrivateKeyPath,
		config.GitHub.APIBaseURL,
	)
	if err != nil {
		log.Printf("[github] disabled: %v", err)
		return nil
	}
	log.Printf("[github] GitHub App %s ready for %v", config.GitHub.AppID, config.GitHub.Repositories)
	return github.NewService(auth, config.GitHub.Repositories, config.GitHub.APIBaseURL)
}

// startCodex returns nil when Codex is off or cannot start. It needs GitHub for
// a clone credential, so without one there is nothing for it to work on.
func startCodex(ctx context.Context, config settings, gh *github.Service) *codex.Manager {
	if !config.Codex.Enabled {
		return nil
	}
	if gh == nil {
		log.Printf("[codex] disabled: it needs the github integration to fetch repositories")
		return nil
	}

	manager, err := codex.New(codex.Options{
		Binary:        config.Codex.Binary,
		ModelProvider: config.Codex.ModelProvider,
		Model:         config.Codex.Model,
		WorkspaceRoot: config.Codex.WorkspaceRoot,
		ExtraConfig:   config.Codex.Config,
		NetworkAccess: config.Codex.NetworkAccess,
		TurnTimeout:   time.Duration(config.Codex.TurnTimeoutMs) * time.Millisecond,
	}, gh.CloneCredentials)
	if err != nil {
		log.Printf("[codex] disabled: %v", err)
		return nil
	}
	if err := manager.Start(ctx); err != nil {
		// A Codex that will not start is a missing feature, not an outage.
		log.Printf("[codex] disabled: %v", err)
		return nil
	}
	manager.StartSweeper(ctx, time.Hour)
	return manager
}

// codexTaskSource adapts the Codex manager to what the GitHub side needs.
//
// It exists so neither package imports the other. The GitHub tools learn a
// worktree path, a branch, a summary, and whether tests ran — and cannot reach
// Codex's credential state even in principle, because there is no import edge
// to reach it through.
type codexTaskSource struct {
	manager *codex.Manager
}

func (c codexTaskSource) PullRequestSource(sessionID, repository string) (github.TaskChange, error) {
	change, err := c.manager.PullRequestSource(sessionID, repository)
	if err != nil {
		return github.TaskChange{}, err
	}
	return github.TaskChange{
		WorktreePath: change.WorktreePath,
		Branch:       change.Branch,
		Title:        change.Title,
		Summary:      change.Summary,
		TestsRun:     change.TestsRun,
	}, nil
}
