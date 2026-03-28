package tts

import (
	"context"
	"fmt"

	"github.com/streamcoreai/server/internal/config"
)

// Client synthesizes text to PCM audio (linear16, 48kHz).
type Client interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

// NewClient returns a TTS client for the configured provider.
func NewClient(cfg *config.Config) (Client, error) {
	switch cfg.TTS.Provider {
	case "deepgram":
		if cfg.Deepgram.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "deepgram", Field: "[deepgram] api_key"}
		}
		return NewDeepgramClient(cfg.Deepgram.APIKey), nil
	case "cartesia":
		if cfg.Cartesia.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "cartesia", Field: "[cartesia] api_key"}
		}
		return NewCartesiaClient(cfg.Cartesia.APIKey, cfg.Cartesia.VoiceID), nil
	case "elevenlabs":
		if cfg.ElevenLabs.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "elevenlabs", Field: "[elevenlabs] api_key"}
		}
		return NewElevenLabsClient(cfg.ElevenLabs.APIKey, cfg.ElevenLabs.VoiceID, cfg.ElevenLabs.Model), nil
	default:
		return nil, fmt.Errorf("unknown tts provider %q (supported: cartesia, deepgram, elevenlabs)", cfg.TTS.Provider)
	}
}
