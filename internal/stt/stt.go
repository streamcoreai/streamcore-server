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

// PartialsEmitter is an optional capability interface for STT providers.
// A provider that streams interim (partial) transcripts while the caller
// is still talking implements it returning true; a finals-only provider —
// one whose transcripts arrive only once the caller has finished — returns
// false.
//
// The pipeline type-asserts a Client against this interface exactly once,
// at construction, and defaults to true when the provider does not
// implement it, so a provider silent on the question keeps the
// partials-driven behaviour. Returning false changes two things: live
// captions show finals only, and barge-in, which can no longer confirm
// real speech from partial text, degrades to firing on VAD alone once the
// full backchannel window has elapsed — with no text, a short burst
// inside the window cannot be classified as anything but backchannel.
type PartialsEmitter interface {
	EmitsPartials() bool
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
		return NewOpenAIClient(ctx, cfg.OpenAI.APIKey, cfg.OpenAI.STTModel, onResult)
	case "assemblyai":
		if cfg.AssemblyAI.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [assemblyai] api_key to be set", cfg.STT.Provider)
		}
		return NewAssemblyAIClient(ctx, cfg.AssemblyAI, onResult)
	case "aliyun":
		if cfg.Aliyun.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [aliyun] api_key to be set", cfg.STT.Provider)
		}
		return NewAliyunClient(ctx, cfg.Aliyun, onResult)
	case "volcengine":
		if cfg.Volcengine.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [volcengine] api_key to be set", cfg.STT.Provider)
		}
		return NewVolcengineClient(ctx, cfg.Volcengine, onResult)
	case "telnyx":
		if cfg.Telnyx.APIKey == "" {
			return nil, fmt.Errorf("stt provider %q requires [telnyx] api_key to be set", cfg.STT.Provider)
		}
		return NewTelnyxClient(ctx, cfg.Telnyx, onResult)
	case "vibevoice":
		return NewVibeVoiceClient(ctx, cfg.VibeVoice.ASRURL, onResult)
	case "moonshine":
		return NewMoonshineClient(ctx, cfg.Moonshine.STTURL, onResult)
	default:
		return nil, fmt.Errorf("unknown stt provider %q (supported: aliyun, assemblyai, deepgram, moonshine, openai, telnyx, vibevoice, volcengine)", cfg.STT.Provider)
	}
}
