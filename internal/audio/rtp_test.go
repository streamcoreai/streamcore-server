package audio

import "testing"

func TestLinear16RoundTrip(t *testing.T) {
	in := []int16{0, 1, -1, 32767, -32768, 1234}
	got := Linear16BytesToPCM(PCMToLinear16Bytes(in))
	if len(got) != len(in) {
		t.Fatalf("len = %d, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("sample %d = %d, want %d", i, got[i], in[i])
		}
	}
}

// TestPCMToLinear16BytesIntoReusesBuffer verifies the hot-path variant
// reuses a sufficient buffer (no allocation) and still round-trips
// correctly, including when the buffer is too small and must grow.
func TestPCMToLinear16BytesIntoReusesBuffer(t *testing.T) {
	in := []int16{0, 1, -1, 32767, -32768, 1234}
	buf := make([]byte, 0, len(in)*2)

	out := PCMToLinear16BytesInto(buf, in)
	if &out[0] != &buf[:1][0] {
		t.Error("expected output to reuse the provided buffer's backing array")
	}
	got := Linear16BytesToPCM(out)
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("sample %d = %d, want %d", i, got[i], in[i])
		}
	}

	// Too-small buffer must grow without corrupting data.
	small := make([]byte, 0, 2)
	out2 := PCMToLinear16BytesInto(small, in)
	got2 := Linear16BytesToPCM(out2)
	for i := range in {
		if got2[i] != in[i] {
			t.Errorf("grown buffer sample %d = %d, want %d", i, got2[i], in[i])
		}
	}
}

// TestLinear16BytesToPCMOddLength verifies an odd-length frame is handled
// gracefully (trailing byte dropped, no panic) rather than crashing the STT
// goroutine — see CODE_REVIEW.md finding #6.
func TestLinear16BytesToPCMOddLength(t *testing.T) {
	got := Linear16BytesToPCM([]byte{0x01, 0x02, 0x03}) // 3 bytes → 1 sample
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0] != int16(0x0201) {
		t.Errorf("sample 0 = %d, want %d", got[0], int16(0x0201))
	}
}
