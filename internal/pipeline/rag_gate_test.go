package pipeline

import "testing"

// The gate exists to keep turns that cannot anchor a vector search off the
// retrieval path. A turn of pure acknowledgement has nothing to embed, so the
// round trip is wasted latency on the caller's critical path.
func TestShouldSkipRAG(t *testing.T) {
	cases := []struct {
		name string
		text string
		skip bool
	}{
		{"pure backchannel", "okay sure thanks", true},
		{"filler only", "um yeah right", true},
		{"empty", "", true},
		{"digits and fillers only", "yeah okay 2", true},
		{"real question", "what are your opening hours", false},
		{"short brand name still retrieves", "do you stock BYD", false},
		{"product question", "how much is the annual subscription", false},
		{"single content word", "parking", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := shouldSkipRAG(nil, tc.text)
			if got != tc.skip {
				t.Fatalf("shouldSkipRAG(%q) = %v (%s), want %v", tc.text, got, reason, tc.skip)
			}
			if got && reason == "" {
				t.Error("a skip must report a reason for the turn-timing log")
			}
		})
	}
}
