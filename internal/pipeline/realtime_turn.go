package pipeline

import (
	"strings"
	"sync"
)

// realtimeUserTurn assembles one spoken turn from the fragments a
// speech-to-speech provider emits for it.
//
// The provider's VAD segments a caller's speech, and a caller who pauses
// mid-sentence ("Can you tell me what... StreamCore AI is?") produces several
// completed transcription items for what is plainly one question. The client
// SDK treats every final transcript as a committed message, so forwarding
// each fragment as final renders the same sentence as several separate "You"
// bubbles, each a longer revision of the last.
//
// This is the realtime-mode counterpart to turnBuffer on the classic path.
// The classic version debounces on a timer because it also decides when the
// agent should reply; here the provider already owns that decision, so the
// turn is closed by the provider itself starting a response.
type realtimeUserTurn struct {
	mu sync.Mutex
	// committed holds fragments the provider has finalised for this turn.
	committed string
	// partial is the in-progress fragment, replaced as it is revised.
	partial string
}

// update records a revised in-progress fragment and returns the full text of
// the turn so far.
func (t *realtimeUserTurn) update(text string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.partial = text
	return t.textLocked()
}

// complete folds a finalised fragment into the turn and returns the full text
// so far. The turn is not closed — more fragments may still arrive.
func (t *realtimeUserTurn) complete(text string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.committed = mergeUserFragments(t.committed, text)
	t.partial = ""
	return t.textLocked()
}

// close ends the turn and returns its final text, emptying the buffer.
func (t *realtimeUserTurn) close() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	text := t.textLocked()
	t.committed, t.partial = "", ""
	return text
}

func (t *realtimeUserTurn) textLocked() string {
	return mergeUserFragments(t.committed, t.partial)
}

// mergeUserFragments joins two pieces of a caller's turn.
//
// Providers differ in what each fragment contains, and xAI's transcription
// events are explicitly cumulative — an update may revise words it already
// emitted. So a fragment is either a continuation to append or a restatement
// that supersedes what came before, and the two cases have to be told apart:
// appending a restatement duplicates the sentence, while replacing a
// continuation loses its first half.
func mergeUserFragments(acc, next string) string {
	acc, next = strings.TrimSpace(acc), strings.TrimSpace(next)
	switch {
	case acc == "":
		return next
	case next == "":
		return acc
	}

	// A restatement of the whole turn so far: keep the newer wording, which
	// carries the provider's corrections.
	if containsFold(next, acc) {
		return next
	}
	// Already covered by what we have — a duplicate delivery.
	if containsFold(acc, next) {
		return acc
	}
	return acc + " " + next
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
