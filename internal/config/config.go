package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Config is the top-level application configuration. Each provider (Deepgram,
// OpenAI, Cartesia, etc.) has its own section. The [stt], [llm], and [tts]
// sections select which provider to use for each role.
type Config struct {
	Server     ServerConfig     `toml:"server"`
	Plugins    PluginsConfig    `toml:"plugins"`
	Pipeline   PipelineConfig   `toml:"pipeline"`
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
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", path, err)
		}
	}

	// Apply defaults
	setDefault(&cfg.Server.Port, "8080")
	setDefault(&cfg.STT.Provider, "deepgram")
	setDefault(&cfg.Deepgram.Model, "nova-3")
	// 300ms keeps the historical snappy endpointing. utterance_end_ms has no
	// prior value; 1000 is Deepgram's minimum and switches on the UtteranceEnd
	// flush that recovers a turn when no speech_final arrives.
	setDefault(&cfg.Deepgram.Endpointing, "300")
	setDefault(&cfg.Deepgram.UtteranceEndMs, "1000")
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

	return cfg, nil
}

func setDefault(field *string, fallback string) {
	if *field == "" {
		*field = fallback
	}
}
