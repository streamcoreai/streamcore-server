package pipeline

// Barge-in classification helpers: pure text predicates shared by the
// inbound reader and the turn buffer. Deliberately free of Pipeline state
// so they stay unit-testable.

import (
	"strings"
	"unicode"
)

// readbackPhraseMarkers are normalized word sequences that signal the
// agent's current utterance is a readback / explicit confirmation
// prompt (the moments when "yeah actually..." style soft corrections
// destroy context if allowed to barge in). The list is intentionally
// small and tied to wording the form-readback templates actually emit.
// Matched on word boundaries against the normalized agent text — a raw
// substring match would also fire on words that merely CONTAIN a marker.
var readbackPhraseMarkers = []string{
	"is that right",
	"is that correct",
	"let me read that back",
	"just to confirm",
	"could you spell",
}

// bargeInMinConfidence gates transcript barge-in on STT confidence so a
// noise-hallucinated transcript can't mute agent audio. Deepgram reports
// ≥0.8 for clear speech; hallucinations from line noise typically score
// well below 0.6.
const bargeInMinConfidence = 0.6

// isBackchannelOnly reports whether every token of the (already normalized)
// utterance is acknowledgement vocabulary — the combined-backchannel case.
func isBackchannelOnly(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	for _, tok := range tokens {
		if !backchannelWordTokens[tok] {
			return false
		}
	}
	return true
}

func normalizedTranscriptText(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '\'':
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func isMeaningfulBargeInTranscript(s string) bool {
	normalized := normalizedTranscriptText(s)
	if normalized == "" || backchannelWordTokens[normalized] {
		return false
	}

	tokens := strings.Fields(normalized)
	if len(tokens) == 0 {
		return false
	}

	for _, tok := range tokens {
		if bargeInCommandTokens[tok] {
			return true
		}
	}

	// Combined backchannels ("yep okay", "yeah yeah right") are still just
	// the caller signalling they're listening — checked after commands so
	// "okay stop" interrupts but "okay yeah" doesn't.
	if isBackchannelOnly(tokens) {
		return false
	}

	contentWords := 0
	for _, tok := range tokens {
		if len(tok) >= 3 && !bargeInWeakTokens[tok] {
			contentWords++
		}
	}

	// Require a content-bearing phrase ("change address", "billing question",
	// "what time") unless this is an explicit interruption command. This
	// keeps coughs, acknowledgements, and STT filler artifacts from cutting
	// off the agent without tying barge-in to any specific business domain.
	return contentWords >= 1 && len(tokens) >= 2
}

// isAcknowledgementOnly reports whether a whole turn is nothing but listening
// noises — "okay", "yeah yeah", "mm-hm right", "got it".
//
// Deliberately a much lower bar than isMeaningfulBargeInTranscript. That
// predicate decides whether to CUT THE AGENT OFF mid-word, so it can afford to
// be conservative and demand two tokens plus a content word. This one decides
// whether a turn is answered at all, so anything carrying meaning has to pass:
// one-word questions ("why?"), corrections ("no"), and commands ("stop") are
// all a single token and none of them are acknowledgement vocabulary.
func isAcknowledgementOnly(s string) bool {
	normalized := normalizedTranscriptText(s)
	if normalized == "" {
		return false
	}
	return isBackchannelOnly(strings.Fields(normalized))
}

func isSpeechActivityTranscript(s string) bool {
	normalized := normalizedTranscriptText(s)
	if normalized == "" {
		return false
	}
	return len([]rune(strings.ReplaceAll(normalized, " ", ""))) >= 2
}

// bargeInConfidenceOK reports whether an STT result is confident enough to
// drive barge-in. Zero means the provider didn't report a confidence —
// treated as OK because "unknown" must not be conflated with "low".
func bargeInConfidenceOK(conf float64) bool {
	return conf == 0 || conf >= bargeInMinConfidence
}

// isStrongBargeInCommand returns true only for the small set of commands
// that should always cut through a readback: "stop", "cancel", and
// "hang up" (which also subsumes "stop talking" via the "stop" token).
// Anything else — including the bargeInCommandTokens set used elsewhere
// — is treated as a weak correction when the readback guard is engaged.
func isStrongBargeInCommand(s string) bool {
	normalized := normalizedTranscriptText(s)
	if normalized == "" {
		return false
	}
	tokens := strings.Fields(normalized)
	for i, tok := range tokens {
		switch tok {
		case "stop", "cancel":
			return true
		case "hang":
			if i+1 < len(tokens) && tokens[i+1] == "up" {
				return true
			}
		}
	}
	return false
}

// endsTerminal reports whether the utterance ends with sentence-terminal
// punctuation — the strongest available signal that the speaker finished a
// complete thought. Used to shrink the merge window for the common case.
// Trailing quotes/brackets after the terminal mark are tolerated.
func endsTerminal(s string) bool {
	trimmed := strings.TrimSpace(s)
	trimmed = strings.TrimRight(trimmed, "'\")]}")
	if trimmed == "" {
		return false
	}
	switch trimmed[len(trimmed)-1] {
	case '.', '!', '?':
		return true
	}
	return false
}

func endsIncomplete(s string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(s))
	if trimmed == "" {
		return false
	}
	trimmed = strings.TrimRight(trimmed, ",.!?;:-—'\"")
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	last := strings.TrimRight(fields[len(fields)-1], ",.!?;:-—'\"")
	return incompleteTailWords[last]
}

// endsDictation reports whether the pending turn text looks like an
// in-progress spelled/numeric dictation: it ends on a separator word, a
// lone letter ("…n z y"), or a digit run ("…0 4 1 2"). Callers pause far
// longer between spelled segments than between words, and with 600ms
// endpointing those pauses otherwise fragment the dictation into multiple
// turns that garble email/phone extraction. Used ONLY for the turn-merge
// window — deliberately NOT part of endsIncomplete, which also drives the
// LLM's "caller trailed off" note (a complete spelled email legitimately
// ends on a letter).
func endsDictation(s string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(s))
	if trimmed == "" {
		return false
	}
	trimmed = strings.TrimRight(trimmed, ",.!?;:-—'\"")
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	last := strings.TrimRight(fields[len(fields)-1], ",.!?;:-—'\"")
	if dictationTailWords[last] {
		return true
	}
	if len(last) == 1 && last[0] >= 'a' && last[0] <= 'z' {
		return true
	}
	if last != "" {
		allDigits := true
		for _, r := range last {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return true
		}
	}
	return false
}

// backchannelWordTokens is the per-word vocabulary used to reject COMBINED
// backchannels ("yep okay", "yeah yeah, right") that the exact-phrase set
// above misses — a caller stacking acknowledgement words is still just
// listening, not interrupting. Includes common STT spelling variants
// ("mmhmm", "yup", "gotcha") so transcription drift doesn't punch through.
// Rejection requires EVERY token to be in this set, so adding common words
// like "it"/"i" here is safe — any real content word breaks the match.
var backchannelWordTokens = map[string]bool{
	"mm": true, "mhm": true, "mmhmm": true, "mhmm": true, "hmm": true, "hm": true,
	"uh": true, "huh": true, "uhhuh": true, "um": true, "umm": true,
	"yep": true, "yup": true, "yeah": true, "yea": true, "yes": true, "ya": true,
	"okay": true, "ok": true, "kay": true, "right": true, "sure": true,
	"alright": true, "cool": true, "nice": true, "wow": true, "oh": true,
	"ah": true, "aha": true, "gotcha": true, "got": true, "it": true,
	"i": true, "see": true, "true": true, "totally": true, "exactly": true,
	"thats": true, "that's": true,
}

var bargeInCommandTokens = map[string]bool{
	"stop": true, "wait": true, "pause": true, "cancel": true,
	"actually": true, "no": true, "hang": true, "hold": true,
	"repeat": true, "transfer": true, "operator": true, "human": true,
	"help": true,
}

var bargeInWeakTokens = map[string]bool{
	"i": true, "im": true, "i'm": true, "me": true, "my": true,
	"you": true, "your": true, "we": true, "it": true, "this": true,
	"that": true, "the": true, "a": true, "an": true,
	"and": true, "or": true, "but": true, "so": true, "to": true,
	"of": true, "for": true, "with": true, "on": true, "in": true,
	"at": true, "from": true, "about": true,
	"is": true, "are": true, "was": true, "were": true, "be": true,
	"can": true, "could": true, "would": true, "should": true,
	"will": true, "do": true, "does": true, "did": true,
	"need": true, "want": true,
	"um": true, "uh": true, "uhh": true, "umm": true, "hmm": true,
	"okay": true, "ok": true, "yeah": true, "yes": true, "yep": true,
	"right": true, "sure": true,
}

// Words that, when appearing as the last word of an utterance, strongly
// suggest the speaker was cut off by their own pause rather than finished.
var incompleteTailWords = map[string]bool{
	"um": true, "uh": true, "er": true, "hmm": true, "like": true,
	"and": true, "or": true, "but": true, "so": true, "because": true,
	"if": true, "when": true, "then": true, "while": true, "as": true,
	"that": true, "which": true, "who": true, "where": true, "why": true, "how": true,
	"the": true, "a": true, "an": true, "my": true, "your": true, "this": true,
	"to": true, "of": true, "for": true, "with": true, "at": true, "on": true, "in": true,
	"by": true, "from": true, "about": true, "into": true, "onto": true, "over": true, "under": true,
	"is": true, "are": true, "was": true, "were": true, "be": true, "been": true, "being": true,
	"am": true, "do": true, "does": true, "did": true, "have": true, "has": true, "had": true,
	"will": true, "would": true, "can": true, "could": true, "shall": true, "should": true,
	"may": true, "might": true, "must": true,
	"i": true, "you": true, "he": true, "she": true, "we": true, "they": true, "it": true,
}

// dictationTailWords are tokens that signal the caller is mid-dictation of
// an email, phone number, or spelled word — separators and provider names
// spoken between segments.
var dictationTailWords = map[string]bool{
	"dot": true, "dash": true, "hyphen": true, "underscore": true,
	"plus": true, "slash": true, "period": true, "point": true,
	"gmail": true, "hotmail": true, "outlook": true, "yahoo": true,
	"icloud": true, "mail": true,
}
