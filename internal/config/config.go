package config

import (
	"fmt"
	"os"
	"path/filepath"

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
	Cartesia   CartesiaConfig   `toml:"cartesia"`
	ElevenLabs ElevenLabsConfig `toml:"elevenlabs"`
	Mediasoup  MediasoupConfig  `toml:"mediasoup"`
}

type MediasoupConfig struct {
	SignalingURL string `toml:"signaling_url"` // HTTP API base URL of the mediasoup server (e.g. "https://localhost:4443")
	RoomID       string `toml:"room_id"`       // Room to join on the mediasoup server
	OriginHeader string `toml:"origin_header"` // Origin header required by the mediasoup API server
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

type CartesiaConfig struct {
	APIKey  string `toml:"api_key"`
	VoiceID string `toml:"voice_id"`
}

type ElevenLabsConfig struct {
	APIKey  string `toml:"api_key"`
	VoiceID string `toml:"voice_id"`
	Model   string `toml:"model"`
}

// Load reads configuration from a TOML file. Resolution order:
//  1. Explicit path argument
//  2. CONFIG_PATH environment variable
//  3. "config.toml" in the working directory or any parent (up to 5 levels)
func Load(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("CONFIG_PATH")
	}
	if path == "" {
		path = findConfigUpward("config.toml", 5)
	}

	cfg := &Config{}
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			if _, err := toml.DecodeFile(path, cfg); err != nil {
				return nil, fmt.Errorf("failed to parse %s: %w", path, err)
			}
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
	setDefault(&cfg.Mediasoup.SignalingURL, "https://localhost:4443")
	setDefault(&cfg.Mediasoup.RoomID, "default-room")
	setDefault(&cfg.Mediasoup.OriginHeader, "https://localhost:4443")

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

// findConfigUpward walks up from the current working directory looking for
// filename, checking up to maxLevels parent directories.
func findConfigUpward(filename string, maxLevels int) string {
	dir, err := os.Getwd()
	if err != nil {
		return filename // fall back to cwd-relative
	}
	for i := 0; i <= maxLevels; i++ {
		candidate := filepath.Join(dir, filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "" // not found
}
