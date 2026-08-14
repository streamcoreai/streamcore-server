package pipeline

import (
	"sync/atomic"

	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/llm"
)

// ConversationState is the part of a call that must outlive the transport
// carrying it: what was said, what the agent remembers of it, and the LLM
// client holding the message history.
//
// A Pipeline is bound to one peer's tracks and dies with them. This is not —
// the Session owns it, so a client that loses its connection and redials with
// a resume token gets a new Pipeline wired onto the *same* conversation
// instead of an agent that has forgotten who they are.
//
// Everything here is shared by pointer with the Pipeline rather than copied,
// so there is no snapshot to take at teardown — which matters, because a
// dropped connection never gets a clean teardown.
type ConversationState struct {
	// LLM holds the message history. Nil in realtime mode, where the
	// speech-to-speech provider keeps the history on its own side.
	LLM llm.Client

	// Log is the running record of the conversation, read by the rolling
	// summary and the low-confidence heuristics. Distinct from the LLM's own
	// history.
	Log *TranscriptLog

	// rollingSummary is the most recent background digest of older turns, so
	// a long call keeps its early context after the LLM history window has
	// dropped it. Pointers, so a new Pipeline shares them rather than
	// starting from a blank summary.
	rollingSummary       *atomic.Value // string
	lastSummaryAtEntries *atomic.Int32
}

// NewConversationState builds the durable half of a call.
//
// resourceID is who is on the call, empty when nobody was identified. It lives
// on the conversation rather than the Pipeline so it survives a redial, same as
// the message history.
//
// In realtime mode no LLM client is constructed: the speech-to-speech provider
// replaces STT, LLM, and TTS at once, and building one would demand API keys
// for a provider this deployment does not use.
func NewConversationState(cfg *config.Config, resourceID string) (*ConversationState, error) {
	state := &ConversationState{
		Log:                  &TranscriptLog{},
		rollingSummary:       &atomic.Value{},
		lastSummaryAtEntries: &atomic.Int32{},
	}

	if !cfg.RealtimeEnabled() {
		client, err := llm.NewClient(cfg, resourceID)
		if err != nil {
			return nil, err
		}
		state.LLM = client
	}

	return state, nil
}

// Summary returns the current rolling summary, empty if none has been
// generated yet.
func (c *ConversationState) Summary() string {
	s, _ := c.rollingSummary.Load().(string)
	return s
}

// HasHistory reports whether this state holds a conversation worth carrying
// across a reconnect. False in realtime mode, where the history lives inside
// the speech-to-speech provider and a new provider connection cannot inherit
// it.
func (c *ConversationState) HasHistory() bool {
	return c != nil && c.LLM != nil
}
