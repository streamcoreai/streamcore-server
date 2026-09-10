package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The arm tools are what the agent actually calls, and they are pure manifest:
// no plugin process runs, so a typo here is only ever caught at a demo.
func TestArmManifestIsWellFormed(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "plugins", "plugins", "arm", "plugin.yaml"))
	if err != nil {
		t.Fatalf("read arm manifest: %v", err)
	}

	var manifest Manifest
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse arm manifest: %v", err)
	}
	manifest.Normalize()

	want := map[string]bool{
		"arm.pick": false, "arm.place": false, "arm.remember": false,
		"arm.home": false, "arm.stop": false,
		"arm.open_gripper": false, "arm.close_gripper": false,
	}

	for _, spec := range manifest.Tools {
		seen, expected := want[spec.Name]
		if !expected {
			t.Fatalf("unexpected tool %q", spec.Name)
		}
		if seen {
			t.Fatalf("%q declared twice", spec.Name)
		}
		want[spec.Name] = true

		if spec.Dispatch == nil {
			t.Fatalf("%q has no dispatch block; it would try to start a process", spec.Name)
		}
		if spec.Dispatch.Topic != "arm.command" {
			t.Fatalf("%q dispatches to %q", spec.Name, spec.Dispatch.Topic)
		}
		if spec.Dispatch.Payload["action"] == nil {
			t.Fatalf("%q carries no action; the device cannot tell what it is", spec.Name)
		}
		// A dispatch tool with no schema is one the model cannot call.
		if _, err := NewDispatchTool(spec); err != nil {
			t.Fatalf("%q: %v", spec.Name, err)
		}
	}

	for name, found := range want {
		if !found {
			t.Fatalf("%s is missing from the manifest", name)
		}
	}
}

// Naming an object is the agent's job; coordinates are the engine's. A schema
// that accepted a pose would invite a model to make one up.
func TestArmToolsTakeNamesNotCoordinates(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "plugins", "plugins", "arm", "plugin.yaml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var manifest Manifest
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse: %v", err)
	}
	manifest.Normalize()

	for _, spec := range manifest.Tools {
		schema := string(spec.Parameters())
		for _, banned := range []string{`"pose"`, `"position"`, `"coordinates"`, `"target_id"`} {
			if strings.Contains(schema, banned) {
				t.Fatalf("%q accepts %s; the model must never author coordinates", spec.Name, banned)
			}
		}
	}
}
