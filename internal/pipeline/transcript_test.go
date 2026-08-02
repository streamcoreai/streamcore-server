package pipeline

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTranscriptEntry_JSONFormat(t *testing.T) {
	ts := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	entry := TranscriptEntry{Role: "user", Text: "Hello", At: ts}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed["role"] != "user" {
		t.Errorf("role = %v, want user", parsed["role"])
	}
	if parsed["text"] != "Hello" {
		t.Errorf("text = %v, want Hello", parsed["text"])
	}
	if _, ok := parsed["at"]; !ok {
		t.Error("missing 'at' field")
	}
}

func TestTranscriptEntry_RoundTrip(t *testing.T) {
	original := TranscriptEntry{
		Role: "agent",
		Text: "How can I help you today?",
		At:   time.Date(2025, 6, 15, 10, 30, 5, 0, time.UTC),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded TranscriptEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Role != original.Role {
		t.Errorf("role = %q, want %q", decoded.Role, original.Role)
	}
	if decoded.Text != original.Text {
		t.Errorf("text = %q, want %q", decoded.Text, original.Text)
	}
}

func TestTranscriptLog_Empty(t *testing.T) {
	log := &TranscriptLog{}

	if log.Len() != 0 {
		t.Errorf("Len() = %d, want 0", log.Len())
	}

	entries := log.Entries()
	if len(entries) != 0 {
		t.Errorf("Entries() returned %d items, want 0", len(entries))
	}

	j, err := log.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	if string(j) != "[]" {
		t.Errorf("JSON() = %s, want []", string(j))
	}
}

func TestTranscriptLog_AddAndEntries(t *testing.T) {
	log := &TranscriptLog{}

	log.Add("user", "I need a reservation")
	log.Add("agent", "For how many people?")
	log.Add("user", "Four")

	if log.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", log.Len())
	}

	entries := log.Entries()
	if len(entries) != 3 {
		t.Fatalf("len(Entries()) = %d, want 3", len(entries))
	}

	if entries[0].Role != "user" || entries[0].Text != "I need a reservation" {
		t.Errorf("entry[0] = %+v, wrong", entries[0])
	}
	if entries[1].Role != "agent" || entries[1].Text != "For how many people?" {
		t.Errorf("entry[1] = %+v, wrong", entries[1])
	}
	if entries[2].Role != "user" || entries[2].Text != "Four" {
		t.Errorf("entry[2] = %+v, wrong", entries[2])
	}

	for _, e := range entries {
		if e.At.IsZero() {
			t.Error("At should not be zero")
		}
	}
}

func TestTranscriptLog_EntriesIsCopy(t *testing.T) {
	log := &TranscriptLog{}
	log.Add("user", "hello")

	entries := log.Entries()
	entries[0] = TranscriptEntry{Role: "hacked", Text: "nope"}

	original := log.Entries()
	if original[0].Role == "hacked" {
		t.Error("Entries() returned a reference, not a copy")
	}
}

func TestTranscriptLog_JSONRoundTrip(t *testing.T) {
	log := &TranscriptLog{}
	log.Add("user", "Book a table for two")
	log.Add("agent", "What time works for you?")
	log.Add("user", "7pm tonight")
	log.Add("agent", "All set, see you at 7!")

	data, err := log.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var decoded []TranscriptEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded) != 4 {
		t.Fatalf("len = %d, want 4", len(decoded))
	}

	want := []struct{ role, text string }{
		{"user", "Book a table for two"},
		{"agent", "What time works for you?"},
		{"user", "7pm tonight"},
		{"agent", "All set, see you at 7!"},
	}
	for i, w := range want {
		if decoded[i].Role != w.role {
			t.Errorf("entry[%d].role = %q, want %q", i, decoded[i].Role, w.role)
		}
		if decoded[i].Text != w.text {
			t.Errorf("entry[%d].text = %q, want %q", i, decoded[i].Text, w.text)
		}
		if decoded[i].At.IsZero() {
			t.Errorf("entry[%d].At is zero", i)
		}
	}
}

func TestTranscriptLog_JSONIsValidJSONArray(t *testing.T) {
	log := &TranscriptLog{}
	log.Add("user", "test")

	data, err := log.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	if !json.Valid(data) {
		t.Errorf("JSON() produced invalid JSON: %s", string(data))
	}
	if data[0] != '[' || data[len(data)-1] != ']' {
		t.Errorf("JSON() = %s, want array", string(data))
	}
}

func TestTranscriptLog_JSONMatchesConversationTranscriptSchema(t *testing.T) {
	// Verify the JSON matches what the conversation_transcript JSONB column expects:
	// [{"role": "user"|"agent", "text": "...", "at": "<ISO-8601>"}]
	log := &TranscriptLog{}
	log.Add("user", "Hello")
	log.Add("agent", "Hi there!")

	data, err := log.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var raw []map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for i, entry := range raw {
		role, ok := entry["role"].(string)
		if !ok {
			t.Errorf("entry[%d]: 'role' is not a string", i)
			continue
		}
		if role != "user" && role != "agent" {
			t.Errorf("entry[%d].role = %q, want 'user' or 'agent'", i, role)
		}

		text, ok := entry["text"].(string)
		if !ok {
			t.Errorf("entry[%d]: 'text' is not a string", i)
			continue
		}
		if text == "" {
			t.Errorf("entry[%d].text is empty", i)
		}

		at, ok := entry["at"].(string)
		if !ok {
			t.Errorf("entry[%d]: 'at' is not a string", i)
			continue
		}
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			t.Errorf("entry[%d].at = %q is not valid RFC3339: %v", i, at, err)
		}
	}
}

func TestTranscriptLog_ConcurrentAdd(t *testing.T) {
	log := &TranscriptLog{}
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if n%2 == 0 {
				log.Add("user", "msg")
			} else {
				log.Add("agent", "msg")
			}
		}(i)
	}
	wg.Wait()

	if log.Len() != 100 {
		t.Errorf("Len() = %d, want 100", log.Len())
	}
}

func TestTranscriptLog_SpecialCharacters(t *testing.T) {
	log := &TranscriptLog{}
	log.Add("user", `He said "hello" and left`)
	log.Add("user", "Unicode: 日本語テスト 🎉")
	log.Add("user", "Newlines:\nline2\nline3")
	log.Add("user", `Back\slash`)

	data, err := log.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("produced invalid JSON: %s", string(data))
	}

	var decoded []TranscriptEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded[0].Text != `He said "hello" and left` {
		t.Errorf("quotes mangled: %q", decoded[0].Text)
	}
	if decoded[1].Text != "Unicode: 日本語テスト 🎉" {
		t.Errorf("unicode mangled: %q", decoded[1].Text)
	}
	if !strings.Contains(decoded[2].Text, "line2") {
		t.Errorf("newlines mangled: %q", decoded[2].Text)
	}
}

func TestTranscriptLog_EmptyTextAllowed(t *testing.T) {
	log := &TranscriptLog{}
	log.Add("user", "")

	if log.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", log.Len())
	}

	data, err := log.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var decoded []TranscriptEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded[0].Text != "" {
		t.Errorf("text = %q, want empty", decoded[0].Text)
	}
}

func TestMarshalTranscript_ProducesValidJSONB(t *testing.T) {
	// Simulate what marshalTranscript in manager.go does:
	// takes TranscriptSnapshot entries and json.Marshal's them.
	entries := []TranscriptEntry{
		{Role: "user", Text: "I want to book a table", At: time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)},
		{Role: "agent", Text: "Sure! For how many?", At: time.Date(2025, 6, 15, 10, 0, 3, 0, time.UTC)},
		{Role: "user", Text: "Four people", At: time.Date(2025, 6, 15, 10, 0, 8, 0, time.UTC)},
		{Role: "agent", Text: "Done!", At: time.Date(2025, 6, 15, 10, 0, 12, 0, time.UTC)},
	}

	j, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// This string would be passed to COALESCE($N::jsonb, ...) in SQL.
	// Verify PostgreSQL would accept it by checking it's valid JSON.
	if !json.Valid(j) {
		t.Fatalf("invalid JSON: %s", string(j))
	}

	// Verify it parses back to the same entries.
	var decoded []TranscriptEntry
	if err := json.Unmarshal(j, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded) != 4 {
		t.Fatalf("len = %d, want 4", len(decoded))
	}

	// Verify order is preserved (important for conversation chronology).
	if decoded[0].Text != "I want to book a table" {
		t.Errorf("entry[0] out of order: %q", decoded[0].Text)
	}
	if decoded[3].Text != "Done!" {
		t.Errorf("entry[3] out of order: %q", decoded[3].Text)
	}
}
