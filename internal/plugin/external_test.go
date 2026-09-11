package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePlugin writes a Python plugin that speaks the protocol directly, without
// the SDK, so these tests exercise the wire format rather than a helper.
func fakePlugin(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	source := `import json, sys, threading

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

` + body + `

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    req = json.loads(line)
    handle(req)
`
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(source), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	return dir
}

func startPlugin(t *testing.T, manifest Manifest, dir string, callbacks Callbacks) *ExternalPlugin {
	t.Helper()
	manifest.Normalize()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	host := NewExternalPlugin(manifest, dir, "")
	host.SetCallbacks(callbacks)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := host.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(host.Stop)
	return host
}

// A plugin written against the original protocol — one tool, a string result,
// no knowledge of any of the newer fields — must keep working untouched.
func TestLegacyStringResultStillWorks(t *testing.T) {
	dir := fakePlugin(t, `
def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        where = req.get("params", {}).get("location", "nowhere")
        send({"jsonrpc": "2.0", "result": "It is sunny in " + where, "id": req["id"]})
`)
	host := startPlugin(t, Manifest{
		Name:       "weather.get",
		Language:   "python",
		Entrypoint: "main.py",
	}, dir, Callbacks{})

	got, err := host.Execute(context.Background(), "weather.get", "s1", json.RawMessage(`{"location":"Berlin"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result := ParseResult(got); result.Speak != "It is sunny in Berlin" {
		t.Errorf("speak = %q", result.Speak)
	}
}

// One process, several tools, routed by the top-level tool field.
func TestMultiToolRouting(t *testing.T) {
	dir := fakePlugin(t, `
def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        send({"jsonrpc": "2.0", "result": "ran " + req["tool"] + " for " + req["session_id"], "id": req["id"]})
`)
	host := startPlugin(t, Manifest{
		Name:       "office",
		Language:   "python",
		Entrypoint: "main.py",
		Tools: []ToolSpec{
			{Name: "office.lights", Description: "Lights"},
			{Name: "office.blinds", Description: "Blinds"},
		},
	}, dir, Callbacks{})

	for _, name := range []string{"office.lights", "office.blinds"} {
		got, err := host.Execute(context.Background(), name, "sess-9", nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want := "ran " + name + " for sess-9"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// A plugin returning an envelope gets its packets sent and its words spoken.
func TestEnvelopeResultCarriesEmissions(t *testing.T) {
	dir := fakePlugin(t, `
def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        send({"jsonrpc": "2.0", "id": req["id"], "result": {
            "speak": "Turning the lights on.",
            "emit": [{"topic": "home.lights", "payload": {"on": True}}],
        }})
`)
	host := startPlugin(t, Manifest{
		Name: "home.lights", Language: "python", Entrypoint: "main.py",
	}, dir, Callbacks{})

	raw, err := host.Execute(context.Background(), "home.lights", "s1", nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	result := ParseResult(raw)
	if result.Speak != "Turning the lights on." {
		t.Errorf("speak = %q", result.Speak)
	}
	if len(result.Emit) != 1 || result.Emit[0].Topic != "home.lights" {
		t.Fatalf("emissions = %+v", result.Emit)
	}
	if !strings.Contains(strings.ReplaceAll(string(result.Emit[0].Payload), " ", ""), `"on":true`) {
		t.Errorf("payload = %s", result.Emit[0].Payload)
	}
}

// A plugin may declare its tools at initialize rather than in the manifest,
// which is how a plugin whose surface depends on what else is loaded works.
func TestToolsDeclaredAtInitialize(t *testing.T) {
	dir := fakePlugin(t, `
def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "id": req["id"], "result": {"tools": [
            {"name": "dyn.one", "description": "First"},
            {"name": "dyn.two", "description": "Second"},
        ]}})
`)
	host := startPlugin(t, Manifest{
		Name: "dyn", Language: "python", Entrypoint: "main.py",
		Tools: []ToolSpec{{Name: "dyn.placeholder", Description: "Replaced"}},
	}, dir, Callbacks{})

	specs := host.ToolSpecs()
	if len(specs) != 2 || specs[0].Name != "dyn.one" || specs[1].Name != "dyn.two" {
		t.Fatalf("tools = %+v", specs)
	}
}

// The three callbacks are what let a plugin do the things that used to require
// being compiled into the server.
func TestPluginCallbacks(t *testing.T) {
	dir := fakePlugin(t, `
pending = {}

def handle(req):
    if "method" not in req:
        # A reply from the server to one of our own requests.
        if req.get("id") in ("c1", "c2"):
            send({"jsonrpc": "2.0", "result": "got:" + str(req.get("result")), "id": pending["exec"]})
        return
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
        return
    if req["method"] == "execute":
        if req["tool"] == "t.emit":
            send({"jsonrpc": "2.0", "method": "emit", "params": {
                "topic": "unsolicited.topic",
                "payload": {"hello": "world"},
                "session_id": req["session_id"],
            }})
            send({"jsonrpc": "2.0", "result": "emitted", "id": req["id"]})
            return
        if req["tool"] == "t.call":
            pending["exec"] = req["id"]
            send({"jsonrpc": "2.0", "method": "tools/call", "id": "c1", "params": {
                "name": "other.tool", "arguments": {"x": 1}, "session_id": req["session_id"],
            }})
            return
        if req["tool"] == "t.complete":
            pending["exec"] = req["id"]
            send({"jsonrpc": "2.0", "method": "llm/complete", "id": "c2", "params": {
                "prompt": "summarise", "session_id": req["session_id"],
            }})
            return
`)

	var mu sync.Mutex
	var emitted []Emission
	host := startPlugin(t, Manifest{
		Name: "t", Language: "python", Entrypoint: "main.py",
		Tools: []ToolSpec{{Name: "t.emit"}, {Name: "t.call"}, {Name: "t.complete"}},
	}, dir, Callbacks{
		Emit: func(_ context.Context, sessionID string, e Emission) error {
			mu.Lock()
			defer mu.Unlock()
			e.SessionID = sessionID
			emitted = append(emitted, e)
			return nil
		},
		CallTool: func(_ context.Context, sessionID, name string, args json.RawMessage) (string, error) {
			return "called " + name + " in " + sessionID + " with " + string(args), nil
		},
		Complete: func(_ context.Context, sessionID string, req CompletionRequest) (string, error) {
			return "completion of " + req.Prompt, nil
		},
	})

	if _, err := host.Execute(context.Background(), "t.emit", "sess-1", nil); err != nil {
		t.Fatalf("emit tool: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(emitted) == 1
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	if len(emitted) != 1 || emitted[0].Topic != "unsolicited.topic" || emitted[0].SessionID != "sess-1" {
		t.Errorf("emitted = %+v", emitted)
	}
	mu.Unlock()

	got, err := host.Execute(context.Background(), "t.call", "sess-2", nil)
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !strings.Contains(got, "called other.tool in sess-2") {
		t.Errorf("tools/call result = %q", got)
	}

	got, err = host.Execute(context.Background(), "t.complete", "sess-3", nil)
	if err != nil {
		t.Fatalf("llm/complete: %v", err)
	}
	if !strings.Contains(got, "completion of summarise") {
		t.Errorf("llm/complete result = %q", got)
	}
}

// A plugin cannot call its own tool: that would deadlock against its own loop.
func TestToolCallCannotReenterOwnPlugin(t *testing.T) {
	dir := fakePlugin(t, `
def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
`)
	host := startPlugin(t, Manifest{
		Name: "loop", Language: "python", Entrypoint: "main.py",
		Tools: []ToolSpec{{Name: "loop.self"}},
	}, dir, Callbacks{
		CallTool: func(context.Context, string, string, json.RawMessage) (string, error) {
			t.Error("callback should not have been reached")
			return "", nil
		},
	})

	_, err := host.serveToolCall(context.Background(), json.RawMessage(`{"name":"loop.self"}`))
	if err == nil || !strings.Contains(err.Error(), "belongs to this plugin") {
		t.Errorf("err = %v", err)
	}
}

// Settings from config.toml reach the plugin at initialize.
func TestConfigReachesPluginAtInitialize(t *testing.T) {
	dir := fakePlugin(t, `
seen = {}

def handle(req):
    if req["method"] == "initialize":
        seen["config"] = req["params"].get("config")
        seen["protocol"] = req["params"].get("protocol")
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        send({"jsonrpc": "2.0", "result": json.dumps(seen), "id": req["id"]})
`)
	manifest := Manifest{Name: "cfg", Language: "python", Entrypoint: "main.py"}
	manifest.config = json.RawMessage(`{"app_id":"123","repos":["a/b"]}`)
	host := startPlugin(t, manifest, dir, Callbacks{})

	got, err := host.Execute(context.Background(), "cfg", "s", nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(got, `"app_id": "123"`) {
		t.Errorf("config did not arrive: %s", got)
	}
	if !strings.Contains(got, `"protocol": 2`) {
		t.Errorf("protocol version did not arrive: %s", got)
	}
}

// A subscribing plugin receives lifecycle events and its emissions become
// outbound events, with no Go on the plugin's side.
func TestEventDeliveryToSubscribingPlugin(t *testing.T) {
	dir := fakePlugin(t, `
def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "event":
        data = req["params"]["data"]
        send({"jsonrpc": "2.0", "id": req["id"], "result": {
            "emit": [{"topic": "display.card", "payload": {"heard": data["transcript"]}}],
        }})
`)
	host := startPlugin(t, Manifest{
		Name: "projector", Language: "python", Entrypoint: "main.py",
		Events: []string{AssistantResponseCompletedEvent},
	}, dir, Callbacks{})

	if events := host.Events(); len(events) != 1 || events[0] != AssistantResponseCompletedEvent {
		t.Fatalf("subscriptions = %v", events)
	}

	out, err := host.HandleEvent(context.Background(), PluginEvent{
		Type:      AssistantResponseCompletedEvent,
		SessionID: "s1",
		TurnID:    "t7",
		Data:      AssistantResponseCompleted{Transcript: "hello there", Response: "hi"},
	})
	if err != nil {
		t.Fatalf("event: %v", err)
	}
	if len(out) != 1 || out[0].Type != "display.card" || out[0].TurnID != "t7" {
		t.Fatalf("outbound = %+v", out)
	}
	if !strings.Contains(string(out[0].Payload.(json.RawMessage)), "hello there") {
		t.Errorf("payload = %s", out[0].Payload)
	}
}

// A tool that outlives its budget fails the call rather than the conversation.
func TestExecuteTimeout(t *testing.T) {
	dir := fakePlugin(t, `
import time

def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        time.sleep(5)
        send({"jsonrpc": "2.0", "result": "too late", "id": req["id"]})
`)
	host := startPlugin(t, Manifest{
		Name: "slow", Language: "python", Entrypoint: "main.py", TimeoutMs: 300,
	}, dir, Callbacks{})

	_, err := host.Execute(context.Background(), "slow", "s", nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
}

// A dead plugin fails its waiters immediately instead of leaving them to time
// out one at a time.
func TestWaitersFailWhenPluginExits(t *testing.T) {
	dir := fakePlugin(t, `
import os

def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        os._exit(1)
`)
	host := startPlugin(t, Manifest{
		Name: "crash", Language: "python", Entrypoint: "main.py", TimeoutMs: 30000,
	}, dir, Callbacks{})

	start := time.Now()
	_, err := host.Execute(context.Background(), "crash", "s", nil)
	if err == nil {
		t.Fatal("expected an error from a plugin that exited")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s to notice the plugin had gone", elapsed)
	}
}

// The build step runs before the process starts, which is what lets a compiled
// language live in the same folder as an interpreted one.
func TestBuildStepRunsBeforeStart(t *testing.T) {
	dir := t.TempDir()
	source := `import json, sys
for line in sys.stdin:
    req = json.loads(line)
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "result": "built", "id": req["id"]}) + "\n")
    sys.stdout.flush()
`
	if err := os.WriteFile(filepath.Join(dir, "template.py"), []byte(source), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	host := startPlugin(t, Manifest{
		Name:  "built",
		Build: []string{"cp", "template.py", "main.py"},
		Exec:  []string{"python3", "main.py"},
	}, dir, Callbacks{})

	if _, err := os.Stat(filepath.Join(dir, "main.py")); err != nil {
		t.Fatalf("build did not produce main.py: %v", err)
	}
	got, err := host.Execute(context.Background(), "built", "s", nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got != "built" {
		t.Errorf("got %q", got)
	}
}

// Several sessions calling one plugin must not serialise behind each other.
func TestConcurrentCallsAreDemultiplexed(t *testing.T) {
	dir := fakePlugin(t, `
def worker(req):
    import time
    time.sleep(0.2)
    send({"jsonrpc": "2.0", "result": req["params"]["n"], "id": req["id"]})

def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        threading.Thread(target=worker, args=(req,), daemon=True).start()
`)
	host := startPlugin(t, Manifest{
		Name: "par", Language: "python", Entrypoint: "main.py", TimeoutMs: 10000,
	}, dir, Callbacks{})

	const calls = 8
	var wg sync.WaitGroup
	results := make([]string, calls)
	errs := make([]error, calls)
	start := time.Now()
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args, _ := json.Marshal(map[string]any{"n": i})
			results[i], errs[i] = host.Execute(context.Background(), "par", "s", args)
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	for i := range results {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		var got int
		if err := json.Unmarshal([]byte(results[i]), &got); err != nil || got != i {
			t.Errorf("call %d got %q — replies were mismatched", i, results[i])
		}
	}
	// Serialised, eight 200ms calls would take 1.6s.
	if elapsed > time.Second {
		t.Errorf("8 concurrent calls took %s; they appear to be serialised", elapsed)
	}
}

// A plugin anyone can drop in will eventually crash. It must come back rather
// than leaving its tools broken until someone restarts the server.
func TestCrashedPluginRestarts(t *testing.T) {
	dir := fakePlugin(t, `
import os

def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        if req["params"].get("crash"):
            os._exit(1)
        send({"jsonrpc": "2.0", "result": "alive", "id": req["id"]})
`)
	host := startPlugin(t, Manifest{
		Name: "flaky", Language: "python", Entrypoint: "main.py", TimeoutMs: 5000,
	}, dir, Callbacks{})

	if _, err := host.Execute(context.Background(), "flaky", "s", json.RawMessage(`{"crash":true}`)); err == nil {
		t.Fatal("a call into a dying plugin reported success")
	}

	// The first restart waits one backoff period before trying.
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		got, err := host.Execute(context.Background(), "flaky", "s", json.RawMessage(`{}`))
		if err == nil {
			if got != "alive" {
				t.Fatalf("after restart, got %q", got)
			}
			return
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("plugin never came back: %v", lastErr)
}

// A deliberate shutdown is not a crash, and must not be recovered from.
func TestStoppedPluginDoesNotRestart(t *testing.T) {
	dir := fakePlugin(t, `
def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        send({"jsonrpc": "2.0", "result": "alive", "id": req["id"]})
`)
	host := startPlugin(t, Manifest{
		Name: "tidy", Language: "python", Entrypoint: "main.py", TimeoutMs: 5000,
	}, dir, Callbacks{})

	host.Stop()
	time.Sleep(2 * time.Second)

	if _, err := host.Execute(context.Background(), "tidy", "s", nil); err == nil {
		t.Fatal("a stopped plugin came back on its own")
	}
}

// A plugin cannot ask the device for a picture itself. It declares what it
// needs and the server supplies it, so the next plugin that wants a frame works
// without the pipeline learning its name.
func TestRequiredCapabilityIsMergedIntoArguments(t *testing.T) {
	dir := fakePlugin(t, `
def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        send({"jsonrpc": "2.0", "result": json.dumps(req["params"], sort_keys=True), "id": req["id"]})
`)
	var asked []string
	host := startPlugin(t, Manifest{
		Name: "vision.analyze", Language: "python", Entrypoint: "main.py",
		Requires:      []string{"camera_frame"},
		ParametersRaw: map[string]any{"type": "object"},
	}, dir, Callbacks{
		Capture: func(_ context.Context, sessionID, capability string) (json.RawMessage, error) {
			asked = append(asked, sessionID+"/"+capability)
			return json.RawMessage(`{"image_base64":"AAAA","image_mime":"image/jpeg"}`), nil
		},
	})

	tool := &externalTool{host: host, spec: host.ToolSpecs()[0]}
	got, err := tool.ExecuteInSession(context.Background(), "s-7", json.RawMessage(`{"question":"what is this"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(asked) != 1 || asked[0] != "s-7/camera_frame" {
		t.Errorf("capture requests = %v", asked)
	}
	for _, want := range []string{`"question": "what is this"`, `"image_base64": "AAAA"`, `"image_mime": "image/jpeg"`} {
		if !strings.Contains(got, want) {
			t.Errorf("arguments missing %s: %s", want, got)
		}
	}
}

// A client with nothing to photograph is a thing to tell the user about, not a
// broken turn. The model must get words it can read back.
func TestFailedCaptureIsConversational(t *testing.T) {
	dir := fakePlugin(t, `
def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        raise RuntimeError("the plugin should never have been reached")
`)
	host := startPlugin(t, Manifest{
		Name: "vision.analyze", Language: "python", Entrypoint: "main.py",
		Requires:      []string{"camera_frame"},
		ParametersRaw: map[string]any{"type": "object"},
	}, dir, Callbacks{
		Capture: func(context.Context, string, string) (json.RawMessage, error) {
			return nil, errors.New("no camera responded")
		},
	})

	tool := &externalTool{host: host, spec: host.ToolSpecs()[0]}
	got, err := tool.ExecuteInSession(context.Background(), "s-1", nil)
	if err != nil {
		t.Fatalf("a failed capture ended the turn: %v", err)
	}
	if !strings.Contains(got, "camera frame") || !strings.Contains(got, "no camera responded") {
		t.Errorf("result does not explain itself: %q", got)
	}
}

// A tool that requires nothing must not pay for the machinery.
func TestToolsWithoutRequirementsSkipCapture(t *testing.T) {
	dir := fakePlugin(t, `
def handle(req):
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        send({"jsonrpc": "2.0", "result": json.dumps(req["params"]), "id": req["id"]})
`)
	host := startPlugin(t, Manifest{
		Name: "plain", Language: "python", Entrypoint: "main.py",
		ParametersRaw: map[string]any{"type": "object"},
	}, dir, Callbacks{
		Capture: func(context.Context, string, string) (json.RawMessage, error) {
			t.Error("capture was called for a tool that requires nothing")
			return nil, nil
		},
	})

	tool := &externalTool{host: host, spec: host.ToolSpecs()[0]}
	if _, err := tool.ExecuteInSession(context.Background(), "s", json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

// A plugin can reach the deployment's knowledge base, rather than standing up a
// second retrieval stack whose contents drift from this one's.
func TestPluginCanSearchTheKnowledgeBase(t *testing.T) {
	dir := fakePlugin(t, `
pending = {}

def handle(req):
    if "method" not in req:
        waiting = pending.pop("exec", None)
        if waiting is not None:
            send({"jsonrpc": "2.0", "result": " | ".join(req.get("result") or []), "id": waiting})
        return
    if req["method"] == "initialize":
        send({"jsonrpc": "2.0", "result": "initialized", "id": req["id"]})
    elif req["method"] == "execute":
        pending["exec"] = req["id"]
        send({"jsonrpc": "2.0", "method": "rag/search", "id": "s1", "params": {
            "query": req["params"]["q"], "limit": 2, "session_id": req.get("session_id", ""),
        }})
`)
	var asked string
	host := startPlugin(t, Manifest{
		Name: "grounded", Language: "python", Entrypoint: "main.py",
		ParametersRaw: map[string]any{"type": "object"},
	}, dir, Callbacks{
		Search: func(_ context.Context, sessionID, query string, limit int) ([]string, error) {
			asked = query
			return []string{"chunk one", "chunk two"}, nil
		},
	})

	got, err := host.Execute(context.Background(), "grounded", "s-1", json.RawMessage(`{"q":"refund policy"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if asked != "refund policy" {
		t.Errorf("query = %q", asked)
	}
	if got != "chunk one | chunk two" {
		t.Errorf("result = %q", got)
	}
}
