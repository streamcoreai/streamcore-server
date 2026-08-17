package stt

import (
	"context"
	"fmt"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

type TranscriptResult struct {
	Text    string
	IsFinal bool
	// Confidence is the provider-reported confidence for the transcript in the
	// range [0, 1]. Zero means the provider did not report a value (or the
	// reported value was genuinely zero); callers must treat it as "unknown"
	// rather than "low confidence".
	Confidence float64
}

// Client is the interface that all STT providers must implement.
type Client interface {
	SendAudio(data []byte) error
	Close()
}

// NewClient returns an STT client for the configured provider.
func NewClient(ctx context.Context, cfg *config.Config, onResult func(TranscriptResult)) (Client, error) {
	switch cfg.STT.Provider {
	case "deepgram":
		if cfg.Deepgram.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [deepgram] api_key to be set", cfg.STT.Provider)
		}
		return NewDeepgramClient(ctx, cfg.Deepgram, onResult)
	case "openai":
		if cfg.OpenAI.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [openai] api_key to be set", cfg.STT.Provider)
		}
		return NewOpenAIClient(ctx, cfg.OpenAI.APIKey, onResult)
	case "assemblyai":
		if cfg.AssemblyAI.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [assemblyai] api_key to be set", cfg.STT.Provider)
		}
		return NewAssemblyAIClient(ctx, cfg.AssemblyAI, onResult)
	case "volcengine":
		if cfg.Volcengine.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [volcengine] api_key to be set", cfg.STT.Provider)
		}
		return NewVolcengineClient(ctx, cfg.Volcengine, onResult)
	case "vibevoice":
		return NewVibeVoiceClient(ctx, cfg.VibeVoice.ASRURL, onResult)
	default:
		return nil, fmt.Errorf("unknown stt provider %q (supported: deepgram, openai, assemblyai, vibevoice, volcengine)", cfg.STT.Provider)
	}
}
