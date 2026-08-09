package tts

import (
	"bytes"
	"context"
	"testing"
)

// A provider may split PCM anywhere, including mid-sample. The aligner must
// reassemble the original byte stream exactly, only ever deferring a trailing
// odd byte to the next chunk.
func TestPCMAlignerPreservesStream(t *testing.T) {
	src := make([]byte, 32)
	for i := range src {
		src[i] = byte(i)
	}

	// Deliberately odd-sized splits so most boundaries land mid-sample.
	splits := [][]int{
		{1, 1, 1, 1, 28},
		{3, 5, 7, 17},
		{31, 1},
		{32},
	}

	for _, split := range splits {
		var align pcmAligner
		var got []byte
		off := 0
		for _, n := range split {
			out := align.take(src[off : off+n])
			if len(out)%2 != 0 {
				t.Fatalf("split %v: emitted odd-length chunk of %d bytes", split, len(out))
			}
			got = append(got, out...)
			off += n
		}
		if !bytes.Equal(got, src) {
			t.Errorf("split %v: reassembled %v, want %v", split, got, src)
		}
	}
}

// An odd total leaves one byte with no partner; it is held, never emitted.
func TestPCMAlignerHoldsFinalOddByte(t *testing.T) {
	var align pcmAligner
	got := align.take([]byte{1, 2, 3})
	if !bytes.Equal(got, []byte{1, 2}) {
		t.Fatalf("take = %v, want [1 2]", got)
	}
	if !align.hasCarry || align.carry != 3 {
		t.Errorf("carry = (%v, %d), want (true, 3)", align.hasCarry, align.carry)
	}
	// An empty follow-up must not flush a lone byte as if it were a sample.
	if out := align.take(nil); len(out) != 0 {
		t.Errorf("take(nil) = %v, want empty", out)
	}
}

func TestDrainForReuseStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	body := bytes.NewReader(make([]byte, 4096))
	drainForReuse(ctx, body)

	// On barge-in the provider is still sending; draining would mean waiting
	// on audio nobody hears, so a cancelled ctx must skip the read entirely.
	if body.Len() != 4096 {
		t.Errorf("drained %d bytes on a cancelled ctx, want 0", 4096-body.Len())
	}
}

func TestDrainForReuseIsBounded(t *testing.T) {
	body := bytes.NewReader(make([]byte, drainLimit+2048))
	drainForReuse(context.Background(), body)

	if got := drainLimit + 2048 - body.Len(); got != drainLimit {
		t.Errorf("drained %d bytes, want the %d-byte cap", got, drainLimit)
	}
}
