package pipeline

import (
	"strings"
	"testing"
)

func TestIsLowConfidenceTurn(t *testing.T) {
	cases := []struct {
		name       string
		conf       float64
		endsIncomp bool
		want       bool
	}{
		{"clearly low", 0.40, false, true},
		{"unknown (zero) never low", 0.0, false, false},
		{"good confidence", 0.90, false, false},
		{"just below floor", lowConfidenceRepromptFloor - 0.01, false, true},
		{"at floor (not below)", lowConfidenceRepromptFloor, false, false},
		{"low but trailing-off → defer to infer", 0.30, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isLowConfidenceTurn(c.conf, c.endsIncomp); got != c.want {
				t.Errorf("isLowConfidenceTurn(%.2f, ends=%v) = %v, want %v",
					c.conf, c.endsIncomp, got, c.want)
			}
		})
	}
}

func TestMisunderstandingDirectiveShapes(t *testing.T) {
	for name, d := range map[string]string{
		"clarify": misunderstandingDirective(),
		"narrow":  misunderstandingNarrowDirective(),
		"handoff": misunderstandingHandoffDirective(),
	} {
		if !strings.HasPrefix(d, "[System note:") || !strings.HasSuffix(d, "]") {
			t.Errorf("%s directive should be a bracketed system note: %q", name, d)
		}
	}
	// Level 1 asks to clarify; levels 2 and 3 explicitly forbid re-asking.
	if !strings.Contains(strings.ToLower(misunderstandingDirective()), "clarif") {
		t.Error("level 1 should ask to clarify")
	}
	for name, d := range map[string]string{
		"narrow":  misunderstandingNarrowDirective(),
		"handoff": misunderstandingHandoffDirective(),
	} {
		if !strings.Contains(d, "Do NOT ask them to repeat") {
			t.Errorf("%s directive must forbid repeat-loops: %q", name, d)
		}
	}
}

// Pipeline-level: consecutive low-confidence turns escalate clarify →
// multiple-choice → handoff, and a clear turn resets the ladder.
func TestMisunderstandingEscalationLadder(t *testing.T) {
	p := &Pipeline{}

	p.storeLastUserConfidence(0.3)
	if note := p.misunderstandingNote("blarghle wuh"); note != misunderstandingDirective() {
		t.Fatalf("turn 1: got %q, want clarify directive", note)
	}

	p.storeLastUserConfidence(0.3)
	if note := p.misunderstandingNote("mumble again"); note != misunderstandingNarrowDirective() {
		t.Fatalf("turn 2: got %q, want narrowing directive", note)
	}

	p.storeLastUserConfidence(0.3)
	if note := p.misunderstandingNote("still garbled"); note != misunderstandingHandoffDirective() {
		t.Fatalf("turn 3: got %q, want handoff directive", note)
	}

	// Stays at handoff level while the line stays bad.
	p.storeLastUserConfidence(0.3)
	if note := p.misunderstandingNote("more noise"); note != misunderstandingHandoffDirective() {
		t.Fatalf("turn 4: got %q, want handoff directive", note)
	}

	// A clear turn resets the ladder back to level 1.
	p.storeLastUserConfidence(0.95)
	if note := p.misunderstandingNote("I'd like to book a service"); note != "" {
		t.Fatalf("clear turn: got %q, want no directive", note)
	}
	p.storeLastUserConfidence(0.3)
	if note := p.misunderstandingNote("garbled once more"); note != misunderstandingDirective() {
		t.Fatalf("after reset: got %q, want clarify directive", note)
	}
}
