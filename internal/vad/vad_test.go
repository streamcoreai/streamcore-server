package vad

import "testing"

// frame returns a 320-sample (20ms @ 16kHz) frame of constant amplitude,
// whose RMS energy equals the amplitude.
func frame(amplitude int16) []int16 {
	s := make([]int16, 320)
	for i := range s {
		s[i] = amplitude
	}
	return s
}

func feed(d *Detector, amplitude int16, frames int) (started, ended bool) {
	for i := 0; i < frames; i++ {
		s, e := d.Process(frame(amplitude))
		started = started || s
		ended = ended || e
	}
	return
}

func TestFixedThresholdDetection(t *testing.T) {
	d := New(1200, 10, 15)
	if started, _ := feed(d, 2000, 10); !started {
		t.Error("expected speech start after 10 loud frames")
	}
	if _, ended := feed(d, 100, 15); !ended {
		t.Error("expected speech end after 15 quiet frames")
	}
}

func TestFixedThresholdIgnoresQuietSpeech(t *testing.T) {
	d := New(1200, 10, 15)
	if started, _ := feed(d, 900, 50); started {
		t.Error("fixed detector must not trigger below threshold")
	}
}

// A quiet caller on a clean line: noise floor ~50, speech RMS ~1000 — below
// the fixed 1200 threshold but above the adapted one.
func TestAdaptiveDetectsQuietSpeechOnCleanLine(t *testing.T) {
	d := NewDefault()
	feed(d, 50, 100) // learn a clean-line noise floor over ~2s
	if thr := d.effectiveThreshold(); thr != adaptiveMinThreshold {
		t.Fatalf("effectiveThreshold = %.0f, want clamp at %.0f", thr, adaptiveMinThreshold)
	}
	if started, _ := feed(d, 1000, 10); !started {
		t.Error("adaptive detector should catch quiet speech on a clean line")
	}
	// Breath/handling noise on the same quiet line must NOT read as speech —
	// this is what makes the floor safe for the 2-frame barge-in detector.
	d2 := NewDefault()
	feed(d2, 50, 100)
	if started, _ := feed(d2, 700, 50); started {
		t.Error("sub-threshold breath noise triggered the adaptive detector")
	}
}

// A noisy line: steady noise RMS ~700 would sit near the old threshold and
// flicker; the adapted threshold rises above it.
func TestAdaptiveRaisesThresholdOnNoisyLine(t *testing.T) {
	d := NewDefault()
	feed(d, 700, 200) // steady road noise, never crosses 1200 base
	thr := d.effectiveThreshold()
	if thr <= 1200 {
		t.Fatalf("effectiveThreshold = %.0f, want above the 1200 base on a noisy line", thr)
	}
	// The same noise must not now read as speech.
	if started, _ := feed(d, 700, 50); started {
		t.Error("steady noise triggered speech after adaptation")
	}
	// Real speech well above the noise still triggers.
	if started, _ := feed(d, 4000, 10); !started {
		t.Error("loud speech failed to trigger on noisy line")
	}
}

// The threshold must never run away past the cap even under absurd noise.
func TestAdaptiveThresholdCapped(t *testing.T) {
	d := NewDefault()
	feed(d, 1100, 500) // continuous near-threshold noise
	maxThr := 1200 * adaptiveMaxFactor
	if thr := d.effectiveThreshold(); thr > maxThr {
		t.Errorf("effectiveThreshold = %.0f, want <= %.0f", thr, maxThr)
	}
}

// Before any noise floor is learned the base threshold applies unchanged.
func TestAdaptiveUsesBaseThresholdInitially(t *testing.T) {
	d := NewDefault()
	if thr := d.effectiveThreshold(); thr != 1200 {
		t.Errorf("initial effectiveThreshold = %.0f, want 1200", thr)
	}
}

// Speech frames must not contaminate the noise floor estimate.
func TestSpeechDoesNotRaiseNoiseFloor(t *testing.T) {
	d := NewDefault()
	feed(d, 100, 100) // clean floor
	floorBefore := d.noiseFloor
	feed(d, 5000, 100) // sustained speech
	if d.noiseFloor != floorBefore {
		t.Errorf("noise floor moved during speech: %.1f → %.1f", floorBefore, d.noiseFloor)
	}
}
