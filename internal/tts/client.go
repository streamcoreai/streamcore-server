package tts

import (
	"context"
	"fmt"

	"github.com/streamcoreai/server/internal/config"
)

// Client synthesizes text to PCM audio (linear16, 16kHz mono).
type Client interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

// NewClient returns a TTS client for the configured provider. The
// returned client wraps the provider in a sanitizer that strips Markdown
// formatting from every input — TTS engines read characters like '*' and
// '#' aloud literally, so RAG snippets or LLM-emitted markdown must be
// scrubbed before synthesis.
func NewClient(cfg *config.Config) (Client, error) {
	inner, err := newProviderClient(cfg)
	if err != nil {
		return nil, err
	}
	return &sanitizingClient{inner: inner}, nil
}

func newProviderClient(cfg *config.Config) (Client, error) {
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
	case "vibevoice":
		return NewVibeVoiceClient(cfg.VibeVoice.TTSURL, cfg.VibeVoice.Voice), nil
	default:
		return nil, fmt.Errorf("unknown tts provider %q (supported: cartesia, deepgram, elevenlabs, vibevoice)", cfg.TTS.Provider)
	}
}

// sanitizingClient strips Markdown from every synthesis request before
// handing it to the wrapped provider.
type sanitizingClient struct {
	inner Client
}

func (s *sanitizingClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	return s.inner.Synthesize(ctx, SanitizeForTTS(text))
}
