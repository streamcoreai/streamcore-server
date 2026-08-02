package pipeline

import (
	"encoding/json"
	"sync"
	"time"
)

// TranscriptEntry represents a single turn in a conversation transcript.
type TranscriptEntry struct {
	Role string    `json:"role"` // "user" or "agent"
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// TranscriptLog is a thread-safe accumulator for conversation transcript entries.
type TranscriptLog struct {
	mu      sync.Mutex
	entries []TranscriptEntry
}

// Add appends a new transcript entry with the given role and text, timestamped at time.Now().
func (l *TranscriptLog) Add(role, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, TranscriptEntry{
		Role: role,
		Text: text,
		At:   time.Now(),
	})
}

// Entries returns a copy of the current transcript entries.
func (l *TranscriptLog) Entries() []TranscriptEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]TranscriptEntry, len(l.entries))
	copy(out, l.entries)
	return out
}

// JSON marshals the transcript entries to JSON. It returns [] if there are no entries.
func (l *TranscriptLog) JSON() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(l.entries)
}

// Len returns the number of transcript entries.
func (l *TranscriptLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// TranscriptCallback is a function that receives transcript events as they occur.
type TranscriptCallback func(role, text string)

// NoopTranscriptCallback is a TranscriptCallback that does nothing.
var NoopTranscriptCallback TranscriptCallback = func(string, string) {}
