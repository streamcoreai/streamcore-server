package pipeline

import (
	"math"
	"time"

	"github.com/streamcoreai/server/internal/audio"
)

// Thinking-sound parameters.
const (
	thinkFreq       = 330.0 // Hz (E4 — gentle tone)
	thinkAmplitude  = 3000  // moderate volume (max ~32767)
	thinkPulseMs    = 150   // beep duration
	thinkSilenceMs  = 1850  // gap between beeps (2s cycle)
	thinkSampleRate = 16000
	thinkDelay      = 500 * time.Millisecond // wait before playing (fast tools stay silent)
)

// Sent-sound parameters — short rising two-tone chime.
const (
	sentFreq1     = 523.0 // Hz (C5)
	sentFreq2     = 659.0 // Hz (E5)
	sentAmplitude = 3000
	sentToneMs    = 100 // each tone duration
	sentGapMs     = 30  // silence between tones
)

// generateThinkingPulse builds one cycle (beep + silence) as 20ms PCM frames.
func generateThinkingPulse() [][]int16 {
	pulseSamples := thinkSampleRate * thinkPulseMs / 1000
	silenceSamples := thinkSampleRate * thinkSilenceMs / 1000
	totalSamples := pulseSamples + silenceSamples

	pcm := make([]int16, totalSamples)
	fadeLen := pulseSamples / 4
	for i := 0; i < pulseSamples; i++ {
		t := float64(i) / float64(thinkSampleRate)
		// Smooth fade-in / fade-out envelope
		env := 1.0
		if i < fadeLen {
			env = float64(i) / float64(fadeLen)
		} else if i > pulseSamples-fadeLen {
			env = float64(pulseSamples-i) / float64(fadeLen)
		}
		pcm[i] = int16(float64(thinkAmplitude) * env * math.Sin(2*math.Pi*thinkFreq*t))
	}
	// Silence portion is already zeroed.

	// Slice into 20ms frames.
	return sliceToFrames(pcm)
}

// generateSentSound builds a short rising two-tone chime as 20ms PCM frames.
func generateSentSound() [][]int16 {
	tone1Samples := thinkSampleRate * sentToneMs / 1000
	gapSamples := thinkSampleRate * sentGapMs / 1000
	tone2Samples := thinkSampleRate * sentToneMs / 1000
	totalSamples := tone1Samples + gapSamples + tone2Samples

	pcm := make([]int16, totalSamples)

	// First tone (C5)
	writeTone(pcm[:tone1Samples], sentFreq1, sentAmplitude, tone1Samples)
	// Gap is already zeroed
	// Second tone (E5)
	writeTone(pcm[tone1Samples+gapSamples:], sentFreq2, sentAmplitude, tone2Samples)

	return sliceToFrames(pcm)
}

// writeTone fills a PCM buffer with a sine wave with fade-in/out envelope.
func writeTone(buf []int16, freq float64, amplitude int, samples int) {
	fadeLen := samples / 4
	for i := 0; i < samples; i++ {
		t := float64(i) / float64(thinkSampleRate)
		env := 1.0
		if i < fadeLen {
			env = float64(i) / float64(fadeLen)
		} else if i > samples-fadeLen {
			env = float64(samples-i) / float64(fadeLen)
		}
		buf[i] = int16(float64(amplitude) * env * math.Sin(2*math.Pi*freq*t))
	}
}

// sliceToFrames divides a PCM buffer into 20ms frames.
func sliceToFrames(pcm []int16) [][]int16 {
	var frames [][]int16
	for i := 0; i < len(pcm); i += audio.FrameSize {
		frame := make([]int16, audio.FrameSize)
		end := i + audio.FrameSize
		if end <= len(pcm) {
			copy(frame, pcm[i:end])
		} else {
			copy(frame, pcm[i:])
		}
		frames = append(frames, frame)
	}
	return frames
}

// playThinkingSound pushes a looping soft tone into outPCMCh until done is
// closed. A short delay prevents audible glitches on fast-returning tools.
func (p *Pipeline) playThinkingSound(done <-chan struct{}) {
	// Give fast tools a grace period — no sound if they finish quickly.
	select {
	case <-done:
		return
	case <-p.ctx.Done():
		return
	case <-time.After(thinkDelay):
	}

	pulse := generateThinkingPulse()
	first := true

	for {
		for _, frame := range pulse {
			select {
			case <-done:
				return
			case <-p.ctx.Done():
				return
			case p.outPCMCh <- PCMFrame{
				Samples:      frame,
				NewTalkspurt: first,
			}:
				first = false
			}
		}
	}
}

// playSentSound plays a short rising two-tone chime to confirm an action completed.
func (p *Pipeline) playSentSound() {
	frames := generateSentSound()
	for i, frame := range frames {
		select {
		case <-p.ctx.Done():
			return
		case p.outPCMCh <- PCMFrame{
			Samples:      frame,
			NewTalkspurt: i == 0,
		}:
		}
	}
}
