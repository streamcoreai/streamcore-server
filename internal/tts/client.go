package tts

import (
	"context"
	"fmt"

	"github.com/streamcoreai/server/internal/config"
)

// StreamChunk represents a chunk of synthesized PCM audio streamed from the
// TTS provider. The channel closes when synthesis is complete. If Err is
// non-nil the synthesis failed and no further chunks will arrive.
type StreamChunk struct {
	PCM []byte
	Err error
}

// Client synthesizes text to PCM audio (linear16, 16kHz mono).
type Client interface {
	// Synthesize produces the full audio for text in one shot.
	Synthesize(ctx context.Context, text string) ([]byte, error)

	// SynthesizeStream produces audio incrementally via a channel, so playback
	// can begin before the full utterance is ready. The channel is always
	// closed by the producer (even on error). Cancel ctx to abort early.
	//
	// Providers that return audio in a single envelope (a JSON body, say)
	// satisfy this by emitting one chunk — callers need not care which.
	SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error)
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
		return NewDeepgramClient(cfg.Deepgram.APIKey, cfg.Deepgram.TTSModel), nil
	case "cartesia":
		if cfg.Cartesia.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "cartesia", Field: "[cartesia] api_key"}
		}
		client := NewCartesiaWSClient(cfg.Cartesia.APIKey, cfg.Cartesia.VoiceID, cfg.Cartesia.WSURL)
		// Gate generations to the account's TTS concurrency limit so extra
		// requests queue locally instead of getting 429s. A negative limit
		// disables gating; the limiter is shared process-wide (one ceiling
		// per Cartesia key).
		if cfg.Cartesia.MaxConcurrency > 0 {
			client = newLimitedClient(client, sharedConcurrencyLimiter(cfg.Cartesia.MaxConcurrency))
		}
		return client, nil
	case "elevenlabs":
		if cfg.ElevenLabs.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "elevenlabs", Field: "[elevenlabs] api_key"}
		}
		return NewElevenLabsClient(cfg.ElevenLabs.APIKey, cfg.ElevenLabs.VoiceID, cfg.ElevenLabs.Model), nil
	case "speechify":
		if cfg.Speechify.APIKey == "" {
			return nil, ErrMissingAPIKey{Provider: "speechify", Field: "[speechify] api_key"}
		}
		return NewSpeechifyClient(cfg.Speechify.APIKey, cfg.Speechify.VoiceID, cfg.Speechify.Model), nil
	case "vibevoice":
		return NewVibeVoiceClient(cfg.VibeVoice.TTSURL, cfg.VibeVoice.Voice), nil
	default:
		return nil, fmt.Errorf("unknown tts provider %q (supported: cartesia, deepgram, elevenlabs, speechify, vibevoice)", cfg.TTS.Provider)
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

func (s *sanitizingClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	return s.inner.SynthesizeStream(ctx, SanitizeForTTS(text))
}

// SynthesizeStreamWithControls forwards per-utterance voice controls to the
// wrapped provider when it supports them, degrading to a plain stream when it
// doesn't — callers never need to know which provider is configured.
func (s *sanitizingClient) SynthesizeStreamWithControls(ctx context.Context, text string, vc VoiceControls) (<-chan StreamChunk, error) {
	if cs, ok := s.inner.(ControllableStreamer); ok {
		return cs.SynthesizeStreamWithControls(ctx, SanitizeForTTS(text), vc)
	}
	return s.inner.SynthesizeStream(ctx, SanitizeForTTS(text))
}
