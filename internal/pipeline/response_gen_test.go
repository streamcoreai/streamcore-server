package pipeline

import (
	"context"
	"testing"
)

// newResponseStatePipeline builds the minimal Pipeline needed to exercise the
// response-ownership handshake: no codecs, no providers, just the shared state
// that supersedeResponse / registerResponse / finishResponse mutate.
func newResponseStatePipeline(events *[]string) *Pipeline {
	return &Pipeline{
		outPCMCh: make(chan PCMFrame, outPCMChSize),
		sendEvent: func(msg interface{}) error {
			if st, ok := msg.(stateMsg); ok && events != nil {
				*events = append(*events, st.State)
			}
			return nil
		},
	}
}

// A response that unwinds after being superseded must not clear the cancel
// func its successor registered. This is the regression that made the agent
// talk over itself: the stale cleanup left the live response uncancellable,
// so the turn after it synthesized into outPCMCh alongside it.
func TestFinishResponseDoesNotClobberSuccessor(t *testing.T) {
	p := newResponseStatePipeline(nil)

	genA := p.supersedeResponse()
	_, cancelA := context.WithCancel(context.Background())
	if !p.registerResponse(genA, cancelA) {
		t.Fatal("registerResponse(genA) rejected the first response")
	}

	// New turn arrives while A is still streaming.
	genB := p.supersedeResponse()
	bCancelled := false
	if !p.registerResponse(genB, func() { bCancelled = true }) {
		t.Fatal("registerResponse(genB) rejected the successor")
	}

	// A's LLM stream returns only now, long after B took over.
	p.finishResponse(genA)

	p.cancelResponse()
	if !bCancelled {
		t.Fatal("B was left without a cancel func — a superseded response cleared it, " +
			"so the next turn would synthesize on top of B")
	}
}

// A superseded response must not clear `speaking` either: it gates barge-in
// detection in runInbound, so clearing it while the successor talks makes the
// agent deaf to interruption for the whole turn.
func TestFinishResponseDoesNotClearSuccessorSpeaking(t *testing.T) {
	p := newResponseStatePipeline(nil)

	genA := p.supersedeResponse()
	genB := p.supersedeResponse()
	p.registerResponse(genB, func() {})

	// B's first TTS frame reached the wire.
	p.speaking.Store(true)

	p.finishResponse(genA)

	if !p.speaking.Load() {
		t.Fatal("speaking cleared by a superseded response — barge-in is now dead for B's turn")
	}
}

// The superseded response must also stay silent on the data channel: emitting
// "listening" while its successor speaks desynchronizes the client UI.
func TestFinishResponseSuppressesStaleListeningState(t *testing.T) {
	var events []string
	p := newResponseStatePipeline(&events)

	genA := p.supersedeResponse()
	p.supersedeResponse()

	p.finishResponse(genA)
	if len(events) != 0 {
		t.Fatalf("superseded response emitted state events: %v", events)
	}

	// The current response still reports completion normally.
	genC := p.supersedeResponse()
	p.registerResponse(genC, func() {})
	p.finishResponse(genC)
	if len(events) != 1 || events[0] != "listening" {
		t.Fatalf("current response should emit exactly one listening event, got %v", events)
	}
}

// supersedeResponse must discard audio the retired response already queued.
// Cancelling only stops NEW frames; the ones already in outPCMCh keep playing
// while the next response stacks up behind them.
func TestSupersedeResponseDropsQueuedAudio(t *testing.T) {
	p := newResponseStatePipeline(nil)
	p.speaking.Store(true)

	for i := 0; i < 25; i++ {
		p.outPCMCh <- PCMFrame{Samples: make([]int16, 320)}
	}

	p.supersedeResponse()

	if n := len(p.outPCMCh); n != 0 {
		t.Fatalf("outPCMCh still holds %d stale frames — the old answer plays into the new turn", n)
	}
	if p.speaking.Load() {
		t.Fatal("speaking still set after the queued audio was discarded")
	}
}

// A response goroutine scheduled after its turn was already superseded must
// abandon itself rather than start synthesizing next to the live response.
func TestRegisterResponseRejectsSupersededGeneration(t *testing.T) {
	p := newResponseStatePipeline(nil)

	stale := p.supersedeResponse()
	p.supersedeResponse()

	if p.registerResponse(stale, func() {}) {
		t.Fatal("a superseded generation was allowed to claim the audio path")
	}
}
