package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

func TestIsAcknowledgementOnly(t *testing.T) {
	tests := []struct {
		text string
		want bool
		why  string
	}{
		// Pure listening noises — the reported case and its variants.
		{"Okay.", true, "the reported case"},
		{"okay", true, "bare acknowledgement"},
		{"yeah, okay", true, "stacked acknowledgements"},
		{"mm-hmm", true, "hummed backchannel"},
		{"got it", true, "multi-word acknowledgement"},
		{"right, sure", true, "stacked acknowledgements"},
		{"I see", true, "acknowledgement phrased as a clause"},
		{"exactly", true, "agreement"},

		// Single tokens that carry intent. These are the reason this predicate
		// cannot reuse isMeaningfulBargeInTranscript, which requires >= 2
		// tokens and would swallow every one of them.
		{"why?", false, "one-word question"},
		{"no", false, "correction"},
		{"stop", false, "command"},
		{"wait", false, "command"},
		{"what?", false, "request to repeat"},
		{"help", false, "request"},

		// Anything with content.
		{"okay but what about billing", false, "acknowledgement plus a question"},
		{"yeah actually change that", false, "acknowledgement plus a correction"},
		{"can you repeat that", false, "real request"},
		{"", false, "empty text is not an acknowledgement"},
	}

	for _, tc := range tests {
		if got := isAcknowledgementOnly(tc.text); got != tc.want {
			t.Errorf("isAcknowledgementOnly(%q) = %v, want %v (%s)", tc.text, got, tc.want, tc.why)
		}
	}
}

// The reported bug: "Okay." spoken over the agent's explanation started a whole
// new LLM turn whose audio played on top of the sentences still in flight.
func TestPassiveAcknowledgementOverAgentSpeechIsIgnored(t *testing.T) {
	p := newResponseStatePipeline(nil)
	p.interruptedText.Store("")

	ev := TranscriptEvent{Text: "Okay.", Final: true, OverAgentSpeech: true}
	if !p.isPassiveAcknowledgement(ev) {
		t.Fatal(`"Okay." over agent speech should not start a new turn`)
	}
}

// The same words said into silence are the caller handing the floor back.
func TestAcknowledgementInSilenceStillGetsAnswered(t *testing.T) {
	p := newResponseStatePipeline(nil)
	p.interruptedText.Store("")

	ev := TranscriptEvent{Text: "Okay.", Final: true, OverAgentSpeech: false}
	if p.isPassiveAcknowledgement(ev) {
		t.Fatal(`"Okay." said into silence is a real turn and must be answered`)
	}
}

// After a confirmed barge-in the agent has already been cut off mid-sentence.
// Swallowing the turn would leave the caller listening to nothing.
func TestAcknowledgementAfterBargeInIsAnswered(t *testing.T) {
	p := newResponseStatePipeline(nil)
	p.interruptedText.Store("the plugin system allows for extensibility")

	ev := TranscriptEvent{Text: "yeah okay", Final: true, OverAgentSpeech: true}
	if p.isPassiveAcknowledgement(ev) {
		t.Fatal("suppressing this turn leaves dead air — the agent was already cut off")
	}
}

// Intent spoken over the agent must always reach the LLM, however short.
func TestMeaningfulTurnOverAgentSpeechIsNeverSuppressed(t *testing.T) {
	p := newResponseStatePipeline(nil)
	p.interruptedText.Store("")

	for _, text := range []string{"no", "stop", "why?", "okay but wait", "hang on"} {
		ev := TranscriptEvent{Text: text, Final: true, OverAgentSpeech: true}
		if p.isPassiveAcknowledgement(ev) {
			t.Errorf("%q was suppressed but carries intent", text)
		}
	}
}

// The talking-over flag has to be sampled while the agent is still audible.
// STT endpointing delays a final by several hundred ms, so the flag must
// survive the agent finishing in the meantime.
func TestAgentSpeechRecentCoversEndpointingLag(t *testing.T) {
	p := newResponseStatePipeline(nil)

	if p.agentSpeechRecent() {
		t.Fatal("a pipeline that has never spoken should not report recent speech")
	}

	p.setSpeaking(true)
	if !p.agentSpeechRecent() {
		t.Fatal("agent is speaking right now")
	}

	p.setSpeaking(false)
	if !p.agentSpeechRecent() {
		t.Fatal("speech that just ended must still count — the STT final is in flight")
	}

	// Rewind the stamp past the grace window.
	p.speechEndedAt.Store(time.Now().Add(-2 * backchannelGrace).UnixNano())
	if p.agentSpeechRecent() {
		t.Fatal("speech that ended long ago must not count as talking-over")
	}
}

// setSpeaking must only stamp on a true → false transition; a redundant
// clear would otherwise keep pushing the grace window forward forever.
func TestSetSpeakingStampsOnlyOnTransition(t *testing.T) {
	p := newResponseStatePipeline(nil)

	p.setSpeaking(true)
	p.setSpeaking(false)
	first := p.speechEndedAt.Load()
	if first == 0 {
		t.Fatal("ending speech should stamp speechEndedAt")
	}

	p.setSpeaking(false)
	if p.speechEndedAt.Load() != first {
		t.Fatal("a redundant clear re-stamped the timestamp, extending the grace window")
	}
}

// A turn that begins over the agent's voice stays flagged through the turn
// buffer even when its later halves are merged in after the agent has stopped
// — which is the common shape, since the merge window runs for hundreds of ms
// past the first final.
func TestTurnBufferKeepsOverAgentSpeechAcrossMerge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := newResponseStatePipeline(nil)
	p.ctx = ctx
	p.cfg = &config.Config{}
	p.cfg.Pipeline.TurnMergeMs = 60
	p.finalCh = make(chan TranscriptEvent, transcriptChSize)
	p.transcriptCh = make(chan TranscriptEvent, transcriptChSize)

	go p.runTurnBuffer()

	// Caller starts over the agent's voice, then continues after it stops.
	p.finalCh <- TranscriptEvent{Text: "okay", Final: true, OverAgentSpeech: true}
	p.finalCh <- TranscriptEvent{Text: "sure", Final: true, OverAgentSpeech: false}

	select {
	case merged := <-p.transcriptCh:
		if merged.Text != "okay sure" {
			t.Fatalf("merged text = %q, want %q", merged.Text, "okay sure")
		}
		if !merged.OverAgentSpeech {
			t.Fatal("merged turn lost the talking-over flag from its first half")
		}
		if !isAcknowledgementOnly(merged.Text) {
			t.Fatalf("merged acknowledgement %q should still classify as one", merged.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("turn buffer never emitted the merged turn")
	}
}
