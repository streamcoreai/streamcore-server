package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The three SDKs implement one protocol, so they are checked against the same
// host with the same expectations. A capability that works in one and not
// another is the bug this catches.

func sdkRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "plugin-sdk"))
	if err != nil {
		t.Fatalf("resolve plugin-sdk: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("plugin-sdk not checked out alongside the server: %v", err)
	}
	return root
}

// exerciseSDK drives one plugin through every capability the protocol offers.
func exerciseSDK(t *testing.T, host *ExternalPlugin) {
	t.Helper()

	specs := host.ToolSpecs()
	if len(specs) != 3 {
		t.Fatalf("declared tools = %+v", specs)
	}

	got, err := host.Execute(context.Background(), "sdk.echo", "s1", json.RawMessage(`{"text":"world"}`))
	if err != nil {
		t.Fatalf("sdk.echo: %v", err)
	}
	if result := ParseResult(got); result.Speak != "hello world" {
		t.Errorf("echo speak = %q (config did not arrive, or the string result did not)", result.Speak)
	}

	got, err = host.Execute(context.Background(), "sdk.drive", "s1", nil)
	if err != nil {
		t.Fatalf("sdk.drive: %v", err)
	}
	result := ParseResult(got)
	if result.Speak != "Driving." {
		t.Errorf("drive speak = %q", result.Speak)
	}
	if len(result.Emit) != 1 || result.Emit[0].Topic != "movement.command" {
		t.Fatalf("drive emissions = %+v", result.Emit)
	}
	if !strings.Contains(strings.ReplaceAll(string(result.Emit[0].Payload), " ", ""), `"action":"forward"`) {
		t.Errorf("drive payload = %s", result.Emit[0].Payload)
	}

	got, err = host.Execute(context.Background(), "sdk.relay", "s2", nil)
	if err != nil {
		t.Fatalf("sdk.relay: %v", err)
	}
	if !strings.Contains(got, "other.tool in s2") {
		t.Errorf("relay = %q", got)
	}

	out, err := host.HandleEvent(context.Background(), PluginEvent{
		Type:      AssistantResponseCompletedEvent,
		SessionID: "s3",
		TurnID:    "turn-1",
		Data:      AssistantResponseCompleted{Transcript: "what is the weather"},
	})
	if err != nil {
		t.Fatalf("event: %v", err)
	}
	if len(out) != 1 || out[0].Type != "display.card" {
		t.Fatalf("outbound = %+v", out)
	}
	if !strings.Contains(string(out[0].Payload.(json.RawMessage)), "summary of: what is the weather") {
		t.Errorf("event payload = %s", out[0].Payload)
	}
}

func parityCallbacks() Callbacks {
	return Callbacks{
		CallTool: func(_ context.Context, sessionID, name string, _ json.RawMessage) (string, error) {
			return name + " in " + sessionID, nil
		},
		Complete: func(_ context.Context, _ string, req CompletionRequest) (string, error) {
			return "summary of: " + req.Prompt, nil
		},
	}
}

func startParityPlugin(t *testing.T, manifest Manifest, dir, sdkDir string) *ExternalPlugin {
	t.Helper()
	manifest.config = json.RawMessage(`{"greeting":"hello"}`)
	manifest.Events = []string{AssistantResponseCompletedEvent}
	manifest.TimeoutMs = 20000
	manifest.Tools = []ToolSpec{{Name: "sdk.placeholder"}}
	manifest.Normalize()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("manifest: %v", err)
	}

	host := NewExternalPlugin(manifest, dir, sdkDir)
	host.SetCallbacks(parityCallbacks())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := host.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(host.Stop)
	return host
}

func TestPythonSDKParity(t *testing.T) {
	root := sdkRoot(t)
	dir := t.TempDir()

	source := `from streamcoreai_plugin import StreamCoreAIPlugin, Tool, Result, Emission

plugin = StreamCoreAIPlugin()
settings = {}

@plugin.on_initialize
def setup(init):
    settings.update(init.config)
    return [
        Tool(name="sdk.echo", description="Echo"),
        Tool(name="sdk.drive", description="Drive"),
        Tool(name="sdk.relay", description="Relay"),
    ]

@plugin.on_execute
def handle(params, call):
    if call.tool == "sdk.echo":
        return settings.get("greeting", "") + " " + params.get("text", "")
    if call.tool == "sdk.drive":
        return Result(speak="Driving.", emit=[Emission("movement.command", {"action": "forward"})])
    if call.tool == "sdk.relay":
        return plugin.call_tool(call.session_id, "other.tool", {"n": 7})
    raise RuntimeError("unknown tool " + call.tool)

@plugin.on_event
def observe(event):
    summary = plugin.complete(event.session_id, event.data["transcript"])
    return Result(emit=[Emission("display.card", {"summary": summary})])

plugin.run()
`
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(source), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}

	host := startParityPlugin(t, Manifest{
		Name: "pysdk", Language: "python", Entrypoint: "main.py",
	}, dir, root)
	exerciseSDK(t, host)
}

func TestTypeScriptSDKParity(t *testing.T) {
	root := sdkRoot(t)
	dist := filepath.Join(root, "typescript", "dist", "index.js")
	if _, err := os.Stat(dist); err != nil {
		t.Skipf("typescript SDK is not built (npm run build in plugin-sdk/typescript): %v", err)
	}

	dir := t.TempDir()
	source := `const { StreamCoreAIPlugin } = require(` + strconv(dist) + `);

const plugin = new StreamCoreAIPlugin();
let settings = {};

plugin.onInitialize((init) => {
  settings = init.config || {};
  return [
    { name: "sdk.echo", description: "Echo" },
    { name: "sdk.drive", description: "Drive" },
    { name: "sdk.relay", description: "Relay" },
  ];
});

plugin.onExecute(async (params, call) => {
  if (call.tool === "sdk.echo") return (settings.greeting || "") + " " + (params.text || "");
  if (call.tool === "sdk.drive") {
    return { speak: "Driving.", emit: [{ topic: "movement.command", payload: { action: "forward" } }] };
  }
  if (call.tool === "sdk.relay") return await plugin.callTool(call.sessionId, "other.tool", { n: 7 });
  throw new Error("unknown tool " + call.tool);
});

plugin.onEvent(async (event) => {
  const summary = await plugin.complete(event.sessionId, event.data.transcript);
  return { emit: [{ topic: "display.card", payload: { summary } }] };
});

plugin.run();
`
	if err := os.WriteFile(filepath.Join(dir, "main.js"), []byte(source), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}

	host := startParityPlugin(t, Manifest{
		Name: "tssdk", Language: "javascript", Entrypoint: "main.js",
	}, dir, root)
	exerciseSDK(t, host)
}

// strconv quotes a path for embedding in JavaScript source.
func strconv(path string) string {
	encoded, _ := json.Marshal(path)
	return string(encoded)
}
