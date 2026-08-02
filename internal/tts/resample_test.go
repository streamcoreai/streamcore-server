package tts

import (
	"math"
	"testing"
)

// tone24k renders a pure sine at freq Hz as s16le mono PCM at 24 kHz.
func tone24k(freq float64, samples int, amp float64) []byte {
	out := make([]byte, 0, samples*2)
	for i := 0; i < samples; i++ {
		v := int16(amp * math.Sin(2*math.Pi*freq*float64(i)/resampleSourceRate))
		u := uint16(v)
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func toFloat(pcm []byte) []float64 {
	out := make([]float64, len(pcm)/2)
	for i := range out {
		out[i] = float64(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8))
	}
	return out
}

func rms(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	var sum float64
	for _, v := range x {
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(x)))
}

// magAt measures the amplitude at freq using a Hann-windowed single-bin DFT.
// The window keeps leakage from a strong tone elsewhere in the spectrum well
// below the levels these tests assert on.
func magAt(x []float64, freq, sampleRate float64) float64 {
	n := len(x)
	if n == 0 {
		return 0
	}
	var re, im, norm float64
	for i, v := range x {
		w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
		ph := 2 * math.Pi * freq * float64(i) / sampleRate
		re += w * v * math.Cos(ph)
		im -= w * v * math.Sin(ph)
		norm += w
	}
	return 2 * math.Hypot(re, im) / norm
}

const outRate = 16000

// A 1 kHz tone must pass through essentially untouched — this pins down that
// the filter is not attenuating the band we care about.
func TestResamplePassesInBandTone(t *testing.T) {
	in := tone24k(1000, 24000, 8000)
	out := toFloat(resample24kTo16k(in))

	if got, want := len(out), 16000; math.Abs(float64(got-want)) > 4 {
		t.Errorf("output length = %d, want ~%d", got, want)
	}
	amp := magAt(out, 1000, outRate)
	if amp < 7600 || amp > 8400 {
		t.Errorf("1 kHz amplitude = %.0f, want ~8000 (unity gain)", amp)
	}
}

// steady returns x with the filter's start/end transient trimmed off. A test
// tone begins abruptly at sample zero, and the FIR rings on that step; that
// ringing is not aliasing, so it is excluded when measuring stopband
// rejection. 256 samples is comfortably longer than the kernel's reach.
func steady(x []float64) []float64 {
	const trim = 256
	if len(x) <= 2*trim {
		return x
	}
	return x[trim : len(x)-trim]
}

// The point of the filter: content above the 8 kHz output Nyquist must be
// rejected rather than folded back into the audible band. The previous
// decimate-by-pattern implementation passed these tones at about −3 dB.
func TestResampleRejectsAboveNyquist(t *testing.T) {
	ref := rms(steady(toFloat(resample24kTo16k(tone24k(1000, 24000, 8000)))))
	if ref <= 0 {
		t.Fatal("reference tone produced silence")
	}

	for _, freq := range []float64{8200, 9000, 10000, 11000, 11500} {
		got := rms(steady(toFloat(resample24kTo16k(tone24k(freq, 24000, 8000)))))
		db := 20 * math.Log10(math.Max(got, 1e-12)/ref)
		if db > -50 {
			t.Errorf("%.0f Hz leaked at %.1f dB, want <= -50 dB", freq, db)
		}
	}
}

// An in-band tone must not generate an image at |f - 8 kHz|. The previous
// implementation filtered its two output phases differently, so a 7 kHz tone
// produced a 1 kHz spur only 12 dB down.
func TestResampleNoPhaseImage(t *testing.T) {
	out := toFloat(resample24kTo16k(tone24k(7000, 24000, 8000)))

	main := magAt(out, 7000, outRate)
	image := magAt(out, 1000, outRate)
	db := 20 * math.Log10(math.Max(image, 1e-12)/math.Max(main, 1e-12))
	if db > -40 {
		t.Errorf("image at 1 kHz is %.1f dB below the 7 kHz tone, want <= -40 dB", db)
	}
}

// Silence in, silence out — and no panic on short or empty buffers.
func TestResampleEdgeCases(t *testing.T) {
	if got := resample24kTo16k(nil); got != nil {
		t.Errorf("nil input returned %d bytes, want nil", len(got))
	}
	if got := resample24kTo16k([]byte{0x01}); got != nil {
		t.Errorf("sub-sample input returned %d bytes, want nil", len(got))
	}
	for _, n := range []int{1, 2, 3, 4, 5, 17} {
		in := make([]byte, n*2)
		out := resample24kTo16k(in)
		if len(out)%2 != 0 {
			t.Errorf("%d samples in produced an odd byte count %d", n, len(out))
		}
		for _, v := range toFloat(out) {
			if v != 0 {
				t.Errorf("%d silent samples in produced non-zero output", n)
				break
			}
		}
	}
}
