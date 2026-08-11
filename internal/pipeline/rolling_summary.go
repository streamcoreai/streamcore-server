package pipeline

import (
	"context"
	"log"
	"strings"
	"time"
)

// Rolling-summary tuning. Entries count both user and agent turns, so an
// "exchange" is ~2 entries.
const (
	// summaryActivateEntries is the transcript size at which summarization
	// begins. The LLM client keeps ~30 messages (≈15 exchanges) before it
	// drops the oldest, so we start a few exchanges earlier to have a summary
	// ready before any context is actually lost.
	summaryActivateEntries = 24

	// summaryRefreshEntries is how many new transcript entries accumulate
	// before the summary is regenerated. ~6 exchanges keeps it current
	// without an LLM call every turn.
	summaryRefreshEntries = 12

	// summaryMaxChars bounds the input we send to the summarizer so a very
	// long call can't balloon the prompt. We keep the most recent characters.
	summaryMaxChars = 8000

	// summaryGenerateTimeout bounds one background summarization call. A
	// slow/wedged provider must not hold the summaryGenerating slot forever
	// — that would silently stop summaries refreshing for the rest of the
	// call. Generous next to a normal 1-3s OneShot; this is a wedge guard,
	// not a latency target (the live turn never waits on this call).
	summaryGenerateTimeout = 10 * time.Second
)

// summarySystemPrompt instructs the model to retain only durable facts.
const summarySystemPrompt = "You maintain a running memory of a live phone call between an AI receptionist and a caller. " +
	"Summarize ONLY the durable facts the agent must not forget for the rest of the call: " +
	"the caller's name and contact details, what they want (their goal/intent), any decisions or commitments made, " +
	"and specifics already discussed (dates, times, items, prices, addresses). " +
	"Omit pleasantries and filler. Be concise — at most a few short lines. Output only the summary text, no preamble."

// shouldRefreshSummary decides whether respond() should kick off a new
// background summarization this turn. Pure so it can be unit-tested.
//   - entries: current transcript entry count
//   - lastAt: entry count when the last summary was produced
//   - generating: whether a background summary is already running
//   - haveSummary: whether a summary already exists
func shouldRefreshSummary(entries, lastAt int, generating, haveSummary bool) bool {
	if generating {
		return false
	}
	if entries < summaryActivateEntries {
		return false
	}
	if !haveSummary {
		return true
	}
	return entries-lastAt >= summaryRefreshEntries
}

// formatTranscriptForSummary renders transcript entries into a plain script for
// the summarizer, keeping at most summaryMaxChars from the end. Pure.
func formatTranscriptForSummary(entries []TranscriptEntry) string {
	var b strings.Builder
	for _, e := range entries {
		text := strings.TrimSpace(e.Text)
		if text == "" {
			continue
		}
		speaker := "Caller"
		if e.Role != "user" { // "agent" and "agent_filler" are both the agent speaking
			speaker = "Agent"
		}
		b.WriteString(speaker)
		b.WriteString(": ")
		b.WriteString(text)
		b.WriteString("\n")
	}
	s := b.String()
	if len(s) > summaryMaxChars {
		s = s[len(s)-summaryMaxChars:]
	}
	return s
}

// summaryContextBlock wraps a summary for injection into the LLM context, or
// returns "" when there's nothing to inject. Pure.
func summaryContextBlock(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	return "[Conversation so far (durable facts — do not re-ask what's already known):\n" + summary + "]"
}

// currentRollingSummary returns the latest background-generated summary, or "".
func (p *Pipeline) currentRollingSummary() string {
	s, _ := p.rollingSummary.Load().(string)
	return s
}

// maybeRefreshRollingSummary kicks off a background summary regeneration when
// the call is long enough and it's time. Non-blocking: the live turn never
// waits on the LLM call. Safe to call every turn from respond().
func (p *Pipeline) maybeRefreshRollingSummary() {
	if p == nil || p.llmClient == nil {
		return
	}
	entries := p.transcriptLog.Len()
	haveSummary := p.currentRollingSummary() != ""
	if !shouldRefreshSummary(entries, int(p.lastSummaryAtEntries.Load()), p.summaryGenerating.Load(), haveSummary) {
		return
	}
	// Claim the slot; bail if another goroutine beat us to it.
	if !p.summaryGenerating.CompareAndSwap(false, true) {
		return
	}

	script := formatTranscriptForSummary(p.transcriptLog.Entries())
	if strings.TrimSpace(script) == "" {
		p.summaryGenerating.Store(false)
		return
	}

	// Use the call-scoped context (p.ctx), not the per-turn respCtx, so a
	// barge-in cancelling the current turn doesn't abort the summary.
	go func(atEntries int32) {
		defer p.recoverKeepAlive("generateRollingSummary")
		defer p.summaryGenerating.Store(false)
		genCtx, cancel := context.WithTimeout(p.ctx, summaryGenerateTimeout)
		defer cancel()
		start := time.Now()
		summary, err := p.llmClient.OneShot(genCtx, summarySystemPrompt, script)
		if err != nil {
			if genCtx.Err() == context.DeadlineExceeded {
				log.Printf("[summary] rolling summary timed out after %s — will retry on a later turn", time.Since(start).Round(time.Millisecond))
			} else {
				log.Printf("[summary] rolling summary failed: %v", err)
			}
			return
		}
		summary = strings.TrimSpace(summary)
		if summary == "" {
			return
		}
		p.rollingSummary.Store(summary)
		p.lastSummaryAtEntries.Store(atEntries)
		log.Printf("[summary] rolling summary refreshed at %d entries (%d chars)", atEntries, len(summary))
	}(int32(entries))
}
