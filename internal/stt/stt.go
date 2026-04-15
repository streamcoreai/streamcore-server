package stt

import (
	"context"
	"fmt"

	"github.com/streamcoreai/server/internal/config"
)

type TranscriptResult struct {
	Text    string
	IsFinal bool
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
	case "vibevoice":
		return NewVibeVoiceClient(ctx, cfg.VibeVoice.ASRURL, onResult)
	default:
		return nil, fmt.Errorf("unknown stt provider %q (supported: deepgram, openai, vibevoice)", cfg.STT.Provider)
	}
}
