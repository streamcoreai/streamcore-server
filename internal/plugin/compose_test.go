package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// stagePlugin writes a manifest and a Python entrypoint into a plugin folder.
func stagePlugin(t *testing.T, root, name, manifest, source string) {
	t.Helper()
	folder := filepath.Join(root, "plugins", name)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatalf("stage %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(folder, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("stage %s manifest: %v", name, err)
	}
	body := `import json, sys, threading

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

` + source + `

for line in sys.stdin:
    line = line.strip()
    if line:
        handle(json.loads(line))
`
	if err := os.WriteFile(filepath.Join(folder, "main.py"), []byte(body), 0o644); err != nil {
		t.Fatalf("stage %s source: %v", name, err)
	}
}

// Two plugins that compose: one offers a credential its peer needs, the other
// only offers a tool once it can see that peer. Neither knows where the other
// lives, and the model sees neither of the joins.
func composed(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()

	// The supplier. Its credential tool is internal; its status tool is not.
	stagePlugin(t, root, "vault", `
name: vault
version: 1
exec: ["python3", "main.py"]
`, `
def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "id": req["id"], "result": {"tools": [
            {"name": "vault.secret", "description": "Internal", "internal": True},
            {"name": "vault.status", "description": "Report vault health"},
        ]}})
    elif req["method"] == "execute":
        if req["tool"] == "vault.secret":
            send({"jsonrpc": "2.0", "result": "tok_" + req["params"].get("scope", ""), "id": req["id"]})
        else:
            send({"jsonrpc": "2.0", "result": "vault is fine", "id": req["id"]})
    elif req["method"] == "ready":
        send({"jsonrpc": "2.0", "result": "ok", "id": req["id"]})
`)

	// The consumer. It cannot know at initialize whether the supplier loaded,
	// so it decides at ready.
	stagePlugin(t, root, "deployer", `
name: deployer
version: 1
exec: ["python3", "main.py"]
`, `
pending = {}

def handle(req):
    if "method" not in req:
        waiting = pending.pop("exec", None)
        if waiting is not None:
            send({"jsonrpc": "2.0", "result": "used " + str(req.get("result")), "id": waiting})
        return

    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "id": req["id"], "result": {"tools": [
            {"name": "deployer.plan", "description": "Plan a deploy"},
        ]}})
    elif req["method"] == "ready":
        if "vault.secret" in req["params"]["tools"]:
            send({"jsonrpc": "2.0", "id": req["id"], "result": {"tools": [
                {"name": "deployer.plan", "description": "Plan a deploy"},
                {"name": "deployer.ship", "description": "Ship it"},
            ]}})
        else:
            send({"jsonrpc": "2.0", "result": "ok", "id": req["id"]})
    elif req["method"] == "execute":
        pending["exec"] = req["id"]
        send({"jsonrpc": "2.0", "method": "tools/call", "id": "x1", "params": {
            "name": "vault.secret", "arguments": {"scope": "prod"}, "session_id": req.get("session_id", ""),
        }})
`)

	manager := NewManager(root)
	if err := manager.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	t.Cleanup(manager.Close)
	return manager
}

// An internal tool is reachable by plugins and invisible to the model. Both
// halves matter: it has to work, and it must not be offered.
func TestInternalToolsAreHiddenFromTheModel(t *testing.T) {
	manager := composed(t)

	for _, tool := range manager.Tools() {
		if tool.Name() == "vault.secret" {
			t.Fatal("an internal tool was offered to the model")
		}
	}
	if _, ok := manager.GetTool("vault.secret"); ok {
		t.Error("the model path resolved an internal tool")
	}
	// A visible tool from the same plugin is unaffected.
	if _, ok := manager.GetTool("vault.status"); !ok {
		t.Error("a visible tool from the same plugin disappeared")
	}

	got, err := manager.CallTool(context.Background(), "s1", "vault.secret", json.RawMessage(`{"scope":"prod"}`))
	if err != nil {
		t.Fatalf("plugin-to-plugin call: %v", err)
	}
	if got != "tok_prod" {
		t.Errorf("got %q", got)
	}
}

// One plugin reaching another's tool, mid-call, without knowing where it lives.
func TestOnePluginReachesAnothersTool(t *testing.T) {
	manager := composed(t)

	tool, ok := manager.GetTool("deployer.ship")
	if !ok {
		t.Fatal("deployer.ship did not appear")
	}
	got, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got != "used tok_prod" {
		t.Errorf("got %q", got)
	}
}

// The whole point of the ready pass: a tool that only makes sense alongside
// another plugin, declared without either plugin depending on load order.
func TestReadyPassRevisesTools(t *testing.T) {
	manager := composed(t)

	if _, ok := manager.GetTool("deployer.ship"); !ok {
		t.Error("deployer.ship was never declared, so the ready pass did not run")
	}
	if _, ok := manager.GetTool("deployer.plan"); !ok {
		t.Error("the revised list dropped a tool that was already there")
	}
}

// Without its peer, the conditional tool must not appear at all.
func TestConditionalToolStaysAbsentWithoutItsPeer(t *testing.T) {
	root := t.TempDir()
	stagePlugin(t, root, "deployer", `
name: deployer
version: 1
exec: ["python3", "main.py"]
`, `
def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "id": req["id"], "result": {"tools": [
            {"name": "deployer.plan", "description": "Plan a deploy"},
        ]}})
    elif req["method"] == "ready":
        if "vault.secret" in req["params"]["tools"]:
            send({"jsonrpc": "2.0", "id": req["id"], "result": {"tools": [
                {"name": "deployer.ship", "description": "Ship it"},
            ]}})
        else:
            send({"jsonrpc": "2.0", "result": "ok", "id": req["id"]})
`)

	manager := NewManager(root)
	if err := manager.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	t.Cleanup(manager.Close)

	if _, ok := manager.GetTool("deployer.ship"); ok {
		t.Error("a tool that needs a missing peer was offered anyway")
	}
	if _, ok := manager.GetTool("deployer.plan"); !ok {
		t.Error("the plugin lost its own tool when its peer was absent")
	}
}
