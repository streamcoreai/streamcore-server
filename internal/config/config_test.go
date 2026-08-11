package config

import (
	"bufio"
	"bytes"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	exampleSectionPattern = regexp.MustCompile(`^\s*#?\s*\[([A-Za-z0-9_.-]+)\]\s*(?:#.*)?$`)
	exampleKeyPattern     = regexp.MustCompile(`^\s*#?\s*([A-Za-z0-9_]+)\s*=`)
)

func configPaths(typ reflect.Type, prefix string) []string {
	var paths []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := field.Tag.Get("toml")
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if field.Type.Kind() == reflect.Struct {
			paths = append(paths, configPaths(field.Type, path)...)
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

func documentedConfigPaths(t *testing.T, path string) map[string]struct{} {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open config example: %v", err)
	}
	defer file.Close()

	documented := make(map[string]struct{})
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if match := exampleSectionPattern.FindStringSubmatch(line); match != nil {
			section = match[1]
			continue
		}
		if section == "" {
			continue
		}
		if match := exampleKeyPattern.FindStringSubmatch(line); match != nil {
			documented[section+"."+match[1]] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read config example: %v", err)
	}
	return documented
}

func TestConfigExampleDocumentsEveryField(t *testing.T) {
	documented := documentedConfigPaths(t, filepath.Join("..", "..", "config.toml.example"))
	var missing []string
	for _, path := range configPaths(reflect.TypeOf(Config{}), "") {
		if _, ok := documented[path]; !ok {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("config.toml.example does not document: %s", strings.Join(missing, ", "))
	}
}

// writeConfig writes a TOML file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	originalOutput := log.Writer()
	originalFlags := log.Flags()
	originalPrefix := log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(originalOutput)
		log.SetFlags(originalFlags)
		log.SetPrefix(originalPrefix)
	})
	return &output
}

func TestLoadWarnsAboutUnknownKeys(t *testing.T) {
	logs := captureLogs(t)
	path := writeConfig(t, `
[pipeline]
unknown_option = true

[cartesia]
api_ke = "test-key"
`)

	if _, err := Load(path); err != nil {
		t.Fatalf("config with unknown keys rejected: %v", err)
	}
	const want = "Warning: unknown config key(s): cartesia.api_ke, pipeline.unknown_option — check for a typo"
	if got := strings.TrimSpace(logs.String()); got != want {
		t.Fatalf("warning = %q, want %q", got, want)
	}
}

func TestLoadDoesNotWarnAboutKnownKeys(t *testing.T) {
	logs := captureLogs(t)
	path := writeConfig(t, `
[pipeline]
barge_in = true

[cartesia]
api_key = "test-key"
`)

	if _, err := Load(path); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if got := logs.String(); got != "" {
		t.Fatalf("valid config logged an unexpected warning: %q", got)
	}
}

// Defaults must be applied before validation runs, or a config that simply
// omits reasoning_effort would be rejected for having an empty value.
func TestLoadAppliesDefaultsBeforeValidating(t *testing.T) {
	path := writeConfig(t, `
[realtime]
provider = "grok"

[grok]
api_key = "xai-test"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("minimal realtime config rejected: %v", err)
	}
	if cfg.Grok.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q, want the default \"high\"", cfg.Grok.ReasoningEffort)
	}
	if cfg.Grok.Model == "" {
		t.Error("model default not applied")
	}
	if cfg.Grok.Transcription == nil || !*cfg.Grok.Transcription {
		t.Error("transcription should default to enabled")
	}
	if !cfg.RealtimeEnabled() {
		t.Error("RealtimeEnabled() false for a configured realtime provider")
	}
}

func TestLoadRejectsBadRealtimeConfig(t *testing.T) {
	path := writeConfig(t, `
[realtime]
provider = "grock"

[grok]
api_key = "xai-test"
`)

	if _, err := Load(path); err == nil {
		t.Error("Load accepted a typo'd realtime provider")
	}
}

// A classic deployment must keep loading with no realtime section present.
func TestLoadClassicConfigUnaffected(t *testing.T) {
	path := writeConfig(t, `
[stt]
provider = "deepgram"

[llm]
provider = "openai"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("classic config rejected: %v", err)
	}
	if cfg.RealtimeEnabled() {
		t.Error("RealtimeEnabled() true with no [realtime] section")
	}
}

func realtimeConfig() *Config {
	cfg := &Config{}
	cfg.Realtime.Provider = "grok"
	cfg.Grok.APIKey = "xai-test"
	cfg.Grok.ReasoningEffort = "high"
	return cfg
}

func TestRealtimeEnabled(t *testing.T) {
	cases := []struct {
		provider string
		want     bool
	}{
		{"", false},
		{"none", false},
		{"grok", true},
	}
	for _, tc := range cases {
		cfg := &Config{}
		cfg.Realtime.Provider = tc.provider
		if got := cfg.RealtimeEnabled(); got != tc.want {
			t.Errorf("RealtimeEnabled() with provider %q = %v, want %v", tc.provider, got, tc.want)
		}
	}
}

func TestValidateRealtimeAcceptsValidConfig(t *testing.T) {
	if err := realtimeConfig().validateRealtime(); err != nil {
		t.Errorf("valid realtime config rejected: %v", err)
	}
}

// A disabled realtime section must not be validated at all, or every classic
// deployment would need Grok settings it has no use for.
func TestValidateRealtimeSkippedWhenDisabled(t *testing.T) {
	cfg := &Config{}
	if err := cfg.validateRealtime(); err != nil {
		t.Errorf("validation ran on a disabled realtime section: %v", err)
	}
}

func TestValidateRealtimeRejectsUnknownProvider(t *testing.T) {
	cfg := realtimeConfig()
	cfg.Realtime.Provider = "grock" // plausible typo

	if err := cfg.validateRealtime(); err == nil {
		t.Error("a typo'd provider was accepted; it would only fail on the first call")
	}
}

func TestValidateRealtimeRequiresAPIKey(t *testing.T) {
	cfg := realtimeConfig()
	cfg.Grok.APIKey = ""

	if err := cfg.validateRealtime(); err == nil {
		t.Error("missing [grok] api_key was accepted")
	}
}

func TestValidateRealtimeRejectsBadReasoningEffort(t *testing.T) {
	cfg := realtimeConfig()
	cfg.Grok.ReasoningEffort = "medium" // valid for chat models, not for voice

	if err := cfg.validateRealtime(); err == nil {
		t.Error("reasoning_effort = \"medium\" was accepted; only high/none are valid")
	}
}

// Out-of-range tunables are rejected by the provider mid-call, so they have to
// be caught at startup instead.
func TestValidateRealtimeRejectsOutOfRangeTunables(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"vad_threshold too low", func(c *Config) { c.Grok.VADThreshold = 0.05 }},
		{"vad_threshold too high", func(c *Config) { c.Grok.VADThreshold = 0.95 }},
		{"silence_duration_ms negative", func(c *Config) { c.Grok.SilenceDurationMs = -1 }},
		{"silence_duration_ms too high", func(c *Config) { c.Grok.SilenceDurationMs = 10001 }},
		{"prefix_padding_ms negative", func(c *Config) { c.Grok.PrefixPaddingMs = -1 }},
		{"prefix_padding_ms too high", func(c *Config) { c.Grok.PrefixPaddingMs = 10001 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := realtimeConfig()
			tc.mutate(cfg)
			if err := cfg.validateRealtime(); err == nil {
				t.Errorf("%s was accepted", tc.name)
			}
		})
	}
}

// Zero means "leave the provider default", so it must not trip the range check.
func TestValidateRealtimeAllowsUnsetTunables(t *testing.T) {
	cfg := realtimeConfig()
	cfg.Grok.VADThreshold = 0
	cfg.Grok.SilenceDurationMs = 0
	cfg.Grok.PrefixPaddingMs = 0

	if err := cfg.validateRealtime(); err != nil {
		t.Errorf("unset tunables rejected: %v", err)
	}
}

func TestValidateRealtimeRejectsTooManyKeyterms(t *testing.T) {
	cfg := realtimeConfig()
	cfg.Grok.Keyterms = make([]string, 101)

	if err := cfg.validateRealtime(); err == nil {
		t.Error("101 keyterms accepted; the documented maximum is 100")
	}
}
