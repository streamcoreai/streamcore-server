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
	Deepgram   DeepgramConfig   `toml:"deepgram"`
	OpenAI     OpenAIConfig     `toml:"openai"`
	Ollama     OllamaConfig     `toml:"ollama"`
	VibeVoice  VibeVoiceConfig  `toml:"vibevoice"`
	Cartesia   CartesiaConfig   `toml:"cartesia"`
	ElevenLabs ElevenLabsConfig `toml:"elevenlabs"`
}

type PluginsConfig struct {
	Directory string `toml:"directory"`
}

type PipelineConfig struct {
	BargeIn          *bool  `toml:"barge_in"`
	Greeting         string `toml:"greeting"`          // Text spoken by the agent when a user connects
	GreetingOutgoing string `toml:"greeting_outgoing"` // Text spoken on outgoing SIP calls (falls back to greeting)
	Debug            bool   `toml:"debug"`             // Emit per-turn timing events over the DataChannel
}

type ServerConfig struct {
	Port string `toml:"port"`
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
}

type ElevenLabsConfig struct {
	APIKey  string `toml:"api_key"`
	VoiceID string `toml:"voice_id"`
	Model   string `toml:"model"`
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
	setDefault(&cfg.LLM.Provider, "openai")
	setDefault(&cfg.TTS.Provider, "cartesia")
	setDefault(&cfg.OpenAI.Model, "gpt-4o-mini")
	setDefault(&cfg.OpenAI.SystemPrompt, "You are a helpful AI voice assistant. Keep your responses concise and conversational.")
	setDefault(&cfg.Ollama.BaseURL, "http://localhost:11434")
	setDefault(&cfg.Ollama.Model, "llama3.2")
	setDefault(&cfg.Ollama.SystemPrompt, "You are a helpful AI voice assistant. Keep your responses concise and conversational.")
	setDefault(&cfg.VibeVoice.ASRURL, "ws://127.0.0.1:8200")
	setDefault(&cfg.VibeVoice.TTSURL, "http://127.0.0.1:8300")
	setDefault(&cfg.VibeVoice.Voice, "en-Emma_woman")

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
