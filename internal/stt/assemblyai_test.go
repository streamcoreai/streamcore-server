package stt

import (
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

func TestAssemblyAILanguageCode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"en", "en"},
		{"en-US", "en"},
		{"en-IN", "en"},
		{"  es-MX  ", "es"},
		{"zh-CN", "zh"},
	}
	for _, tc := range tests {
		if got := assemblyAILanguageCode(tc.in); got != tc.want {
			t.Errorf("assemblyAILanguageCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildAssemblyAIURL_Defaults(t *testing.T) {
	raw := buildAssemblyAIURL(config.AssemblyAIConfig{})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Scheme != "wss" || u.Host != "streaming.assemblyai.com" || u.Path != "/v3/ws" {
		t.Fatalf("unexpected base url: %s", raw)
	}
	q := u.Query()
	if q.Get("sample_rate") != "16000" {
		t.Errorf("sample_rate not set to 16000: got %q", q.Get("sample_rate"))
	}
	if q.Get("encoding") != "pcm_s16le" {
		t.Errorf("encoding not pcm_s16le: got %q", q.Get("encoding"))
	}
	if q.Get("format_turns") != "true" {
		t.Errorf("format_turns should default true: got %q", q.Get("format_turns"))
	}
	if q.Get("speech_model") != "" {
		t.Errorf("speech_model should be omitted when empty: got %q", q.Get("speech_model"))
	}
}

func TestBuildAssemblyAIURL_AllOptions(t *testing.T) {
	off := false
	cfg := config.AssemblyAIConfig{
		Model:              "u3-rt-pro",
		Language:           "en-IN",
		FormatTurns:        &off,
		EndOfTurnSilenceMs: 500,
		Keyterms:           []string{"Rediffmail", "Krishnamurthy", ""},
	}
	raw := buildAssemblyAIURL(cfg)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("speech_model") != "u3-rt-pro" {
		t.Errorf("speech_model = %q", q.Get("speech_model"))
	}
	if q.Get("language_code") != "en" {
		t.Errorf("language_code = %q want en", q.Get("language_code"))
	}
	if q.Get("format_turns") != "false" {
		t.Errorf("format_turns = %q want false", q.Get("format_turns"))
	}
	if q.Get("end_of_turn_confidence_threshold_silence_duration_ms") != "500" {
		t.Errorf("silence duration = %q", q.Get("end_of_turn_confidence_threshold_silence_duration_ms"))
	}
	terms := q["keyterm_prompt"]
	if len(terms) != 2 || terms[0] != "Rediffmail" || terms[1] != "Krishnamurthy" {
		t.Errorf("keyterm_prompt = %v want [Rediffmail Krishnamurthy]", terms)
	}
}

func TestAssemblyAIHandleTurn_SuppressesDuplicateFinals(t *testing.T) {
	var calls atomic.Int32
	var lastIsFinal atomic.Bool
	c := &assemblyAIClient{
		onResult: func(r TranscriptResult) {
			calls.Add(1)
			lastIsFinal.Store(r.IsFinal)
		},
		suppressDuplicate: 5 * time.Second,
	}

	// First final
	c.handleTurn(assemblyAITurnMsg{Type: "Turn", Transcript: "hello world", EndOfTurn: true})
	if calls.Load() != 1 || !lastIsFinal.Load() {
		t.Fatalf("first final: got calls=%d final=%v", calls.Load(), lastIsFinal.Load())
	}
	// Same text repeated within the window — should be suppressed.
	c.handleTurn(assemblyAITurnMsg{Type: "Turn", Transcript: "hello world", EndOfTurn: true})
	if calls.Load() != 1 {
		t.Fatalf("duplicate final should be suppressed: calls=%d", calls.Load())
	}
	// Different text passes through.
	c.handleTurn(assemblyAITurnMsg{Type: "Turn", Transcript: "different", EndOfTurn: true})
	if calls.Load() != 2 {
		t.Fatalf("new final should fire: calls=%d", calls.Load())
	}
}

func TestAssemblyAIHandleTurn_PartialsDedupe(t *testing.T) {
	var calls atomic.Int32
	var lastText atomic.Value
	lastText.Store("")
	c := &assemblyAIClient{
		onResult: func(r TranscriptResult) {
			calls.Add(1)
			lastText.Store(r.Text)
		},
		suppressDuplicate: 5 * time.Second,
	}

	c.handleTurn(assemblyAITurnMsg{Transcript: "hi there", EndOfTurn: false})
	c.handleTurn(assemblyAITurnMsg{Transcript: "hi there", EndOfTurn: false}) // dup
	c.handleTurn(assemblyAITurnMsg{Transcript: "hi there ho", EndOfTurn: false})
	if calls.Load() != 2 {
		t.Fatalf("expected 2 partials, got %d", calls.Load())
	}
	if got := lastText.Load().(string); !strings.HasPrefix(got, "hi there ho") {
		t.Fatalf("last partial = %q", got)
	}
}

func TestAssemblyAIHandleTurn_EmptyTranscriptIgnored(t *testing.T) {
	var calls atomic.Int32
	c := &assemblyAIClient{
		onResult:          func(r TranscriptResult) { calls.Add(1) },
		suppressDuplicate: 5 * time.Second,
	}
	c.handleTurn(assemblyAITurnMsg{Transcript: "   ", EndOfTurn: true})
	c.handleTurn(assemblyAITurnMsg{Transcript: "", EndOfTurn: false})
	if calls.Load() != 0 {
		t.Fatalf("empty transcripts should not fire: calls=%d", calls.Load())
	}
}

func TestAssemblyAIHandleTurn_PropagatesFinalConfidence(t *testing.T) {
	var got TranscriptResult
	var calls atomic.Int32
	c := &assemblyAIClient{
		onResult: func(r TranscriptResult) {
			calls.Add(1)
			got = r
		},
		suppressDuplicate: 5 * time.Second,
	}
	c.handleTurn(assemblyAITurnMsg{
		Transcript: "hello world",
		EndOfTurn:  true,
		Words: []assemblyAIWord{
			{Confidence: 0.9},
			{Confidence: 0.8},
		},
	})
	if calls.Load() != 1 || !got.IsFinal {
		t.Fatalf("expected one final, got calls=%d result=%+v", calls.Load(), got)
	}
	want := 0.85
	if got.Confidence < want-1e-9 || got.Confidence > want+1e-9 {
		t.Fatalf("final confidence = %v, want %v (mean of word confidences)", got.Confidence, want)
	}
}

func TestAssemblyAIHandleTurn_PropagatesPartialConfidence(t *testing.T) {
	var got TranscriptResult
	c := &assemblyAIClient{
		onResult:          func(r TranscriptResult) { got = r },
		suppressDuplicate: 5 * time.Second,
	}
	c.handleTurn(assemblyAITurnMsg{
		Transcript: "hi there",
		EndOfTurn:  false,
		Words: []assemblyAIWord{
			{Confidence: 0.6},
			{Confidence: 0.4},
		},
	})
	if got.IsFinal {
		t.Fatalf("expected partial, got final: %+v", got)
	}
	want := 0.5
	if got.Confidence < want-1e-9 || got.Confidence > want+1e-9 {
		t.Fatalf("partial confidence = %v, want %v", got.Confidence, want)
	}
}

func TestAssemblyAIHandleTurn_MissingConfidenceFallsBackToZero(t *testing.T) {
	var got TranscriptResult
	var calls atomic.Int32
	c := &assemblyAIClient{
		onResult: func(r TranscriptResult) {
			calls.Add(1)
			got = r
		},
		suppressDuplicate: 5 * time.Second,
	}
	// No words array — emulates a provider message without per-word confidences.
	c.handleTurn(assemblyAITurnMsg{Transcript: "hello", EndOfTurn: true})
	if calls.Load() != 1 || !got.IsFinal {
		t.Fatalf("expected one final, got calls=%d result=%+v", calls.Load(), got)
	}
	if got.Text != "hello" {
		t.Fatalf("text = %q, want %q", got.Text, "hello")
	}
	if got.Confidence != 0 {
		t.Fatalf("missing-confidence final = %v, want 0", got.Confidence)
	}
}
