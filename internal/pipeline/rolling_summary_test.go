package pipeline

import (
	"strings"
	"testing"
)

func TestShouldRefreshSummary(t *testing.T) {
	cases := []struct {
		name                    string
		entries, lastAt         int
		generating, haveSummary bool
		want                    bool
	}{
		{"short call, no summary", 10, 0, false, false, false},
		{"already generating", 30, 0, true, false, false},
		{"activates at threshold", summaryActivateEntries, 0, false, false, true},
		{"have summary, too soon", summaryActivateEntries + 4, summaryActivateEntries, false, true, false},
		{"have summary, time to refresh", summaryActivateEntries + summaryRefreshEntries, summaryActivateEntries, false, true, true},
		{"just below activation", summaryActivateEntries - 1, 0, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldRefreshSummary(c.entries, c.lastAt, c.generating, c.haveSummary); got != c.want {
				t.Errorf("shouldRefreshSummary(%d,%d,%v,%v) = %v, want %v",
					c.entries, c.lastAt, c.generating, c.haveSummary, got, c.want)
			}
		})
	}
}

func TestFormatTranscriptForSummary(t *testing.T) {
	entries := []TranscriptEntry{
		{Role: "user", Text: "Hi, I'm Jason"},
		{Role: "agent", Text: "Hello Jason"},
		{Role: "user", Text: "  "}, // blank skipped
	}
	got := formatTranscriptForSummary(entries)
	if !strings.Contains(got, "Caller: Hi, I'm Jason") {
		t.Errorf("missing caller line: %q", got)
	}
	if !strings.Contains(got, "Agent: Hello Jason") {
		t.Errorf("missing agent line: %q", got)
	}
	if strings.Count(got, "\n") != 2 {
		t.Errorf("blank entry should be skipped, got %d lines: %q", strings.Count(got, "\n"), got)
	}
}

func TestFormatTranscriptForSummaryTruncates(t *testing.T) {
	long := strings.Repeat("x", summaryMaxChars*2)
	got := formatTranscriptForSummary([]TranscriptEntry{{Role: "user", Text: long}})
	if len(got) > summaryMaxChars {
		t.Errorf("expected <= %d chars, got %d", summaryMaxChars, len(got))
	}
}

func TestSummaryContextBlock(t *testing.T) {
	if got := summaryContextBlock("   "); got != "" {
		t.Errorf("blank summary should yield no block, got %q", got)
	}
	got := summaryContextBlock("Caller is Jason; wants a quote for a kitchen reno.")
	if !strings.HasPrefix(got, "[Conversation so far") || !strings.HasSuffix(got, "]") {
		t.Errorf("unexpected block format: %q", got)
	}
}
