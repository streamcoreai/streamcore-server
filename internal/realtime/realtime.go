// Package realtime holds speech-to-speech providers: models that take caller
// audio in and return agent audio out, doing transcription, reasoning, and
// synthesis in a single hop.
//
// This replaces the STT -> LLM -> TTS chain rather than plugging into it. A
// realtime provider owns turn detection and interruption too, so the pipeline
// runs a much thinner loop around it (see pipeline.runRealtime).
package realtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// ToolDefinition describes a function the model may call. It mirrors
// llm.ToolDefinition so the plugin manager's tools map over unchanged.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Handlers receives everything the provider emits. Every callback is invoked
// from the provider's single read goroutine, so implementations must not
// block: hand work off to a channel or a goroutine instead.
type Handlers struct {
	// OnAudio delivers agent audio as linear16 PCM at audio.SampleRate,
	// mono. Chunk boundaries are arbitrary and do not align to frames.
	OnAudio func(pcm []byte)

	// OnUserTranscript reports what the caller said. Partials may revise
	// earlier text, so treat each call as replacing the current utterance
	// rather than appending to it.
	OnUserTranscript func(text string, final bool)

	// OnAgentTranscript reports the agent's own words as they are spoken.
	// Deltas are incremental.
	OnAgentTranscript func(delta string)

	// OnSpeechStarted fires when the provider's VAD detects the caller
	// starting to talk. The provider stops generating on its own side; the
	// pipeline still has to discard audio it has already buffered locally.
	OnSpeechStarted func()

	// OnSpeechStopped fires when the caller stops talking and the provider
	// begins working on a response. With server-side VAD this fires once per
	// detected speech segment, which may be several times per spoken turn.
	OnSpeechStopped func()

	// OnResponseStarted fires when the provider begins an agent turn. Unlike
	// OnSpeechStopped this happens once per response, so it marks the point
	// at which the caller's turn is definitively finished.
	OnResponseStarted func()

	// OnResponseDone fires when the agent has finished a turn.
	OnResponseDone func()

	// OnToolCall executes a function call. The returned string is sent back
	// to the model as the tool output; an error is reported to the model as
	// text so it can recover verbally instead of stalling.
	OnToolCall func(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// Options configures a session at connection time.
type Options struct {
	// Tools are the functions the model may call.
	Tools []ToolDefinition

	// InstructionsSuffix is appended to the configured system prompt. The
	// plugin manager's skill instructions arrive this way, mirroring
	// llm.Client.AppendSystemPrompt on the classic path.
	InstructionsSuffix string

	// Debug logs every server event type as it arrives. Providers differ in
	// how they segment a turn, and that shows up as duplicated or truncated
	// transcript messages; this is how you see the actual sequence.
	Debug bool
}

// Client is the interface that all speech-to-speech providers must implement.
type Client interface {
	// SendAudio streams caller audio as linear16 PCM at audio.SampleRate.
	SendAudio(pcm []byte) error

	// Speak makes the agent utter text unprompted, for greetings and other
	// server-initiated turns. The text is spoken, not responded to.
	Speak(text string) error

	// Close tears down the provider connection.
	Close() error
}

// NewClient returns a speech-to-speech client for the configured provider.
func NewClient(ctx context.Context, cfg *config.Config, opts Options, h Handlers) (Client, error) {
	switch cfg.Realtime.Provider {
	case "grok":
		if cfg.Grok.APIKey == "" {
			return nil, fmt.Errorf("realtime provider %q requires [grok] api_key to be set", cfg.Realtime.Provider)
		}
		return NewGrokClient(ctx, cfg.Grok, opts, h)
	default:
		return nil, fmt.Errorf("unknown realtime provider %q (supported: grok)", cfg.Realtime.Provider)
	}
}
