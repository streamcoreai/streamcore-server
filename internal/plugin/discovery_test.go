package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// stageShipped copies the named shipped plugin folders into a temporary plugin
// directory, so discovery is exercised against the real manifests without
// starting every plugin in the repo.
func stageShipped(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	staged := filepath.Join(root, "plugins")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatalf("stage: %v", err)
	}

	for _, name := range names {
		source := filepath.Join("..", "..", "plugins", "plugins", name, "plugin.yaml")
		manifest, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read %s: %v", source, err)
		}
		folder := filepath.Join(staged, name)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatalf("stage %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(folder, "plugin.yaml"), manifest, 0o644); err != nil {
			t.Fatalf("stage %s: %v", name, err)
		}
	}
	return root
}

// Locomotion and gestures used to be Go compiled into the binary. They are now
// discovered from disk like anything else, and this is the path that has to
// keep working: manifest to registered tool, with no process started.
func TestShippedMotionPluginsLoadFromDisk(t *testing.T) {
	manager := NewManager(stageShipped(t, "movement", "bot-gestures"))
	if err := manager.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	t.Cleanup(manager.Close)

	if got := len(manager.Tools()); got != 25 {
		t.Fatalf("registered %d tools, want 25", got)
	}
	for _, name := range []string{"movement.forward", "movement.stop", "bot.wave", "bot.rest"} {
		if _, ok := manager.GetTool(name); !ok {
			t.Errorf("%s did not load", name)
		}
	}

	// No process: a dispatch-only plugin has nothing to stop.
	if len(manager.hosts) != 0 {
		t.Errorf("dispatch-only plugins started %d processes", len(manager.hosts))
	}
}

// The plugins that carry credentials ship dormant. A deployment that has not
// asked for them must not have them started, and must not see their tools.
func TestCredentialPluginsShipDisabled(t *testing.T) {
	manager := NewManager(stageShipped(t, "github", "codex", "display-projector"))
	if err := manager.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	t.Cleanup(manager.Close)

	if got := len(manager.Tools()); got != 0 {
		t.Errorf("a dormant plugin exposed %d tools", got)
	}
	if len(manager.hosts) != 0 {
		t.Errorf("a dormant plugin started %d processes", len(manager.hosts))
	}
}

// An operator turns one on from config.toml without editing a file they do not
// own. Discovery has to honour that, and the plugin then really does start.
func TestConfigEnablesADormantPlugin(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a Go plugin")
	}
	// CI checks out the server alone, so the SDK the plugin builds against is
	// only there in the full workspace.
	sdk, err := filepath.Abs(filepath.Join("..", "..", "..", "plugin-sdk", "go"))
	if err != nil {
		t.Fatalf("resolve sdk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sdk, "go.mod")); err != nil {
		t.Skipf("plugin-sdk/go not checked out alongside the server: %v", err)
	}

	root := stageShipped(t, "display-projector")
	// The staged copy needs the plugin's source to build from.
	source := filepath.Join("..", "..", "plugins", "plugins", "display-projector")
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		target := filepath.Join(root, "plugins", "display-projector", entry.Name())
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatalf("copy %s: %v", entry.Name(), err)
		}
	}
	// go.mod points at the SDK by relative path, which the staged copy has
	// moved away from.
	gomod := filepath.Join(root, "plugins", "display-projector", "go.mod")
	if err := os.WriteFile(gomod, []byte(`module display-projector

go 1.22

require github.com/streamcoreai/plugin-sdk/go v0.0.0

replace github.com/streamcoreai/plugin-sdk/go => `+sdk+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite go.mod: %v", err)
	}

	manager := NewManager(root)
	manager.SetPluginSettings(map[string]map[string]any{
		"display-projector": {"enabled": true},
	})
	if err := manager.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	t.Cleanup(manager.Close)

	if len(manager.hosts) != 1 {
		t.Fatalf("the plugin did not start: %d processes", len(manager.hosts))
	}
	// It is an observer, so it must still be invisible to the model.
	if got := len(manager.Tools()); got != 0 {
		t.Errorf("the projector exposed %d tools to the model", got)
	}
	handler, ok := manager.eventHandlers["display-projector"]
	if !ok {
		t.Fatal("the projector did not subscribe to anything")
	}
	if events := handler.Events(); len(events) != 1 || events[0] != AssistantResponseCompletedEvent {
		t.Errorf("subscriptions = %v", events)
	}
}
