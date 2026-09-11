package vad

import (
	"sync"
	"time"
)

// Defaults for the echo reference, tuned on an 8kHz µ-law carrier path.
const (
	DefaultEchoWindow = 400 * time.Millisecond
	DefaultEchoGain   = 0.6
	DefaultEchoMargin = 1.8
)

// EchoGuard bounds barge-in detection by what the agent itself just sent.
//
// A browser runs AEC before audio ever reaches the server, so on the WebRTC
// path the detector never sees the agent's own output coming back. Over a
// carrier there is no AEC anywhere in the path. The returning audio is
// attenuated but structurally identical to speech, so an RMS-vs-noise-floor
// test cannot separate the two: adaptiveMinThreshold and noiseFloorMultiplier
// are tuned against background noise, and echo is a copy of speech.
//
// Raising the fixed threshold instead only trades self-barging for being deaf
// to quiet callers. The reference that does separate the two cases is the
// outbound signal. Echo cannot be louder than the audio that produced it, so
// recent outbound RMS scaled by gain bounds it from above, and inbound counts
// as an interruption only once it clears that bound by margin. The margin is
// what tells double-talk from echo. While the agent is silent the bound is
// zero and the ordinary adaptive threshold governs, so sensitivity to a quiet
// caller on a clean line is untouched.
type EchoGuard struct {
	mu     sync.Mutex
	window time.Duration
	gain   float64
	margin float64

	// Ring of recent outbound frames, oldest at start, count entries live.
	buf   []echoSample
	start int
	count int

	now func() time.Time
}

type echoSample struct {
	at  time.Time
	rms float64
}

// NewEchoGuard builds a guard over the given reference window. Zero or
// negative parameters fall back to the defaults.
func NewEchoGuard(window time.Duration, gain, margin float64) *EchoGuard {
	if window <= 0 {
		window = DefaultEchoWindow
	}
	if gain <= 0 {
		gain = DefaultEchoGain
	}
	if margin <= 0 {
		margin = DefaultEchoMargin
	}
	// Sized for frames as short as 5ms so a full window always fits. Nothing
	// on the sender path pushes faster than realtime; if something did, the
	// oldest entry drops and the window shortens rather than the ring growing.
	return &EchoGuard{
		window: window,
		gain:   gain,
		margin: margin,
		buf:    make([]echoSample, int(window/(5*time.Millisecond))+8),
		now:    time.Now,
	}
}

// Observe records one frame of outbound audio.
//
// Pass the samples that actually go on the wire, after any duck attenuation.
// Outbound RMS is naturally available at enqueue, but a ducked talkspurt is
// ~12dB quieter by the time it is sent, and a bound built from the un-ducked
// signal would hold the barge-in threshold high through the whole duck, which
// is when the caller most needs to be heard.
func (g *EchoGuard) Observe(samples []int16) {
	if g == nil {
		return
	}
	rms := RMSEnergy(samples)
	at := g.now()

	g.mu.Lock()
	defer g.mu.Unlock()
	g.prune(at)
	if g.count == len(g.buf) {
		g.start = (g.start + 1) % len(g.buf)
		g.count--
	}
	g.buf[(g.start+g.count)%len(g.buf)] = echoSample{at: at, rms: rms}
	g.count++
}

// Floor is an upper bound on how loud the agent's echo can be right now: the
// loudest frame sent inside the window, scaled by the gain. It returns to
// zero once the agent has been quiet for a full window.
//
// Loudest rather than mean, because the echo path delay is unknown — anything
// sent inside the window could be what is arriving now.
func (g *EchoGuard) Floor() float64 {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.prune(g.now())

	var peak float64
	for i := 0; i < g.count; i++ {
		if v := g.buf[(g.start+i)%len(g.buf)].rms; v > peak {
			peak = v
		}
	}
	return peak * g.gain
}

// Threshold is the inbound RMS a frame must exceed to read as the caller
// talking rather than the agent's own voice returning. Zero while the agent
// is silent, which hands the decision back to the adaptive threshold.
func (g *EchoGuard) Threshold() float64 {
	if g == nil {
		return 0
	}
	return g.Floor() * g.margin
}

// Reset drops the reference window, for when the audio path changes under the
// detector — a resumed session lands on new tracks and the old outbound RMS
// describes a link that no longer exists.
func (g *EchoGuard) Reset() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.start, g.count = 0, 0
}

// prune drops entries that have aged out. Caller holds the lock.
func (g *EchoGuard) prune(now time.Time) {
	cutoff := now.Add(-g.window)
	for g.count > 0 && g.buf[g.start].at.Before(cutoff) {
		g.start = (g.start + 1) % len(g.buf)
		g.count--
	}
}
