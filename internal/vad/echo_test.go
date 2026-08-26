package vad

import (
	"testing"
	"time"
)

// fakeClock drives the guard's rolling window without sleeping.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestGuard() (*EchoGuard, *fakeClock) {
	c := &fakeClock{t: time.Unix(0, 0)}
	g := NewEchoGuard(DefaultEchoWindow, DefaultEchoGain, DefaultEchoMargin)
	g.now = c.now
	return g, c
}

// send feeds n 20ms outbound frames of the given amplitude, advancing the clock.
func send(g *EchoGuard, c *fakeClock, amplitude int16, frames int) {
	for i := 0; i < frames; i++ {
		g.Observe(frame(amplitude))
		c.advance(20 * time.Millisecond)
	}
}

func TestEchoGuardSilentAgentImposesNoFloor(t *testing.T) {
	g, _ := newTestGuard()
	if f := g.Floor(); f != 0 {
		t.Errorf("Floor with nothing sent = %.0f, want 0", f)
	}
	if thr := g.Threshold(); thr != 0 {
		t.Errorf("Threshold with nothing sent = %.0f, want 0", thr)
	}
}

func TestEchoGuardFloorTracksLoudestRecentFrame(t *testing.T) {
	g, c := newTestGuard()
	send(g, c, 1000, 3)
	send(g, c, 5000, 1)
	send(g, c, 1000, 3)
	// Echo path delay is unknown, so the peak inside the window governs.
	if got, want := g.Floor(), 5000*DefaultEchoGain; got != want {
		t.Errorf("Floor = %.0f, want %.0f", got, want)
	}
}

func TestEchoGuardWindowExpires(t *testing.T) {
	g, c := newTestGuard()
	send(g, c, 5000, 5)
	c.advance(DefaultEchoWindow)
	if f := g.Floor(); f != 0 {
		t.Errorf("Floor a full window after the agent stopped = %.0f, want 0", f)
	}
}

// A nil guard is the WebRTC path: every call must be inert.
func TestNilEchoGuardIsInert(t *testing.T) {
	var g *EchoGuard
	g.Observe(frame(5000))
	g.Reset()
	if g.Floor() != 0 || g.Threshold() != 0 {
		t.Error("nil guard reported a non-zero bound")
	}
}

// The bug from the issue: agent speech returning over a carrier at a fraction
// of its original level trips the 2-frame barge-in VAD.
func TestBargeInIgnoresEchoOfOwnVoice(t *testing.T) {
	d := NewBargeIn()
	g, c := newTestGuard()
	d.SetEchoReference(g)

	feed(d, 60, 100) // learn a clean-line noise floor first

	// Agent talking at 6000 RMS; ~30% of it comes back, well above the 900
	// adaptive floor that would otherwise call it speech.
	for i := 0; i < 50; i++ {
		g.Observe(frame(6000))
		c.advance(20 * time.Millisecond)
		if started, _ := d.Process(frame(1800)); started {
			t.Fatalf("echo at frame %d read as a caller barge-in", i)
		}
	}
}

// Double-talk over the same echo must still get through: the margin is what
// separates the two cases.
func TestBargeInStillFiresOnDoubleTalk(t *testing.T) {
	d := NewBargeIn()
	g, c := newTestGuard()
	d.SetEchoReference(g)

	feed(d, 60, 100)

	var started bool
	for i := 0; i < 10; i++ {
		g.Observe(frame(6000))
		c.advance(20 * time.Millisecond)
		// Caller over the top of the agent: echo (1800) plus their own voice.
		s, _ := d.Process(frame(9000))
		started = started || s
	}
	if !started {
		t.Error("caller talking over the agent failed to trigger barge-in")
	}
}

// While the duck is on, the wire carries ~12dB less, so the bound must fall
// with it. Observing post-duck samples is what makes that automatic.
func TestEchoGuardFloorFollowsDuck(t *testing.T) {
	d := NewBargeIn()
	g, c := newTestGuard()
	d.SetEchoReference(g)
	feed(d, 60, 100)

	full := frame(6000)
	ducked := make([]int16, len(full))
	for i, v := range full {
		ducked[i] = v / 4
	}

	// Ducked audio echoing back at the same ratio must not hold the caller out.
	var started bool
	for i := 0; i < 10; i++ {
		g.Observe(ducked)
		c.advance(20 * time.Millisecond)
		s, _ := d.Process(frame(2500))
		started = started || s
	}
	if !started {
		t.Error("caller inaudible under a ducked echo bound — the duck was not reflected in the floor")
	}
}

// Echo must not be learned as background noise. If it were, the adaptive
// threshold would ratchet up every time the agent spoke and stay there,
// leaving the agent deaf to a quiet caller once the echo bound lifted.
func TestEchoDoesNotPoisonNoiseFloor(t *testing.T) {
	d := NewBargeIn()
	g, c := newTestGuard()
	d.SetEchoReference(g)

	feed(d, 60, 100)
	floorBefore := d.noiseFloor

	for i := 0; i < 100; i++ {
		g.Observe(frame(6000))
		c.advance(20 * time.Millisecond)
		d.Process(frame(1800))
	}
	if d.noiseFloor != floorBefore {
		t.Errorf("noise floor moved during echo: %.1f → %.1f", floorBefore, d.noiseFloor)
	}

	// And once the agent stops, the quiet caller is heard again.
	c.advance(DefaultEchoWindow)
	if started, _ := feed(d, 1000, 5); !started {
		t.Error("quiet caller missed after the agent stopped speaking")
	}
}
