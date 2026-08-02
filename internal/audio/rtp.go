package audio

import (
	"encoding/binary"
	"log"
)

// PCMToLinear16Bytes converts int16 PCM samples to little-endian byte slice
// suitable for Deepgram STT.
func PCMToLinear16Bytes(pcm []int16) []byte {
	return PCMToLinear16BytesInto(nil, pcm)
}

// PCMToLinear16BytesInto is the allocation-free variant: it reuses buf when
// its capacity suffices (allocating only otherwise) and returns the encoded
// slice. Callers on per-frame hot paths keep one buffer and pass it back in
// every call — safe as long as the previous result is no longer referenced,
// which holds for the STT path (SendAudio implementations either write the
// bytes to the wire synchronously or copy them before returning).
func PCMToLinear16BytesInto(buf []byte, pcm []int16) []byte {
	need := len(pcm) * 2
	if cap(buf) < need {
		buf = make([]byte, need)
	}
	buf = buf[:need]
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(s))
	}
	return buf
}

// Linear16BytesToPCM converts little-endian byte slice to int16 PCM samples.
// linear16 frames are always even-length; an odd length means upstream framing
// corruption. We log it (the trailing byte is dropped) rather than panic, so a
// single bad frame can't take down the STT goroutine.
func Linear16BytesToPCM(data []byte) []int16 {
	if len(data)%2 != 0 {
		log.Printf("[audio] Linear16BytesToPCM: odd-length frame (%d bytes) — dropping trailing byte (upstream framing corruption?)", len(data))
	}
	pcm := make([]int16, len(data)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	return pcm
}
