package pipeline

import (
	"strings"
)

// lastUserConfidence returns the STT confidence of the most recent final
// caller turn, or 0 when the provider reported none.
func (p *Pipeline) lastUserConfidence() float64 {
	if p == nil {
		return 0
	}
	c, _ := p.userConfidence.Load().(float64)
	return c
}

func (p *Pipeline) storeLastUserConfidence(c float64) {
	if p != nil {
		p.userConfidence.Store(c)
	}
}

// readbackBargeInGuardEnabled reports whether weak barge-ins should be
// ignored while the agent is reading values back for confirmation.
func (p *Pipeline) readbackBargeInGuardEnabled() bool {
	return p != nil && p.cfg != nil && p.cfg.Pipeline.ReadbackBargeInGuardEnabled
}

// readbackInProgress reports whether the agent is currently speaking a
// confirmation readback, judged from what it is saying right now.
func (p *Pipeline) readbackInProgress() bool {
	if p == nil || !p.speaking.Load() {
		return false
	}
	txt, _ := p.lastAgentText.Load().(string)
	if txt == "" {
		return false
	}
	normalized := " " + normalizedTranscriptText(txt) + " "
	for _, m := range readbackPhraseMarkers {
		if strings.Contains(normalized, " "+m+" ") {
			return true
		}
	}
	return false
}

// previousAgentWasCollectingField reports whether the agent's last turn was a
// question asking the caller for a specific value. A terse reply to such a
// question is an answer, not a misunderstanding.
func (p *Pipeline) previousAgentWasCollectingField() bool {
	if p == nil || p.transcriptLog == nil {
		return false
	}
	entries := p.transcriptLog.Entries()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Role != "agent" {
			continue
		}
		return isCollectionPrompt(entries[i].Text)
	}
	return false
}

// isCollectionPrompt matches an agent question that asks for a concrete value.
func isCollectionPrompt(text string) bool {
	t := normalizedTranscriptText(text)
	if t == "" || !strings.Contains(text, "?") {
		return false
	}
	if strings.Contains(t, "is that all correct") || strings.Contains(t, "anything youd like to change") {
		return false
	}
	for _, cue := range collectionPromptCues {
		if strings.Contains(t, cue) {
			return true
		}
	}
	return false
}

var collectionPromptCues = []string{
	"what is your", "whats your", "can i get your", "could i get your",
	"may i have your", "can i have your", "what name", "your name",
	"your number", "phone number", "your email", "email address",
	"what date", "what time", "how many",
}

// looksLikeCollectedFieldAnswer reports whether a short caller utterance reads
// as a direct answer (a name, a number) rather than a garbled turn.
func looksLikeCollectedFieldAnswer(text string) bool {
	t := normalizedTranscriptText(text)
	if t == "" || strings.Contains(text, "?") {
		return false
	}
	words := strings.Fields(t)
	if len(words) == 0 || len(words) > 8 {
		return false
	}
	for _, word := range words {
		if !fillerWords[word] {
			return true
		}
	}
	return false
}

// ragSearchQuery returns the text to retrieve on for this turn.
func (p *Pipeline) ragSearchQuery(userText string) string {
	return strings.TrimSpace(userText)
}
