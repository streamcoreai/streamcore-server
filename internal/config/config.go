package config

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the top-level application configuration. Each provider (Deepgram,
// OpenAI, Cartesia, etc.) has its own section. The [stt], [llm], and [tts]
// sections select which provider to use for each role.
//
// Setting [realtime] provider switches the pipeline to a speech-to-speech
// model that replaces all three roles at once; [stt], [llm], and [tts] are
// then unused.
type Config struct {
	Server     ServerConfig     `toml:"server"`
	Plugins    PluginsConfig    `toml:"plugins"`
	Pipeline   PipelineConfig   `toml:"pipeline"`
	Realtime   RealtimeConfig   `toml:"realtime"`
	Grok       GrokConfig       `toml:"grok"`
	STT        STTConfig        `toml:"stt"`
	LLM        LLMConfig        `toml:"llm"`
	TTS        TTSConfig        `toml:"tts"`
	RAG        RAGConfig        `toml:"rag"`
	Deepgram   DeepgramConfig   `toml:"deepgram"`
	AssemblyAI AssemblyAIConfig `toml:"assemblyai"`
	OpenAI     OpenAIConfig     `toml:"openai"`
	Ollama     OllamaConfig     `toml:"ollama"`
	VibeVoice  VibeVoiceConfig  `toml:"vibevoice"`
	Cartesia   CartesiaConfig   `toml:"cartesia"`
	ElevenLabs ElevenLabsConfig `toml:"elevenlabs"`
	Speechify  SpeechifyConfig  `toml:"speechify"`
	MiniMax    MiniMaxConfig    `toml:"minimax"`
	MiMo       MiMoConfig       `toml:"mimo"`
	Pgvector   PgvectorConfig   `toml:"pgvector"`
	Supabase   SupabaseConfig   `toml:"supabase"`
}

type PluginsConfig struct {
	Directory string `toml:"directory"`
}

type PipelineConfig struct {
	BargeIn          *bool  `toml:"barge_in"`
	Greeting         string `toml:"greeting"`          // Text spoken by the agent when a user connects
	GreetingOutgoing string `toml:"greeting_outgoing"` // Text spoken on outgoing SIP calls (falls back to greeting)
	Debug            bool   `toml:"debug"`             // Emit per-turn timing events over the DataChannel

	// UserSpeechQuietMs is how long the caller must be quiet before the agent
	// starts speaking. It stops the agent talking over someone who is still
	// mid-thought after the transcript went final.
	UserSpeechQuietMs int `toml:"user_speech_quiet_ms"`

	// TurnMergeMs is the debounce window for merging consecutive final
	// transcripts into a single turn. A caller who pauses mid-sentence
	// ("I want to... um... book a table") otherwise fires two turns and the
	// agent answers the first half.
	TurnMergeMs int `toml:"turn_merge_ms"`

	// ReadbackBargeInGuardEnabled suppresses weak correction/backchannel
	// barge-ins ("yeah", "mm-hm", "no that's wrong") while the agent is
	// reading values back for confirmation. Only strong commands (stop,
	// cancel, hang up) cut through. Default off, preserving existing
	// barge-in behaviour.
	ReadbackBargeInGuardEnabled bool `toml:"readback_bargein_guard_enabled"`

	// RAGPrefetch starts retrieval speculatively during the turn-merge
	// window so embedding and vector search overlap the debounce instead of
	// adding to it.
	RAGPrefetch bool `toml:"rag_prefetch"`
}

type ServerConfig struct {
	Port       string `toml:"port"`
	PublicIP   string `toml:"public_ip"`   // Public IP for ICE candidates when behind NAT (e.g., EC2). Leave empty for local/direct connections.
	TurnSecret string `toml:"turn_secret"` // Shared secret for the built-in TURN server. Required when public_ip is set.
	JWTSecret  string `toml:"jwt_secret"`  // Shared secret for HMAC-SHA256 JWT validation on /whip. Leave empty to disable auth.
	APIKey     string `toml:"api_key"`     // API key required to call /token. Leave empty to allow unauthenticated token generation.

	// SessionGraceMs is how long a session with no connected peers is kept
	// before the reaper closes it. The window is what lets a client recover a
	// dropped connection — via ICE restart or a redial — without losing the
	// conversation. Too short and a network handover costs the call; too long
	// and abandoned sessions pile up. Defaults to 30000.
	SessionGraceMs int `toml:"session_grace_ms"`
}

// RealtimeConfig selects a speech-to-speech provider. When Provider is set,
// the pipeline runs in realtime mode: audio goes straight to the model and
// comes back as audio, so [stt], [llm], and [tts] are bypassed entirely.
// Empty (the default) keeps the classic STT -> LLM -> TTS pipeline.
type RealtimeConfig struct {
	Provider string `toml:"provider"`
}

// GrokConfig configures xAI's Grok speech-to-speech model over the Realtime
// WebSocket API (wss://api.x.ai/v1/realtime). Used when realtime.provider is
// "grok".
type GrokConfig struct {
	APIKey string `toml:"api_key"`
	// Model is the voice model. "grok-voice-latest" always tracks the newest
	// one; pin a versioned name (e.g. "grok-voice-think-fast-2.0") in
	// production so a model change never lands unannounced.
	Model string `toml:"model"`
	// Voice is a built-in voice ID (lowercase, e.g. "eve") or a custom voice
	// ID from the Custom Voices API. Empty uses the model default.
	Voice string `toml:"voice"`
	// SystemPrompt maps to the session `instructions`. Grok voice models are
	// strong enough that a short prompt beats a ported-over GPT one — xAI
	// explicitly recommends stripping workaround prompting.
	SystemPrompt string `toml:"system_prompt"`
	// ReasoningEffort is "high" (default, better on multi-step instructions
	// and ambiguous queries) or "none" (lower latency).
	ReasoningEffort string `toml:"reasoning_effort"`

	// --- Server-side turn detection ---

	// VADThreshold is the activation threshold in 0.1-0.9. Higher demands
	// louder audio to trigger a turn. Zero leaves the provider default (0.85).
	VADThreshold float64 `toml:"vad_threshold"`
	// SilenceDurationMs is how long the caller must be silent before the
	// server ends their turn (0-10000). Zero leaves the provider default.
	SilenceDurationMs int `toml:"silence_duration_ms"`
	// PrefixPaddingMs is how much audio before the detected speech onset to
	// include, so the first word is not clipped. Zero leaves the default (333).
	PrefixPaddingMs int `toml:"prefix_padding_ms"`
	// IdleTimeoutMs makes the agent proactively re-engage after this much
	// silence. Zero disables the proactive check-in.
	IdleTimeoutMs int `toml:"idle_timeout_ms"`

	// --- Input transcription (for the client-facing transcript only) ---

	// Transcription enables grok-transcribe on the caller's audio so the
	// DataChannel still receives user transcripts. The model itself hears the
	// audio directly and does not need this; it costs an extra transcription
	// pass. Nil means enabled.
	Transcription *bool `toml:"transcription"`
	// LanguageHint biases transcription to a BCP-47 language. Spanish and
	// Portuguese need a regional variant ("es-MX", "pt-BR"); bare "es"/"pt"
	// are rejected. Unrecognised codes fall back to auto-detection.
	LanguageHint string `toml:"language_hint"`
	// Keyterms bias transcription toward domain vocabulary, mirroring the
	// Deepgram and AssemblyAI options. Up to 100 terms, 50 chars each.
	Keyterms []string `toml:"keyterms"`

	// --- Server-side tools ---

	// WebSearch and XSearch enable xAI's hosted search tools. These run on
	// xAI's side and need no local plugin.
	WebSearch bool `toml:"web_search"`
	XSearch   bool `toml:"x_search"`
}

type STTConfig struct {
	Provider string `toml:"provider"`
}

type LLMConfig struct {
	Provider string `toml:"provider"`
}

type TTSConfig struct {
	Provider string `toml:"provider"`
}

type DeepgramConfig struct {
	APIKey string `toml:"api_key"`
	Model  string `toml:"model"`
	// Language is a BCP-47 tag (e.g. "en-US", "es-MX"). It is mapped to the
	// language code the Nova-3 model expects; anything outside en/es becomes
	// "multi". Empty defaults to English.
	Language string `toml:"language"`
	// Endpointing is how long (ms, as a string) Deepgram waits for silence
	// before finalising a transcript. Lower is snappier but fragments natural
	// mid-sentence pauses ("I want to… um… order a pizza").
	Endpointing string `toml:"endpointing"`
	// UtteranceEndMs asks Deepgram to emit an UtteranceEnd event after this
	// much silence, which the callback uses to flush buffered chunks when no
	// speech_final arrives. Deepgram requires >= 1000 and interim results.
	UtteranceEndMs string `toml:"utterance_end_ms"`
	// Keyterms bias the Nova-3 decoder toward domain vocabulary that is
	// phonetically ambiguous against common English (product names, place
	// names). Ignored by other models.
	Keyterms []string `toml:"keyterms"`
	// TTSModel selects the Aura voice used when tts.provider = "deepgram".
	// Model is STT-only, so TTS gets its own field on the same section (both
	// roles share one API key). Empty defaults to aura-2-thalia-en.
	TTSModel string `toml:"tts_model"`
}

type AssemblyAIConfig struct {
	APIKey string `toml:"api_key"`
	// Model selects the Universal-Streaming speech model. Common values:
	//   - "u3-rt-pro" (Universal-3 Pro, strongest entity capture)
	//   - "u3-rt"     (Universal-3 baseline, cheaper)
	Model string `toml:"model"`
	// Language is a BCP-47 tag; the region is stripped before it is sent as
	// AssemblyAI's `language_code` (which expects ISO 639-1). Empty lets the
	// model auto-detect.
	Language string `toml:"language"`
	// FormatTurns enables auto-formatted text (capitalisation, punctuation)
	// on the final turn. Nil means enabled.
	FormatTurns *bool `toml:"format_turns"`
	// EndOfTurnSilenceMs tunes how long the model waits before declaring the
	// turn over. Zero leaves the provider default.
	EndOfTurnSilenceMs int `toml:"end_of_turn_silence_ms"`
	// Keyterms are passed as repeated keyterm_prompt parameters to bias the
	// decoder, mirroring the Deepgram option.
	Keyterms []string `toml:"keyterms"`
}

type OpenAIConfig struct {
	APIKey       string `toml:"api_key"`
	Model        string `toml:"model"`
	SystemPrompt string `toml:"system_prompt"`
}

type OllamaConfig struct {
	BaseURL      string `toml:"base_url"`
	Model        string `toml:"model"`
	SystemPrompt string `toml:"system_prompt"`
}

type CartesiaConfig struct {
	APIKey  string `toml:"api_key"`
	VoiceID string `toml:"voice_id"`
	// WSURL overrides the Cartesia WebSocket endpoint. Empty (the default)
	// talks to wss://api.cartesia.ai/tts/websocket.
	WSURL string `toml:"ws_url"`
	// MaxConcurrency caps simultaneous in-flight generations. Cartesia counts
	// concurrency by active generation context — not by call or connection —
	// and returns 429 past the account limit, so set this to your plan's TTS
	// concurrency limit and extra requests queue locally instead of failing.
	// 0 (unset) defaults to 3; a negative value disables the limiter.
	MaxConcurrency int `toml:"max_concurrency"`
}

type ElevenLabsConfig struct {
	APIKey  string `toml:"api_key"`
	VoiceID string `toml:"voice_id"`
	Model   string `toml:"model"`
}

type SpeechifyConfig struct {
	APIKey  string `toml:"api_key"`
	VoiceID string `toml:"voice_id"`
	Model   string `toml:"model"`
}

type MiMoConfig struct {
	APIKey string `toml:"api_key"`
	// Voice is a built-in voice name, passed through verbatim — the Chinese
	// set (茉莉, 冰糖, 苏打, 白桦 …) and the English one (Mia, Chloe, Milo,
	// Dean …) share this field.
	Voice   string `toml:"voice"`
	Model   string `toml:"model"`
	BaseURL string `toml:"base_url"`
}

type MiniMaxConfig struct {
	APIKey  string `toml:"api_key"`
	VoiceID string `toml:"voice_id"`
	Model   string `toml:"model"`
	// BaseURL overrides the API host. Empty uses the global endpoint;
	// mainland-China accounts need https://api.minimaxi.com/v1.
	BaseURL string `toml:"base_url"`
}

type RAGConfig struct {
	Provider       string `toml:"provider"`        // "pgvector", "supabase", or "" (disabled)
	TopK           int    `toml:"top_k"`           // Number of chunks to retrieve (default 3)
	EmbeddingModel string `toml:"embedding_model"` // OpenAI embedding model (default text-embedding-3-small)
}

type PgvectorConfig struct {
	ConnectionString string `toml:"connection_string"`
	Table            string `toml:"table"` // Table name (default "documents")
}

type SupabaseConfig struct {
	URL      string `toml:"url"`      // Supabase project URL
	APIKey   string `toml:"api_key"`  // Supabase anon or service_role key
	Function string `toml:"function"` // RPC function name (default "match_documents")
	Table    string `toml:"table"`    // Table name (default "documents"), used by streamcore-cli for ingestion
}

type VibeVoiceConfig struct {
	ASRURL string `toml:"asr_url"` // WebSocket URL for the ASR server
	TTSURL string `toml:"tts_url"` // HTTP URL for the TTS server
	Voice  string `toml:"voice"`   // TTS voice name
}

// Load reads configuration from a TOML file. It tries the given path first,
// then falls back to "config.toml" in the working directory.
func Load(path string) (*Config, error) {
	if path == "" {
		path = "config.toml"
	}

	cfg := &Config{}
	if _, err := os.Stat(path); err == nil {
		metadata, err := toml.DecodeFile(path, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", path, err)
		}
		if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
			keys := make([]string, len(undecoded))
			for i, key := range undecoded {
				keys[i] = key.String()
			}
			sort.Strings(keys)
			log.Printf("Warning: unknown config key(s): %s — check for a typo", strings.Join(keys, ", "))
		}
	}

	// Apply defaults
	setDefault(&cfg.Server.Port, "8080")
	if cfg.Server.SessionGraceMs == 0 {
		cfg.Server.SessionGraceMs = 30000
	}
	setDefault(&cfg.STT.Provider, "deepgram")
	setDefault(&cfg.Deepgram.Model, "nova-3")
	// 300ms keeps the historical snappy endpointing. utterance_end_ms has no
	// prior value; 1000 is Deepgram's minimum and switches on the UtteranceEnd
	// flush that recovers a turn when no speech_final arrives.
	setDefault(&cfg.Deepgram.Endpointing, "300")
	setDefault(&cfg.Deepgram.UtteranceEndMs, "1000")
	setDefault(&cfg.Deepgram.TTSModel, "aura-2-thalia-en")
	setDefault(&cfg.AssemblyAI.Model, "u3-rt-pro")
	// Cartesia counts concurrency by active generation context; 3 comfortably
	// serves many concurrent calls because agent speech is bursty and
	// half-duplex. Negative disables the limiter.
	if cfg.Cartesia.MaxConcurrency == 0 {
		cfg.Cartesia.MaxConcurrency = 3
	}
	setDefault(&cfg.LLM.Provider, "openai")
	setDefault(&cfg.TTS.Provider, "cartesia")
	setDefault(&cfg.OpenAI.Model, "gpt-4o-mini")
	setDefault(&cfg.OpenAI.SystemPrompt, "You are a helpful AI voice assistant having a natural phone conversation. Keep responses to 1-2 sentences unless asked for detail. When interrupted (indicated by bracketed context), respond the way a patient human would: if they say 'no', address the disagreement; if they redirect, follow their lead. Never repeat what you already said, never ask 'would you like me to continue', and never mention that you were interrupted.")
	setDefault(&cfg.Ollama.BaseURL, "http://localhost:11434")
	setDefault(&cfg.Ollama.Model, "llama3.2")
	setDefault(&cfg.Ollama.SystemPrompt, "You are a helpful AI voice assistant having a natural phone conversation. Keep responses to 1-2 sentences unless asked for detail. When interrupted (indicated by bracketed context), respond the way a patient human would: if they say 'no', address the disagreement; if they redirect, follow their lead. Never repeat what you already said, never ask 'would you like me to continue', and never mention that you were interrupted.")
	setDefault(&cfg.VibeVoice.ASRURL, "ws://127.0.0.1:8200")
	setDefault(&cfg.VibeVoice.TTSURL, "http://127.0.0.1:8300")
	setDefault(&cfg.VibeVoice.Voice, "en-Emma_woman")

	// Quiet grace and turn-merge debounce. Both default to conservative values
	// that measurably reduce the agent talking over a caller mid-thought.
	if cfg.Pipeline.UserSpeechQuietMs == 0 {
		cfg.Pipeline.UserSpeechQuietMs = 600
	}
	if cfg.Pipeline.TurnMergeMs == 0 {
		cfg.Pipeline.TurnMergeMs = 350
	}

	// Default barge-in to true if not explicitly set
	if cfg.Pipeline.BargeIn == nil {
		t := true
		cfg.Pipeline.BargeIn = &t
	}

	// Realtime (speech-to-speech) defaults.
	setDefault(&cfg.Grok.Model, "grok-voice-latest")
	setDefault(&cfg.Grok.Voice, "eve")
	setDefault(&cfg.Grok.ReasoningEffort, "high")
	setDefault(&cfg.Grok.SystemPrompt, "You are a helpful AI voice assistant on a phone call. Keep responses short and conversational.")
	if cfg.Grok.Transcription == nil {
		t := true
		cfg.Grok.Transcription = &t
	}

	if err := cfg.validateRealtime(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// RealtimeEnabled reports whether the pipeline should run in speech-to-speech
// mode, bypassing the STT, LLM, and TTS providers.
func (c *Config) RealtimeEnabled() bool {
	return c.Realtime.Provider != "" && c.Realtime.Provider != "none"
}

// validateRealtime rejects a bad speech-to-speech configuration at startup.
// Without this a typo'd provider or a missing key only surfaces when the
// first caller connects, which reads as a broken deployment rather than a
// misconfigured one.
func (c *Config) validateRealtime() error {
	if !c.RealtimeEnabled() {
		return nil
	}

	switch c.Realtime.Provider {
	case "grok":
		if c.Grok.APIKey == "" {
			return fmt.Errorf("realtime.provider = %q requires [grok] api_key to be set", c.Realtime.Provider)
		}
	default:
		return fmt.Errorf("unknown realtime provider %q (supported: grok)", c.Realtime.Provider)
	}

	if e := c.Grok.ReasoningEffort; e != "high" && e != "none" {
		return fmt.Errorf("[grok] reasoning_effort = %q must be \"high\" or \"none\"", e)
	}
	// Out-of-range values are rejected by the provider mid-call, which is a
	// far worse place to discover them.
	if t := c.Grok.VADThreshold; t != 0 && (t < 0.1 || t > 0.9) {
		return fmt.Errorf("[grok] vad_threshold = %v must be between 0.1 and 0.9", t)
	}
	if ms := c.Grok.SilenceDurationMs; ms < 0 || ms > 10000 {
		return fmt.Errorf("[grok] silence_duration_ms = %d must be between 0 and 10000", ms)
	}
	if ms := c.Grok.PrefixPaddingMs; ms < 0 || ms > 10000 {
		return fmt.Errorf("[grok] prefix_padding_ms = %d must be between 0 and 10000", ms)
	}
	if n := len(c.Grok.Keyterms); n > 100 {
		return fmt.Errorf("[grok] keyterms has %d entries; the maximum is 100", n)
	}

	return nil
}

func setDefault(field *string, fallback string) {
	if *field == "" {
		*field = fallback
	}
}
