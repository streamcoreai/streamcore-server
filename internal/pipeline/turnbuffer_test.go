package pipeline

import (
	"testing"
	"time"
)

func TestMergeWait(t *testing.T) {
	const base = 350 * time.Millisecond

	cases := []struct {
		name string
		text string
		want time.Duration
	}{
		// Terminal punctuation beats a pronoun tail: the provider already judged
		// the thought complete, so the tail word is not evidence of a pause.
		{"punctuated sentence ending on a pronoun", "I already fixed it.", base},
		{"punctuated question", "can you check it?", base},
		{"punctuated exclamation", "that worked!", base},

		// The case the tail list exists for — same words, no closer.
		{"unpunctuated pronoun tail", "I already fixed it", turnMergeIncomplete},
		{"dangling conjunction", "I tried that and", turnMergeIncomplete},
		{"filler tail", "so it was, um", turnMergeIncomplete},

		// Dictation still extends: callers pause much longer between spelled
		// segments than between words.
		{"mid-dictation separator", "my email is gary dot", turnMergeIncomplete},
		{"spelled letters", "it's g a r", turnMergeIncomplete},
		{"digit run", "the number is 0 4 1 2", turnMergeIncomplete},
		{"completed address is not mid-dictation", "gary@example.com.", base},

		{"ordinary finished clause", "yes that's right", base},
		{"empty", "", base},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mergeWait(c.text, base); got != c.want {
				t.Errorf("mergeWait(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}
