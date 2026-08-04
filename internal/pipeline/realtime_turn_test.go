package pipeline

import "testing"

func TestMergeUserFragments(t *testing.T) {
	cases := []struct {
		name string
		acc  string
		next string
		want string
	}{
		{"empty accumulator", "", "hello", "hello"},
		{"empty fragment", "hello", "", "hello"},
		{"both empty", "", "", ""},
		{
			// The cumulative case: each update restates the turn with
			// corrections folded in.
			name: "restatement supersedes",
			acc:  "Can you tell me what",
			next: "Can you tell me what StreamCore AI is?",
			want: "Can you tell me what StreamCore AI is?",
		},
		{
			// Identical re-delivery must not double the sentence.
			name: "duplicate collapses",
			acc:  "What model are you?",
			next: "What model are you?",
			want: "What model are you?",
		},
		{
			name: "stale fragment does not truncate the turn",
			acc:  "What model are you?",
			next: "What model",
			want: "What model are you?",
		},
		{
			// The segmented case: genuinely new words get appended.
			name: "continuation appends",
			acc:  "Can you tell me",
			next: "what the weather is",
			want: "Can you tell me what the weather is",
		},
		{"whitespace is normalised", "  hello  ", "  world  ", "hello world"},
		{
			name: "case-insensitive restatement",
			acc:  "can you tell me",
			next: "Can you tell me what StreamCore is",
			want: "Can you tell me what StreamCore is",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeUserFragments(tc.acc, tc.next); got != tc.want {
				t.Errorf("mergeUserFragments(%q, %q) = %q, want %q", tc.acc, tc.next, got, tc.want)
			}
		})
	}
}

// The reported bug: cumulative updates rendered as three separate "You"
// bubbles because each was forwarded as its own final.
func TestRealtimeUserTurnCollapsesCumulativeUpdates(t *testing.T) {
	var turn realtimeUserTurn

	turn.update("Can you tell me what's")
	turn.update("Can you tell me what StreamCore AI?")
	turn.update("Can you tell me what StreamCore AI is?")

	if got := turn.close(); got != "Can you tell me what StreamCore AI is?" {
		t.Errorf("turn text = %q, want the final revision only", got)
	}
}

// The same sentence arriving as both an update and a completion must not
// appear twice.
func TestRealtimeUserTurnCollapsesDuplicateCompletion(t *testing.T) {
	var turn realtimeUserTurn

	turn.update("What model are you?")
	turn.complete("What model are you?")

	if got := turn.close(); got != "What model are you?" {
		t.Errorf("turn text = %q, want the sentence once", got)
	}
}

// A caller who pauses mid-sentence produces several completed fragments for
// one question; they belong in one message.
func TestRealtimeUserTurnMergesSegmentedSpeech(t *testing.T) {
	var turn realtimeUserTurn

	turn.complete("Can you tell me")
	turn.complete("what the weather is in Tauranga")

	if got := turn.close(); got != "Can you tell me what the weather is in Tauranga" {
		t.Errorf("turn text = %q, want the segments merged", got)
	}
}

// A partial arriving after a completed fragment extends the turn rather than
// replacing the committed part.
func TestRealtimeUserTurnKeepsCommittedTextAcrossPartials(t *testing.T) {
	var turn realtimeUserTurn

	turn.complete("Book me a table")
	if got := turn.update("for four people"); got != "Book me a table for four people" {
		t.Errorf("interim text = %q, want committed text retained", got)
	}
	if got := turn.close(); got != "Book me a table for four people" {
		t.Errorf("turn text = %q", got)
	}
}

// close must empty the buffer, or the next turn inherits this one's words.
func TestRealtimeUserTurnResetsAfterClose(t *testing.T) {
	var turn realtimeUserTurn

	turn.update("first question")
	if got := turn.close(); got != "first question" {
		t.Fatalf("first turn = %q", got)
	}
	if got := turn.close(); got != "" {
		t.Errorf("second close returned %q, want empty", got)
	}

	turn.update("second question")
	if got := turn.close(); got != "second question" {
		t.Errorf("second turn = %q, want no bleed from the first", got)
	}
}

// A response with no caller speech before it (a greeting, or an idle
// check-in) must not emit an empty message.
func TestRealtimeUserTurnCloseWhenEmpty(t *testing.T) {
	var turn realtimeUserTurn

	if got := turn.close(); got != "" {
		t.Errorf("close() on an untouched turn = %q, want empty", got)
	}
}
