package realtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/streamcoreai/streamcore-server/internal/audio"
	"github.com/streamcoreai/streamcore-server/internal/config"
)

func baseConfig() config.GrokConfig {
	on := true
	return config.GrokConfig{
		APIKey:          "test-key",
		Model:           "grok-voice-think-fast-2.0",
		Voice:           "eve",
		SystemPrompt:    "Be brief.",
		ReasoningEffort: "high",
		Transcription:   &on,
	}
}

// session returns the "session" object from a session.update payload.
func session(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	if got := payload["type"]; got != "session.update" {
		t.Fatalf("payload type = %v, want session.update", got)
	}
	s, ok := payload["session"].(map[string]any)
	if !ok {
		t.Fatalf("session is %T, want map", payload["session"])
	}
	return s
}

// The audio format must match the pipeline exactly. Any drift here means
// silent resampling or, worse, audio played at the wrong speed.
func TestSessionUpdateMatchesPipelineAudioFormat(t *testing.T) {
	s := session(t, sessionUpdate(baseConfig(), Options{}))

	a, ok := s["audio"].(map[string]any)
	if !ok {
		t.Fatalf("audio is %T, want map", s["audio"])
	}

	for _, dir := range []string{"input", "output"} {
		side, ok := a[dir].(map[string]any)
		if !ok {
			t.Fatalf("audio.%s is %T, want map", dir, a[dir])
		}
		format, ok := side["format"].(map[string]any)
		if !ok {
			t.Fatalf("audio.%s.format is %T, want map", dir, side["format"])
		}
		if format["type"] != "audio/pcm" {
			t.Errorf("audio.%s.format.type = %v, want audio/pcm", dir, format["type"])
		}
		if format["rate"] != audio.SampleRate {
			t.Errorf("audio.%s.format.rate = %v, want %d (pipeline rate)", dir, format["rate"], audio.SampleRate)
		}
		if side["transport"] != "binary" {
			t.Errorf("audio.%s.transport = %v, want binary", dir, side["transport"])
		}
	}
}

func TestSessionUpdateCarriesVoiceAndInstructions(t *testing.T) {
	s := session(t, sessionUpdate(baseConfig(), Options{}))

	if s["voice"] != "eve" {
		t.Errorf("voice = %v, want eve", s["voice"])
	}
	if s["instructions"] != "Be brief." {
		t.Errorf("instructions = %v, want %q", s["instructions"], "Be brief.")
	}
}

// Skills reach the model through the instructions suffix. Dropping it would
// leave skills silently inert in realtime mode.
func TestSessionUpdateAppendsInstructionsSuffix(t *testing.T) {
	s := session(t, sessionUpdate(baseConfig(), Options{InstructionsSuffix: "Always confirm the address."}))

	got, _ := s["instructions"].(string)
	if !strings.Contains(got, "Be brief.") {
		t.Errorf("system prompt lost from instructions: %q", got)
	}
	if !strings.Contains(got, "Always confirm the address.") {
		t.Errorf("suffix missing from instructions: %q", got)
	}
}

func TestSessionUpdateWithoutSuffixLeavesPromptUnchanged(t *testing.T) {
	s := session(t, sessionUpdate(baseConfig(), Options{}))

	if got := s["instructions"]; got != "Be brief." {
		t.Errorf("instructions = %q, want the system prompt verbatim", got)
	}
}

func TestSessionUpdateOmitsEmptyVoice(t *testing.T) {
	cfg := baseConfig()
	cfg.Voice = ""

	s := session(t, sessionUpdate(cfg, Options{}))
	if _, ok := s["voice"]; ok {
		t.Error("empty voice was sent; the provider default should apply instead")
	}
}

// Zero-valued tunables must be omitted so the provider's own defaults stand.
// Sending threshold=0 would be out of the documented 0.1-0.9 range.
func TestSessionUpdateOmitsZeroTurnDetectionTunables(t *testing.T) {
	s := session(t, sessionUpdate(baseConfig(), Options{}))

	td, ok := s["turn_detection"].(map[string]any)
	if !ok {
		t.Fatalf("turn_detection is %T, want map", s["turn_detection"])
	}
	if td["type"] != "server_vad" {
		t.Errorf("turn_detection.type = %v, want server_vad", td["type"])
	}
	for _, k := range []string{"threshold", "silence_duration_ms", "prefix_padding_ms", "idle_timeout_ms"} {
		if _, ok := td[k]; ok {
			t.Errorf("turn_detection.%s sent despite being unset", k)
		}
	}
}

func TestSessionUpdateIncludesSetTurnDetectionTunables(t *testing.T) {
	cfg := baseConfig()
	cfg.VADThreshold = 0.6
	cfg.SilenceDurationMs = 500
	cfg.PrefixPaddingMs = 200
	cfg.IdleTimeoutMs = 15000

	td := session(t, sessionUpdate(cfg, Options{}))["turn_detection"].(map[string]any)

	if td["threshold"] != 0.6 {
		t.Errorf("threshold = %v, want 0.6", td["threshold"])
	}
	if td["silence_duration_ms"] != 500 {
		t.Errorf("silence_duration_ms = %v, want 500", td["silence_duration_ms"])
	}
	if td["prefix_padding_ms"] != 200 {
		t.Errorf("prefix_padding_ms = %v, want 200", td["prefix_padding_ms"])
	}
	if td["idle_timeout_ms"] != 15000 {
		t.Errorf("idle_timeout_ms = %v, want 15000", td["idle_timeout_ms"])
	}
}

func TestSessionUpdateTranscriptionToggle(t *testing.T) {
	cfg := baseConfig()
	cfg.LanguageHint = "ja"
	cfg.Keyterms = []string{"Tauranga"}

	in := session(t, sessionUpdate(cfg, Options{}))["audio"].(map[string]any)["input"].(map[string]any)
	tr, ok := in["transcription"].(map[string]any)
	if !ok {
		t.Fatalf("transcription is %T, want map", in["transcription"])
	}
	if tr["model"] != "grok-transcribe" {
		t.Errorf("transcription.model = %v, want grok-transcribe", tr["model"])
	}
	if tr["language_hint"] != "ja" {
		t.Errorf("language_hint = %v, want ja", tr["language_hint"])
	}

	off := false
	cfg.Transcription = &off
	in = session(t, sessionUpdate(cfg, Options{}))["audio"].(map[string]any)["input"].(map[string]any)
	if _, ok := in["transcription"]; ok {
		t.Error("transcription configured despite being disabled")
	}
}

func TestSessionUpdateMapsFunctionTools(t *testing.T) {
	tools := []ToolDefinition{{
		Name:        "weather.get",
		Description: "Get the weather",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
	}}

	list, ok := session(t, sessionUpdate(baseConfig(), Options{Tools: tools}))["tools"].([]map[string]any)
	if !ok {
		t.Fatalf("tools is %T, want slice", session(t, sessionUpdate(baseConfig(), Options{Tools: tools}))["tools"])
	}
	if len(list) != 1 {
		t.Fatalf("got %d tools, want 1", len(list))
	}
	if list[0]["type"] != "function" {
		t.Errorf("tool type = %v, want function", list[0]["type"])
	}
	if list[0]["name"] != "weather.get" {
		t.Errorf("tool name = %v, want weather.get", list[0]["name"])
	}

	// Parameters must survive as a JSON object, not a base64 string — which
	// is what happens if a []byte reaches the encoder untyped.
	encoded, err := json.Marshal(list[0]["parameters"])
	if err != nil {
		t.Fatalf("marshal parameters: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatalf("parameters did not encode as a JSON object: %v (got %s)", err, encoded)
	}
	if schema["type"] != "object" {
		t.Errorf("parameters.type = %v, want object", schema["type"])
	}
}

// A tool with no parameters must still send a valid empty schema; omitting
// the field or sending null is rejected.
func TestSessionUpdateDefaultsEmptyToolParameters(t *testing.T) {
	tools := []ToolDefinition{{Name: "time.get", Description: "Current time"}}

	list := session(t, sessionUpdate(baseConfig(), Options{Tools: tools}))["tools"].([]map[string]any)
	encoded, err := json.Marshal(list[0]["parameters"])
	if err != nil {
		t.Fatalf("marshal parameters: %v", err)
	}
	if string(encoded) != "{}" {
		t.Errorf("empty parameters encoded as %s, want {}", encoded)
	}
}

func TestSessionUpdateServerSideSearchTools(t *testing.T) {
	cfg := baseConfig()
	cfg.WebSearch = true
	cfg.XSearch = true

	list := session(t, sessionUpdate(cfg, Options{}))["tools"].([]map[string]any)
	types := make(map[string]bool, len(list))
	for _, tool := range list {
		types[tool["type"].(string)] = true
	}
	if !types["web_search"] || !types["x_search"] {
		t.Errorf("search tools missing from %v", types)
	}
}

func TestSessionUpdateOmitsEmptyToolList(t *testing.T) {
	s := session(t, sessionUpdate(baseConfig(), Options{}))
	if _, ok := s["tools"]; ok {
		t.Error("empty tools array sent; the field should be omitted entirely")
	}
}

// The whole payload has to survive encoding — a nil json.RawMessage or a
// non-serialisable value would only surface at runtime otherwise.
func TestSessionUpdateIsSerialisable(t *testing.T) {
	cfg := baseConfig()
	cfg.WebSearch = true
	tools := []ToolDefinition{
		{Name: "a", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "b", Description: "d"},
	}

	data, err := json.Marshal(sessionUpdate(cfg, Options{Tools: tools}))
	if err != nil {
		t.Fatalf("session.update does not serialise: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("session.update does not round-trip: %v", err)
	}
}
