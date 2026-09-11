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

// A Go plugin is an ordinary subprocess plugin: same folder, same manifest,
// same protocol. This builds one against the real SDK and drives it through the
// server's own host, so the two implementations are checked against each other
// rather than against a description of the protocol.
func TestGoSDKPluginRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a Go plugin")
	}

	sdk, err := filepath.Abs(filepath.Join("..", "..", "..", "plugin-sdk", "go"))
	if err != nil {
		t.Fatalf("resolve sdk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sdk, "go.mod")); err != nil {
		t.Skipf("plugin-sdk/go not checked out alongside the server: %v", err)
	}

	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	write("go.mod", `module goplugin

go 1.22

require github.com/streamcoreai/plugin-sdk/go v0.0.0

replace github.com/streamcoreai/plugin-sdk/go => `+sdk+"\n")

	write("main.go", `package main

import (
	"encoding/json"
	"fmt"

	streamcore "github.com/streamcoreai/plugin-sdk/go"
)

func main() {
	plugin := streamcore.New()

	var settings struct {
		Greeting string `+"`json:\"greeting\"`"+`
	}

	plugin.OnInitialize(func(init streamcore.Init) ([]streamcore.Tool, error) {
		if err := init.Bind(&settings); err != nil {
			return nil, err
		}
		return []streamcore.Tool{
			{Name: "go.echo", Description: "Echo"},
			{Name: "go.drive", Description: "Drive"},
			{Name: "go.relay", Description: "Relay"},
		}, nil
	})

	plugin.OnExecute(func(call streamcore.Call) (any, error) {
		switch call.Tool {
		case "go.echo":
			var args struct {
				Text string `+"`json:\"text\"`"+`
			}
			if err := call.Bind(&args); err != nil {
				return nil, err
			}
			return settings.Greeting + " " + args.Text, nil

		case "go.drive":
			return streamcore.Result{
				Speak: "Driving.",
				Emit: []streamcore.Emission{{
					Topic:   "movement.command",
					Payload: map[string]any{"action": "forward"},
				}},
			}, nil

		case "go.relay":
			answer, err := plugin.CallTool(call.SessionID, "other.tool", map[string]any{"n": 7})
			if err != nil {
				return nil, err
			}
			return "relayed: " + answer, nil
		}
		return nil, fmt.Errorf("unknown tool %q", call.Tool)
	})

	plugin.OnEvent(func(event streamcore.Event) (any, error) {
		var data struct {
			Transcript string `+"`json:\"transcript\"`"+`
		}
		if err := event.Bind(&data); err != nil {
			return nil, err
		}
		summary, err := plugin.Complete(event.SessionID, "Be brief.", "Summarise: "+data.Transcript)
		if err != nil {
			return nil, err
		}
		payload, _ := json.Marshal(map[string]string{"summary": summary})
		return streamcore.Result{
			Emit: []streamcore.Emission{{Topic: "display.card", Payload: json.RawMessage(payload)}},
		}, nil
	})

	plugin.Run()
}
`)

	manifest := Manifest{
		Name:      "goplugin",
		Build:     []string{"go", "build", "-o", "goplugin", "."},
		Exec:      []string{"./goplugin"},
		Events:    []string{AssistantResponseCompletedEvent},
		TimeoutMs: 20000,
		Tools:     []ToolSpec{{Name: "go.placeholder"}},
	}
	manifest.config = json.RawMessage(`{"greeting":"hello"}`)
	manifest.Normalize()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("manifest: %v", err)
	}

	host := NewExternalPlugin(manifest, dir, "")
	host.SetCallbacks(Callbacks{
		CallTool: func(_ context.Context, sessionID, name string, args json.RawMessage) (string, error) {
			return "tool " + name + " saw " + string(args), nil
		},
		Complete: func(_ context.Context, _ string, req CompletionRequest) (string, error) {
			return "summary of: " + req.Prompt, nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := host.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer host.Stop()

	// Tools declared at initialize replaced the manifest's placeholder.
	specs := host.ToolSpecs()
	if len(specs) != 3 || specs[0].Name != "go.echo" {
		t.Fatalf("tools = %+v", specs)
	}

	// Config arrived, and a plain string is spoken as-is.
	got, err := host.Execute(context.Background(), "go.echo", "s1", json.RawMessage(`{"text":"world"}`))
	if err != nil {
		t.Fatalf("go.echo: %v", err)
	}
	if result := ParseResult(got); result.Speak != "hello world" {
		t.Errorf("echo speak = %q", result.Speak)
	}

	// An envelope emits a packet and speaks.
	got, err = host.Execute(context.Background(), "go.drive", "s1", nil)
	if err != nil {
		t.Fatalf("go.drive: %v", err)
	}
	result := ParseResult(got)
	if result.Speak != "Driving." {
		t.Errorf("drive speak = %q", result.Speak)
	}
	if len(result.Emit) != 1 || result.Emit[0].Topic != "movement.command" {
		t.Fatalf("drive emissions = %+v", result.Emit)
	}

	// A plugin reaching another plugin's tool, mid-call.
	got, err = host.Execute(context.Background(), "go.relay", "s2", nil)
	if err != nil {
		t.Fatalf("go.relay: %v", err)
	}
	if !strings.Contains(got, "tool other.tool saw") || !strings.Contains(got, `"n":7`) {
		t.Errorf("relay = %q", got)
	}

	// An event handler that asks the server's model for a completion.
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
	if !strings.Contains(string(out[0].Payload.(json.RawMessage)), "summary of: Summarise: what is the weather") {
		t.Errorf("payload = %s", out[0].Payload)
	}
}
