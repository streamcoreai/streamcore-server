package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/stt"
)

// feedSpan records seconds of audio at one level, as the inbound loop does.
func feedSpan(g *speakerGate, seconds, rms float64) {
	for i := 0; i < int(seconds/speakerFrame.Seconds()); i++ {
		g.frame(rms)
	}
}

// The caller's turns teach the gate their level; a final from audio 10 dB
// below it is the room and is not answered.
func TestSpeakerGateIgnoresTheRoom(t *testing.T) {
	g := newSpeakerGate(10)
	now := time.Now()

	feedSpan(g, 2, 5000) // the caller: 0-2s
	if _, _, bg := g.background(stt.TranscriptResult{IsFinal: true, AudioStart: 0, AudioEnd: 2}, now); bg {
		t.Fatal("the first final, the caller's, was called background")
	}
	feedSpan(g, 2, 800) // a TV: 2-4s
	if _, _, bg := g.background(stt.TranscriptResult{IsFinal: true, AudioStart: 2, AudioEnd: 4}, now); !bg {
		t.Error("speech 16 dB below the caller was answered")
	}
	feedSpan(g, 2, 2500) // the caller, 6 dB softer: 4-6s
	if _, _, bg := g.background(stt.TranscriptResult{IsFinal: true, AudioStart: 4, AudioEnd: 6}, now); bg {
		t.Error("the caller speaking 6 dB softer was ignored")
	}

	if g.near(800, now) {
		t.Error("a room-level frame counted as the caller talking")
	}
	if !g.near(4000, now) {
		t.Error("a caller-level frame did not count")
	}

	// With no committed turns the level decays, so a caller who really is
	// quieter is heard again within a minute, once the line is quiet.
	feedSpan(g, 10, 30)
	if !g.near(800, now.Add(time.Minute)) {
		t.Error("the learned level never relaxed")
	}
}

// A minute of chatter with no caller turn must not lower the bar until the
// chatter itself counts as the caller.
func TestSpeakerGateDoesNotRelaxIntoTheRoom(t *testing.T) {
	g := newSpeakerGate(10)
	now := time.Now()
	feedSpan(g, 2, 10000)
	g.background(stt.TranscriptResult{IsFinal: true, AudioStart: 0, AudioEnd: 2}, now)

	feedSpan(g, 20, 1300) // the room, 18dB down, with peaks 6dB over its floor
	feedSpan(g, 2, 2600)
	later := now.Add(time.Minute)
	if _, _, bg := g.background(stt.TranscriptResult{IsFinal: true, AudioStart: 22, AudioEnd: 24}, later); !bg {
		t.Error("after a minute of chatter its loudest moment was taken for the caller")
	}
	if g.near(2600, later) {
		t.Error("after a minute of chatter a room-level frame counted as the caller")
	}
	if !g.near(6000, later) {
		t.Error("the caller stopped counting")
	}
}

// Off, unlearned, nil, or without an audio span, the gate lets everything
// through: behaviour is what it was before the gate existed.
func TestSpeakerGatePassThrough(t *testing.T) {
	now := time.Now()
	var nilGate *speakerGate
	if _, _, bg := nilGate.background(stt.TranscriptResult{IsFinal: true, AudioStart: 0, AudioEnd: 1}, now); bg || !nilGate.near(1, now) {
		t.Error("nil gate filtered")
	}
	off := newSpeakerGate(0)
	feedSpan(off, 1, 100)
	if _, _, bg := off.background(stt.TranscriptResult{IsFinal: true, AudioStart: 0, AudioEnd: 1}, now); bg || !off.near(1, now) {
		t.Error("disabled gate filtered")
	}
	g := newSpeakerGate(10)
	if !g.near(1, now) {
		t.Error("an unlearned gate filtered a frame")
	}
	feedSpan(g, 1, 5000)
	g.background(stt.TranscriptResult{IsFinal: true, AudioStart: 0, AudioEnd: 1}, now)
	if _, _, bg := g.background(stt.TranscriptResult{IsFinal: true}, now); bg {
		t.Error("a final with no audio span was called background")
	}
}

// After an STT reconnect the provider's clock restarts at zero.
func TestSpeakerGateFollowsReconnect(t *testing.T) {
	g := newSpeakerGate(10)
	now := time.Now()
	feedSpan(g, 3, 5000)
	g.background(stt.TranscriptResult{IsFinal: true, AudioStart: 0, AudioEnd: 3}, now)
	g.newConnection()
	feedSpan(g, 1, 700)
	if _, _, bg := g.background(stt.TranscriptResult{IsFinal: true, AudioStart: 0, AudioEnd: 1}, now); !bg {
		t.Error("after a reconnect the span was read from the old connection's audio")
	}
}

func TestHoldFrom(t *testing.T) {
	const base = 350 * time.Millisecond
	now := time.Now()
	p := &Pipeline{}

	if got := p.holdFrom(base, base, now); got != base {
		t.Errorf("no caller speech seen: hold %v, want the full %v from the final", got, base)
	}

	p.callerVoiceAt.Store(now.Add(-800 * time.Millisecond).UnixNano())
	if got := p.holdFrom(base, base, now); got != 0 {
		t.Errorf("caller quiet 800ms already: hold %v, want 0", got)
	}

	p.callerVoiceAt.Store(now.Add(-100 * time.Millisecond).UnixNano())
	if got := p.holdFrom(base, base, now); got != 250*time.Millisecond {
		t.Errorf("caller quiet 100ms: hold %v, want 250ms", got)
	}

	// The VAD missed this utterance: its last stamp is from long before.
	p.callerVoiceAt.Store(now.Add(-5 * time.Second).UnixNano())
	if got := p.holdFrom(base, base, now); got != base {
		t.Errorf("stale VAD stamp: hold %v, want the full %v", got, base)
	}

	// An incomplete-looking turn keeps its long wait regardless.
	p.callerVoiceAt.Store(now.Add(-800 * time.Millisecond).UnixNano())
	if got := p.holdFrom(turnMergeIncomplete, base, now); got != turnMergeIncomplete {
		t.Errorf("incomplete turn: hold %v, want %v", got, turnMergeIncomplete)
	}
}

func newTurnBufferPipeline(t *testing.T) *Pipeline {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := &config.Config{}
	cfg.Pipeline.TurnMergeMs = 350
	p := &Pipeline{
		ctx:          ctx,
		cancel:       cancel,
		cfg:          cfg,
		finalCh:      make(chan TranscriptEvent, 10),
		transcriptCh: make(chan TranscriptEvent, 10),
	}
	go p.runTurnBuffer()
	return p
}

// A caller who has already been quiet longer than the hold by the time the
// final lands gets an answer straight away.
func TestTurnBufferSkipsHoldAlreadySpent(t *testing.T) {
	p := newTurnBufferPipeline(t)
	p.callerVoiceAt.Store(time.Now().Add(-800 * time.Millisecond).UnixNano())

	start := time.Now()
	p.finalCh <- TranscriptEvent{Text: "What's the capital of France?", Final: true, TurnStart: start}
	select {
	case ev := <-p.transcriptCh:
		if took := time.Since(start); took > 150*time.Millisecond {
			t.Errorf("turn held %v after the caller had been quiet 800ms", took)
		}
		if ev.Text != "What's the capital of France?" {
			t.Errorf("turn text %q", ev.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("turn never fired")
	}
}

// A final followed by more speech is a hesitation: the turn waits for the
// caller and takes in what they say next.
func TestTurnBufferHoldsWhileCallerTalks(t *testing.T) {
	p := newTurnBufferPipeline(t)
	talking := make(chan struct{})
	go func() {
		for {
			select {
			case <-talking:
				return
			default:
				p.callerVoiceAt.Store(time.Now().UnixNano())
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()

	p.finalCh <- TranscriptEvent{Text: "I'd like to book a table for two people.", Final: true, TurnStart: time.Now()}
	select {
	case ev := <-p.transcriptCh:
		close(talking)
		t.Fatalf("turn %q fired while the caller was still talking", ev.Text)
	case <-time.After(700 * time.Millisecond):
	}

	close(talking)
	p.callerVoiceAt.Store(time.Now().UnixNano())
	p.finalCh <- TranscriptEvent{Text: "At seven o'clock tonight.", Final: true, TurnStart: time.Now()}
	select {
	case ev := <-p.transcriptCh:
		if ev.Text != "I'd like to book a table for two people. At seven o'clock tonight." {
			t.Errorf("merged turn %q", ev.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("merged turn never fired")
	}
	select {
	case ev := <-p.transcriptCh:
		t.Errorf("a second turn fired: %q", ev.Text)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestMergeWait(t *testing.T) {
	const base = 350 * time.Millisecond

	cases := []struct {
		name string
		text string
		want time.Duration
	}{
		// Terminal punctuation beats a pronoun tail: the provider already judged
		// the thought complete, so the tail word is not evidence of a pause.
		{"punctuated sentence ending on a pronoun", "I already fixed it.", base},
		{"punctuated question", "can you check it?", base},
		{"punctuated exclamation", "that worked!", base},

		// The case the tail list exists for — same words, no closer.
		{"unpunctuated pronoun tail", "I already fixed it", turnMergeIncomplete},
		{"dangling conjunction", "I tried that and", turnMergeIncomplete},
		{"filler tail", "so it was, um", turnMergeIncomplete},

		// Dictation still extends: callers pause much longer between spelled
		// segments than between words.
		{"mid-dictation separator", "my email is gary dot", turnMergeIncomplete},
		{"spelled letters", "it's g a r", turnMergeIncomplete},
		{"digit run", "the number is 0 4 1 2", turnMergeIncomplete},
		{"completed address is not mid-dictation", "gary@example.com.", base},

		{"ordinary finished clause", "yes that's right", base},
		{"empty", "", base},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mergeWait(c.text, base); got != c.want {
				t.Errorf("mergeWait(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

// A caller the VAD cannot hear keeps the silence the old 300ms endpointing
// plus hold demanded, whatever endpointing is now set to.
func TestVADMissHold(t *testing.T) {
	cfg := &config.Config{}
	cfg.STT.Provider = "deepgram"
	for ep, want := range map[string]time.Duration{"25": 275 * time.Millisecond, "300": 0, "500": 0, "bad": 0} {
		cfg.Deepgram.Endpointing = ep
		if got := vadMissHoldFor(cfg); got != want {
			t.Errorf("endpointing %s: extra hold %v, want %v", ep, got, want)
		}
	}
	cfg.STT.Provider = "openai"
	cfg.Deepgram.Endpointing = "25"
	if got := vadMissHoldFor(cfg); got != 0 {
		t.Errorf("non-Deepgram STT got extra hold %v", got)
	}

	const base = 350 * time.Millisecond
	p := &Pipeline{vadMissHold: 275 * time.Millisecond}
	if got := p.holdFrom(base, base, time.Now()); got != base+275*time.Millisecond {
		t.Errorf("VAD never heard the caller: hold %v, want %v", got, base+275*time.Millisecond)
	}
	p.callerVoiceAt.Store(time.Now().Add(-800 * time.Millisecond).UnixNano())
	if got := p.holdFrom(base, base, time.Now()); got != 0 {
		t.Errorf("VAD heard the caller: hold %v, want 0", got)
	}
}

// A tool asked for while the caller is still finishing their sentence waits,
// and never runs if the caller's next turn supersedes it.
func TestToolWaitsForContinuation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipeline.UserSpeechQuietMs = 600
	p := &Pipeline{cfg: cfg}
	p.turnCommittedAt.Store(time.Now().Add(-time.Second).UnixNano())
	p.callerVoiceAt.Store(time.Now().UnixNano())

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if _, err := p.runTool(ctx, nil, nil); err == nil {
		t.Fatal("tool ran for a turn the caller was still finishing")
	}
}
