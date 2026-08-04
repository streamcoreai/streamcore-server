package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig writes a TOML file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
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
