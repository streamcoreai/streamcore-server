package tts

import "math"

// streamResampler24to16 is the streaming counterpart to resample24kTo16k.
//
// The one-shot version treats its input as a complete utterance and lets the
// filter window run off both ends into implicit zeros. That is correct for a
// whole utterance and wrong for a chunk of one: applied per chunk it zero-pads
// at every boundary, so a response arriving in eighteen pieces gets seventeen
// discontinuities the caller hears as clicks.
//
// This keeps the input tail each output sample still needs, so the filter sees
// a continuous signal across chunk boundaries. Only the true end of the
// utterance gets edge treatment, via Flush.
type streamResampler24to16 struct {
	// hist holds input samples that later outputs still need. base is the
	// index of hist[0] in the overall input stream, so absolute sample
	// positions survive the trimming below.
	hist []float64
	base int
	// m is the next output index to produce, counted from the start of the
	// stream so the output phase never resets mid-utterance.
	m int
	// n counts every input sample written, including those already trimmed
	// out of hist. Flush needs it to produce exactly the sample count the
	// one-shot path would, which is derived from the total input length.
	n int
	// carry holds the leading byte of a sample split across two chunks.
	// Chunk boundaries fall wherever the provider put them, so dropping an
	// odd trailing byte would shift every following sample by one byte and
	// turn the rest of the utterance into noise.
	carry    byte
	hasCarry bool
}

// Write consumes a chunk of little-endian linear16 PCM at 24 kHz and returns
// the 16 kHz samples that are fully resolved by the data seen so far. It
// returns nil when a chunk is too short to complete another output sample.
func (r *streamResampler24to16) Write(in []byte) []byte {
	i := 0
	if r.hasCarry && len(in) > 0 {
		r.hist = append(r.hist, float64(int16(uint16(r.carry)|uint16(in[0])<<8)))
		r.n++
		r.hasCarry = false
		i = 1
	}
	for ; i+1 < len(in); i += 2 {
		r.hist = append(r.hist, float64(int16(uint16(in[i])|uint16(in[i+1])<<8)))
		r.n++
	}
	if i < len(in) {
		r.carry, r.hasCarry = in[i], true
	}
	return r.emit(false)
}

// Flush returns the remaining output, treating the stream as ended: the filter
// window may now run past the last input sample, exactly as the one-shot path
// allows at the end of an utterance.
func (r *streamResampler24to16) Flush() []byte {
	out := r.emit(true)
	r.hist, r.base, r.m, r.n = nil, 0, 0, 0
	r.carry, r.hasCarry = 0, false
	return out
}

// emit produces every output sample whose filter window is fully covered by
// the buffered input. With final set, it instead runs to the output length the
// one-shot path would produce for this much input, letting the last few
// windows overhang the end exactly as that path allows.
func (r *streamResampler24to16) emit(final bool) []byte {
	h := resampleKernel()
	half := (len(h) - 1) / 2
	last := r.base + len(r.hist) - 1 // index of the newest buffered sample
	if last < r.base {
		return nil
	}

	// Matches resample24kTo16k's outN for the whole stream, so a chunked
	// utterance and a whole one yield the same number of samples.
	outN := (r.n*resampleUp + resampleDown - 1) / resampleDown

	out := make([]byte, 0, 64)
	for {
		if final && r.m >= outN {
			break
		}
		center := r.m*resampleDown + half

		hi := center / resampleUp
		if hi > last {
			if !final {
				break // needs input that has not arrived yet
			}
			hi = last
		}

		lo := (center - len(h) + 1) / resampleUp
		if lo < 0 {
			lo = 0
		}
		if lo < r.base {
			// Should not happen — trimming below never drops a sample a later
			// output needs — but clamping keeps a bug from indexing out of range.
			lo = r.base
		}

		var acc float64
		for i := lo; i <= hi; i++ {
			if k := center - i*resampleUp; k >= 0 && k < len(h) {
				acc += r.hist[i-r.base] * h[k]
			}
		}

		v := math.Round(acc)
		if v > math.MaxInt16 {
			v = math.MaxInt16
		} else if v < math.MinInt16 {
			v = math.MinInt16
		}
		s := int16(v)
		out = append(out, byte(uint16(s)), byte(uint16(s)>>8))

		r.m++
	}

	r.trim(h)
	return out
}

// trim drops history the next output can no longer reach, bounding memory on
// a long utterance while keeping every sample the filter still needs.
func (r *streamResampler24to16) trim(h []float64) {
	center := r.m*resampleDown + (len(h)-1)/2
	keepFrom := (center - len(h) + 1) / resampleUp
	if keepFrom <= r.base {
		return
	}
	if drop := keepFrom - r.base; drop < len(r.hist) {
		r.hist = append([]float64(nil), r.hist[drop:]...)
		r.base = keepFrom
	}
}
