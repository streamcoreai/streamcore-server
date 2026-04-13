package vad

import "math"

// Detector implements a simple energy-based Voice Activity Detector.
// It requires consecutive frames above/below threshold to trigger state changes,
// preventing spurious transitions from brief noise or pauses.
type Detector struct {
	threshold    float64
	speechFrames int // consecutive speech frames to trigger start
	silentFrames int // consecutive silent frames to trigger end
	isSpeaking   bool
	speechCount  int
	silentCount  int
}

// New creates a VAD with custom parameters.
func New(threshold float64, speechFrames, silentFrames int) *Detector {
	return &Detector{
		threshold:    threshold,
		speechFrames: speechFrames,
		silentFrames: silentFrames,
	}
}

// NewDefault creates a VAD tuned for voice agents:
// ~1200 RMS threshold, 200ms onset (10 frames), 300ms offset (15 frames) at 20ms/frame.
// The higher threshold and longer onset reduce false triggers from background noise.
func NewDefault() *Detector {
	return New(1200.0, 10, 15)
}

// NewBargeIn creates a VAD optimized for barge-in interrupt detection:
// same RMS threshold, but only 60ms onset (3 frames) for faster response.
// The silent frames count stays at 15 to avoid premature end-of-speech.
func NewBargeIn() *Detector {
	return New(1200.0, 3, 15)
}

// Process evaluates a PCM frame and returns whether speech just started or ended.
func (d *Detector) Process(samples []int16) (started, ended bool) {
	energy := RMSEnergy(samples)
	if energy > d.threshold {
		d.speechCount++
		d.silentCount = 0
		if !d.isSpeaking && d.speechCount >= d.speechFrames {
			d.isSpeaking = true
			started = true
		}
	} else {
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
