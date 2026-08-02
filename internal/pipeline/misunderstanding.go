package pipeline

import "log"

// lowConfidenceRepromptFloor is the STT-confidence ceiling below which a turn
// is treated as "probably garbled". Set well under the high-care floor (0.75)
// so this only fires on genuinely poor transcriptions — high-care already
// covers the moderate-confidence "verify by spelling" band, and we don't want
// the agent constantly asking callers to repeat themselves.
const lowConfidenceRepromptFloor = 0.55

// misunderstandingDirective (level 1) is the per-turn nudge appended on the
// FIRST consecutive low-confidence caller turn. It asks the LLM to clarify
// once rather than guess — distinct from high-care (which verifies captured
// field values) and from the trailed-off handler (which infers from context).
func misunderstandingDirective() string {
	return "[System note: The caller's last utterance came through with low transcription confidence and may be garbled. " +
		"If you cannot reasonably tell what they meant from the conversation so far, ask ONE short, friendly clarifying " +
		"question (e.g. \"Sorry, I didn't quite catch that — could you say it once more?\") instead of guessing. " +
		"Ask to clarify at most once; if it stays unclear you will receive different instructions.]"
}

// misunderstandingNarrowDirective (level 2) fires on the SECOND consecutive
// low-confidence turn. Repeating "could you say that again?" is a robotic
// loop, so narrow the space instead: offer the caller a short multiple
// choice grounded in what this business actually does.
func misunderstandingNarrowDirective() string {
	return "[System note: This is the second consecutive caller turn with very low transcription confidence. " +
		"Do NOT ask them to repeat again — that loop frustrates callers. Instead offer a short multiple-choice " +
		"clarification: name the two or three most likely things they could be asking about given this conversation " +
		"and this business, and ask which one they meant (e.g. \"I'm having a little trouble hearing you — are you " +
		"calling about booking an appointment, our opening hours, or something else?\"). One short sentence.]"
}

// misunderstandingHandoffDirective (level 3+) fires from the THIRD
// consecutive low-confidence turn. The line is effectively unusable for
// free-form conversation — stop re-asking and offer an alternative path.
func misunderstandingHandoffDirective() string {
	return "[System note: Multiple consecutive caller turns have been unintelligible. Do NOT ask them to repeat " +
		"again. Apologize briefly for the trouble. If a call-transfer or handoff tool is available in this session, " +
		"offer to connect them to a person now. Otherwise offer to take their name and phone number so someone can " +
		"call them back, or suggest calling again from a clearer line. Keep it warm and short.]"
}

// misunderstandingLevelDirective maps the consecutive low-confidence streak
// to the escalation level's directive.
func misunderstandingLevelDirective(streak int32) string {
	switch {
	case streak <= 1:
		return misunderstandingDirective()
	case streak == 2:
		return misunderstandingNarrowDirective()
	default:
		return misunderstandingHandoffDirective()
	}
}

// isLowConfidenceTurn is the pure per-turn decision: confidence is known
// (>0) and below the floor, and the utterance isn't a trailing-off fragment
// (the trailed-off handler owns that case — it infers from context rather
// than re-asking). Zero confidence means "unknown" (provider didn't
// report), never "low".
func isLowConfidenceTurn(confidence float64, endsIncompleteFlag bool) bool {
	if confidence <= 0 || confidence >= lowConfidenceRepromptFloor {
		return false
	}
	return !endsIncompleteFlag
}

// misunderstandingNote returns the escalation directive for this turn, or ""
// when the turn is clear. Consecutive low-confidence turns walk an
// escalation ladder — clarify once → multiple-choice narrowing → offer a
// person/callback — instead of the old behaviour of nudging once and then
// silently guessing. A clear or unknown-confidence turn resets the ladder.
func (p *Pipeline) misunderstandingNote(userText string) string {
	if p == nil {
		return ""
	}
	conf := p.lastUserConfidence()

	// Form/booking field answers have their own recovery (re-prompts, failed-
	// capture tracking); don't double up here.
	if p.previousAgentWasCollectingField() && looksLikeCollectedFieldAnswer(userText) {
		p.misunderstandingStreak.Store(0)
		return ""
	}

	if !isLowConfidenceTurn(conf, endsIncomplete(userText)) {
		// A clear (or unknown-confidence) turn resets the ladder. A low-
		// confidence trailing-off turn neither escalates nor resets — the
		// trailed-off handler owns that turn.
		if conf <= 0 || conf >= lowConfidenceRepromptFloor {
			p.misunderstandingStreak.Store(0)
		}
		return ""
	}

	streak := p.misunderstandingStreak.Add(1)
	log.Printf("[misunderstanding] low-confidence turn (conf=%.2f, consecutive=%d)", conf, streak)
	return misunderstandingLevelDirective(streak)
}
