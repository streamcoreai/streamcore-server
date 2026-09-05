package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The bounds the shipped manifests advertise. They used to be Go constants the
// pipeline clamped with; the clamp is now driven by the schema itself, so these
// exist to catch a hand-edit that quietly widens what the clients accept.
const (
	minMovementMs = 100
	maxMovementMs = 10000
	minGestureMs  = 200
	maxGestureMs  = 10000
)

func loadManifest(t *testing.T, folder string) (Manifest, map[string]*DispatchTool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "plugins", "plugins", folder, "plugin.yaml"))
	if err != nil {
		t.Fatalf("read %s manifest: %v", folder, err)
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse %s manifest: %v", folder, err)
	}
	manifest.Normalize()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("%s manifest is invalid: %v", folder, err)
	}

	tools := map[string]*DispatchTool{}
	for _, spec := range manifest.Tools {
		tool, err := NewDispatchTool(spec)
		if err != nil {
			t.Fatalf("%s: %v", spec.Name, err)
		}
		tools[spec.Name] = tool
	}
	return manifest, tools
}

func schemaProperties(t *testing.T, tool *DispatchTool) map[string]struct {
	Type    string          `json:"type"`
	Default json.RawMessage `json:"default"`
	Minimum *int            `json:"minimum"`
	Maximum *int            `json:"maximum"`
} {
	t.Helper()
	var schema struct {
		Properties map[string]struct {
			Type    string          `json:"type"`
			Default json.RawMessage `json:"default"`
			Minimum *int            `json:"minimum"`
			Maximum *int            `json:"maximum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("%s: bad schema: %v", tool.Name(), err)
	}
	return schema.Properties
}

// Client-side, `continuous` only feeds the distance calculation, so these are
// the only actions that can honour it. A tool that advertises it without
// honouring it lets the model set the flag, be told it worked, and leave the
// robot doing a normal timed move instead.
func TestContinuousOnlyOnTravellingTools(t *testing.T) {
	honours := map[string]bool{
		"movement.forward": true, "movement.backward": true,
		"movement.pivot_forward_left": true, "movement.pivot_forward_right": true,
		"movement.pivot_back_left": true, "movement.pivot_back_right": true,
	}
	_, tools := loadManifest(t, "movement")
	for name, tool := range tools {
		_, offered := schemaProperties(t, tool)["continuous"]
		if offered != honours[name] {
			t.Errorf("%s: offers continuous=%v, honoured=%v", name, offered, honours[name])
		}
		if !strings.HasPrefix(name, "movement.") {
			t.Errorf("%s: not namespaced under movement.", name)
		}
	}
}

// The advertised defaults tell the model what it gets when it omits an
// argument, so they have to match what the clients actually fall back to —
// defaultsFor in the voice bot, parse_movement_command in the firmware. Both
// clients agree on this table; if it changes, change it in all three.
func TestAdvertisedDefaultsMatchClients(t *testing.T) {
	want := map[string][2]int{ // duration_ms, speed_percent
		"movement.forward": {1500, 80}, "movement.backward": {1500, 80},
		"movement.turn_left": {1500, 80}, "movement.turn_right": {1500, 80},
		"movement.pivot_forward_left": {1500, 80}, "movement.pivot_forward_right": {1500, 80},
		"movement.pivot_back_left": {1500, 80}, "movement.pivot_back_right": {1500, 80},
		"movement.fancy": {3000, 80},
		"movement.shake": {1200, 90},
	}
	_, tools := loadManifest(t, "movement")
	for name, tool := range tools {
		expected, ok := want[name]
		if !ok {
			if name != "movement.stop" {
				t.Errorf("%s: no expected defaults recorded", name)
			}
			continue
		}
		properties := schemaProperties(t, tool)
		intDefault := func(field string) int {
			var value int
			if err := json.Unmarshal(properties[field].Default, &value); err != nil {
				t.Fatalf("%s: %s default: %v", name, field, err)
			}
			return value
		}
		if got := intDefault("duration_ms"); got != expected[0] {
			t.Errorf("%s: duration_ms default %d, clients use %d", name, got, expected[0])
		}
		if got := intDefault("speed_percent"); got != expected[1] {
			t.Errorf("%s: speed_percent default %d, clients use %d", name, got, expected[1])
		}
	}
}

func TestSchemaBoundsAreAdvertised(t *testing.T) {
	check := func(folder string, min, max int) {
		_, tools := loadManifest(t, folder)
		for name, tool := range tools {
			duration, ok := schemaProperties(t, tool)["duration_ms"]
			if !ok {
				continue // stop and rest take no arguments
			}
			if duration.Minimum == nil || *duration.Minimum != min {
				t.Errorf("%s: duration_ms minimum %v, want %d", name, duration.Minimum, min)
			}
			if duration.Maximum == nil || *duration.Maximum != max {
				t.Errorf("%s: duration_ms maximum %v, want %d", name, duration.Maximum, max)
			}
		}
	}
	check("movement", minMovementMs, maxMovementMs)
	check("bot-gestures", minGestureMs, maxGestureMs)
}

// The bounds are advertised so the model can predict what it gets; the dispatch
// tool has to actually apply them, or the schema is a lie the client acts on.
func TestDispatchClampsToAdvertisedBounds(t *testing.T) {
	_, tools := loadManifest(t, "movement")
	tool := tools["movement.forward"]

	for _, tc := range []struct {
		args string
		want string
	}{
		{`{"duration_ms":50}`, `{"action":"forward","duration_ms":100}`},
		{`{"duration_ms":999999}`, `{"action":"forward","duration_ms":10000}`},
		{`{"speed_percent":300}`, `{"action":"forward","speed_percent":100}`},
		{`{"duration_ms":1500,"speed_percent":80}`, `{"action":"forward","duration_ms":1500,"speed_percent":80}`},
	} {
		raw, err := tool.Execute(json.RawMessage(tc.args))
		if err != nil {
			t.Fatalf("%s: %v", tc.args, err)
		}
		result := ParseResult(raw)
		if len(result.Emit) != 1 {
			t.Fatalf("%s: expected one emission, got %d", tc.args, len(result.Emit))
		}
		var got, want any
		json.Unmarshal(result.Emit[0].Payload, &got)
		json.Unmarshal([]byte(tc.want), &want)
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s: got %s, want %s", tc.args, gotJSON, wantJSON)
		}
	}
}

// A tool must not forward an argument it never advertised. turn_left has no
// `continuous`, and a client that received one would have no way to tell the
// model it was ignored.
func TestUndeclaredArgumentsAreDropped(t *testing.T) {
	_, tools := loadManifest(t, "movement")
	raw, err := tools["movement.turn_left"].Execute(json.RawMessage(`{"continuous":true,"duration_ms":800}`))
	if err != nil {
		t.Fatalf("turn_left: %v", err)
	}
	result := ParseResult(raw)
	if strings.Contains(string(result.Emit[0].Payload), "continuous") {
		t.Errorf("turn_left forwarded continuous: %s", result.Emit[0].Payload)
	}
	if !strings.Contains(string(result.Emit[0].Payload), `"duration_ms":800`) {
		t.Errorf("turn_left dropped a declared argument: %s", result.Emit[0].Payload)
	}
}

// The shipped manifests must stay pure data: a dispatch-only plugin starts no
// process, which is the whole reason locomotion left the binary.
func TestShippedMotionPluginsStartNoProcess(t *testing.T) {
	for _, folder := range []string{"movement", "bot-gestures"} {
		manifest, _ := loadManifest(t, folder)
		if manifest.NeedsProcess() {
			t.Errorf("%s wants a process: exec=%v", folder, manifest.Exec)
		}
		for _, spec := range manifest.Tools {
			if spec.Dispatch == nil {
				t.Errorf("%s: %s has no dispatch block", folder, spec.Name)
			}
		}
	}
}

// A spoken confirmation has to name what it is about, and what it is about is
// almost always an argument.
func TestConfirmationPromptSubstitutesArguments(t *testing.T) {
	tool := &externalTool{spec: ToolSpec{
		Name:                 "github.create_pull_request",
		ConfirmationRequired: true,
		ConfirmationPrompt:   "This will push the branch and open a pull request on {repository}. Shall I do that?",
	}}

	got := tool.ConfirmationPrompt(json.RawMessage(`{"repository":"acme/api","title":"Fix"}`))
	want := "This will push the branch and open a pull request on acme/api. Shall I do that?"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// An argument the model omitted must not be read out as braces.
	got = tool.ConfirmationPrompt(json.RawMessage(`{}`))
	if strings.Contains(got, "{") {
		t.Errorf("unresolved placeholder survived: %q", got)
	}
}

// Every plugin has a name; only some describe a tool at the top level. An
// observer, or a plugin that declares its tools at initialize, must not have a
// tool invented for it out of its own name.
func TestManifestNameAloneIsNotATool(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest Manifest
		want     int
	}{
		{
			name:     "observer",
			manifest: Manifest{Name: "display-projector", Exec: []string{"./x"}, Events: []string{"e"}},
		},
		{
			name:     "declares at initialize",
			manifest: Manifest{Name: "developer", Exec: []string{"./x"}},
		},
		{
			name: "single tool",
			manifest: Manifest{
				Name:          "weather.get",
				Exec:          []string{"./x"},
				ParametersRaw: map[string]any{"type": "object"},
			},
			want: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := tc.manifest
			manifest.Normalize()
			if len(manifest.Tools) != tc.want {
				t.Errorf("declared %d tools, want %d: %+v", len(manifest.Tools), tc.want, manifest.Tools)
			}
			if err := manifest.Validate(); err != nil {
				t.Errorf("Validate: %v", err)
			}
		})
	}
}
