package audio

import (
	"github.com/godeps/opus"
)

const (
	SampleRate = 16000
	Channels   = 1
	FrameSize  = 320 // 20ms at 16kHz

	// MaxDecodeSize is the maximum samples opus_decode can return (120ms at 16kHz).
	// The decode buffer must be this large per the Opus spec.
	MaxDecodeSize = SampleRate * 120 / 1000 // 1920

	// RTPTimestampIncr is the RTP timestamp increment per 20ms frame.
	// Opus RTP clock is always 48kHz per RFC 7587, regardless of audio sample rate.
	RTPTimestampIncr = 960
)

type OpusDecoder struct {
	dec *opus.Decoder
	// scratch is the spec-mandated worst-case decode buffer (120ms). The
	// decoder first decodes into it, then copies the actual frame (typically
	// 320 samples = 640B) into a right-sized allocation for the caller.
	// Allocating MaxDecodeSize (3.84KB) per 20ms frame was ~190KB/s of GC
	// churn per call; the scratch reuse cuts that ~6×. Not safe for
	// concurrent use — one decoder per pipeline, called only from runReader.
	scratch []int16
}

func NewOpusDecoder() (*OpusDecoder, error) {
	dec, err := opus.NewDecoder(SampleRate, Channels)
	if err != nil {
		return nil, err
	}
	return &OpusDecoder{dec: dec, scratch: make([]int16, MaxDecodeSize)}, nil
}

// Decode decodes an Opus packet into PCM int16 samples. The returned slice
// is freshly allocated at the frame's actual size and safe to retain (frames
// flow through channels to other goroutines).
func (d *OpusDecoder) Decode(data []byte) ([]int16, error) {
	n, err := d.dec.Decode(data, d.scratch)
	if err != nil {
		return nil, err
	}
	pcm := make([]int16, n)
	copy(pcm, d.scratch[:n])
	return pcm, nil
}

type OpusEncoder struct {
	enc *opus.Encoder
	// buf is reused across Encode calls. Safe because the encoder is called
	// only from the single sender goroutine, which finishes marshalling the
	// RTP packet (copying the payload) before the next Encode.
	buf []byte
}

func NewOpusEncoder() (*OpusEncoder, error) {
	enc, err := opus.NewEncoder(SampleRate, Channels, opus.AppVoIP)
	if err != nil {
		return nil, err
	}
	if err := enc.SetBitrate(32000); err != nil {
		return nil, err
	}
	return &OpusEncoder{enc: enc, buf: make([]byte, 1024)}, nil
}

// Encode encodes PCM int16 samples into an Opus packet. The returned slice
// aliases the encoder's internal buffer and is only valid until the next
// Encode call — callers must finish with it (or copy) before encoding again.
// The sender path satisfies this: encodeAndSend marshals the RTP packet
// (which copies the payload) before returning.
func (e *OpusEncoder) Encode(pcm []int16) ([]byte, error) {
	n, err := e.enc.Encode(pcm, e.buf)
	if err != nil {
		return nil, err
	}
	return e.buf[:n], nil
}
