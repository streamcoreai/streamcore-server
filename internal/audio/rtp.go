package audio

import (
	"encoding/binary"
)

// PCMToLinear16Bytes converts int16 PCM samples to little-endian byte slice
// suitable for Deepgram STT.
func PCMToLinear16Bytes(pcm []int16) []byte {
	buf := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(s))
	}
	return buf
}

// Linear16BytesToPCM converts little-endian byte slice to int16 PCM samples.
func Linear16BytesToPCM(data []byte) []int16 {
	pcm := make([]int16, len(data)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	return pcm
}
