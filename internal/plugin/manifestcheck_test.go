package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// Every shipped manifest must still load after a contract change. Adding
// optional fields is only safe if the ones written before them are untouched.
func TestEveryShippedManifestStillValidates(t *testing.T) {
	files, err := filepath.Glob("../../plugins/plugins/*/plugin.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no shipped manifests found: %v", err)
	}
	for _, f := range files {
		name := filepath.Base(filepath.Dir(f))
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var m Manifest
		if err := yaml.Unmarshal(raw, &m); err != nil {
			t.Errorf("%s: parse: %v", name, err)
			continue
		}
		m.Normalize()
		if err := m.Validate(); err != nil {
			t.Errorf("%s: validate: %v", name, err)
			continue
		}
		t.Logf("ok %-22s %d tools", name, len(m.Tools))
	}
}
