package vad

import "math"

// Detector implements a simple energy-based Voice Activity Detector.
// It requires consecutive frames above/below threshold to trigger state changes,
// preventing spurious transitions from brief noise or pauses.
//
// When adaptive mode is on, the detector additionally tracks an exponential
// moving average of non-speech energy (the line's noise floor) and derives
// the effective threshold from it. One fixed global threshold can't serve
// both a quiet caller on a clean line (speech RMS below 1200 → never
// detected) and a caller next to a road (noise RMS near 1200 → constant
// false triggers); the adaptive floor tracks each call's actual conditions.
type Detector struct {
	threshold    float64
	speechFrames int // consecutive speech frames to trigger start
	silentFrames int // consecutive silent frames to trigger end
	isSpeaking   bool
	speechCount  int
	silentCount  int

	adaptive   bool
	noiseFloor float64 // EMA of non-speech frame energy; negative = unset
}

// Adaptive-threshold tuning. The floor adapts over ~1s of silence frames
// (alpha 0.05 at 50 frames/sec). The effective threshold sits at 3× the
// noise floor — comfortably above breathing/line hiss while well below
// speech, which carries 10-30dB over the floor — clamped so it can never
// drop below adaptiveMinThreshold nor run past adaptiveMaxFactor× the base
// threshold (continuous loud noise must not push the threshold above
// actual speech energy).
//
// adaptiveMinThreshold is deliberately only 25% below the 1200 base: the
// barge-in detector trips on just 2 frames (40ms), and a 600 floor proved
// hair-trigger in production — breath/handling noise on a quiet line
// engaged the TTS duck during agent speech and could escalate to a
// response-cancelling mute. 900 still catches quiet callers the fixed
// threshold missed without arming the duck on every exhale.
const (
	noiseFloorAlpha      = 0.05
	noiseFloorMultiplier = 3.0
	adaptiveMinThreshold = 900.0
	adaptiveMaxFactor    = 2.5
)

// New creates a VAD with custom parameters and a fixed threshold.
func New(threshold float64, speechFrames, silentFrames int) *Detector {
	return &Detector{
		threshold:    threshold,
		speechFrames: speechFrames,
		silentFrames: silentFrames,
		noiseFloor:   -1,
	}
}

// NewDefault creates a VAD tuned for voice agents:
// adaptive threshold (base 1200 RMS until the noise floor is learned),
// 200ms onset (10 frames), 300ms offset (15 frames) at 20ms/frame.
func NewDefault() *Detector {
	d := New(1200.0, 10, 15)
	d.adaptive = true
	return d
}

// NewBargeIn creates a VAD optimized for barge-in interrupt detection:
// same adaptive threshold, but only 40ms onset (2 frames) for faster
// response. The silent frames count stays at 15 to avoid premature
// end-of-speech.
func NewBargeIn() *Detector {
	d := New(1200.0, 2, 15)
	d.adaptive = true
	return d
}

// effectiveThreshold returns the current decision threshold: the fixed base
// until a noise floor has been learned, then the clamped multiple of the
// floor.
func (d *Detector) effectiveThreshold() float64 {
	if !d.adaptive || d.noiseFloor < 0 {
		return d.threshold
	}
	thr := d.noiseFloor * noiseFloorMultiplier
	if thr < adaptiveMinThreshold {
		thr = adaptiveMinThreshold
	}
	if maxThr := d.threshold * adaptiveMaxFactor; thr > maxThr {
		thr = maxThr
	}
	return thr
}

// updateNoiseFloor folds a non-speech frame's energy into the EMA.
func (d *Detector) updateNoiseFloor(energy float64) {
	if !d.adaptive {
		return
	}
	if d.noiseFloor < 0 {
		d.noiseFloor = energy
		return
	}
	d.noiseFloor = (1-noiseFloorAlpha)*d.noiseFloor + noiseFloorAlpha*energy
}

// Process evaluates a PCM frame and returns whether speech just started or ended.
func (d *Detector) Process(samples []int16) (started, ended bool) {
	energy := RMSEnergy(samples)
	if energy > d.effectiveThreshold() {
		d.speechCount++
		d.silentCount = 0
		if !d.isSpeaking && d.speechCount >= d.speechFrames {
			d.isSpeaking = true
			started = true
		}
	} else {
		// Below-threshold frames are what the noise floor is made of —
		// learning only here keeps speech energy out of the floor estimate.
		d.updateNoiseFloor(energy)
		d.silentCount++
		d.speechCount = 0
		if d.isSpeaking && d.silentCount >= d.silentFrames {
			d.isSpeaking = false
			ended = true
		}
	}
	return
}

// IsSpeaking returns current speech state.
func (d *Detector) IsSpeaking() bool {
	return d.isSpeaking
}

// Reset clears all state.
func (d *Detector) Reset() {
	d.isSpeaking = false
	d.speechCount = 0
	d.silentCount = 0
}

// RMSEnergy calculates root-mean-square energy of int16 PCM samples.
func RMSEnergy(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(samples)))
}
