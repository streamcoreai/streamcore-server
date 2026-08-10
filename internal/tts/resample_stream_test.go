package tts

import (
	"math"
	"testing"
)

// pcm24k renders n samples of a sine at freq Hz as little-endian linear16.
func pcm24k(n int, freq float64) []byte {
	out := make([]byte, 0, n*2)
	for i := 0; i < n; i++ {
		v := int16(12000 * math.Sin(2*math.Pi*freq*float64(i)/float64(resampleSourceRate)))
		out = append(out, byte(uint16(v)), byte(uint16(v)>>8))
	}
	return out
}

func samples(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(uint16(b[2*i]) | uint16(b[2*i+1])<<8)
	}
	return out
}

// The streaming resampler exists so a chunked utterance sounds like the same
// utterance resampled whole. If the two diverge, the chunk boundaries are
// audible — which is the entire failure this type is here to prevent.
func TestStreamResamplerMatchesOneShot(t *testing.T) {
	in := pcm24k(4800, 440) // 200 ms

	for _, chunk := range []int{2, 64, 321, 1024, 9600} {
		var rs streamResampler24to16
		var got []byte
		for off := 0; off < len(in); off += chunk {
			end := off + chunk
			if end > len(in) {
				end = len(in)
			}
			got = append(got, rs.Write(in[off:end])...)
		}
		got = append(got, rs.Flush()...)

		want := resample24kTo16k(in)
		if len(got) != len(want) {
			t.Errorf("chunk=%d: got %d bytes, want %d", chunk, len(got), len(want))
			continue
		}

		g, w := samples(got), samples(want)
		for i := range w {
			// Both paths run the same kernel over the same samples, so the
			// only expected difference is float rounding at the boundary.
			if d := int(g[i]) - int(w[i]); d > 1 || d < -1 {
				t.Errorf("chunk=%d: sample %d = %d, want %d", chunk, i, g[i], w[i])
				break
			}
		}
	}
}

// A chunk shorter than one output period must not emit a partial or misaligned
// sample — it should hold the input until enough has arrived.
func TestStreamResamplerHoldsShortChunks(t *testing.T) {
	var rs streamResampler24to16
	if out := rs.Write(pcm24k(1, 440)); len(out) != 0 {
		t.Fatalf("one input sample produced %d bytes, want 0", len(out))
	}
}

// Every emitted chunk must be int16-aligned; an odd length would desynchronise
// the sample stream for everything downstream.
func TestStreamResamplerEmitsAlignedChunks(t *testing.T) {
	in := pcm24k(2400, 440)
	var rs streamResampler24to16
	for off := 0; off < len(in); off += 100 {
		end := off + 100
		if end > len(in) {
			end = len(in)
		}
		if out := rs.Write(in[off:end]); len(out)%2 != 0 {
			t.Fatalf("emitted %d bytes, want an even count", len(out))
		}
	}
	if out := rs.Flush(); len(out)%2 != 0 {
		t.Fatalf("flush emitted %d bytes, want an even count", len(out))
	}
}

// Flush resets the resampler so one client can synthesize many utterances
// without the previous one's tail leaking into the next.
func TestStreamResamplerResetsAfterFlush(t *testing.T) {
	in := pcm24k(2400, 440)
	var rs streamResampler24to16

	first := append(rs.Write(in), rs.Flush()...)
	second := append(rs.Write(in), rs.Flush()...)

	if len(first) != len(second) {
		t.Fatalf("second utterance produced %d bytes, first produced %d", len(second), len(first))
	}
}
