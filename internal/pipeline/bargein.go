package pipeline

// Barge-in classification helpers: pure text predicates shared by the
// inbound reader and the turn buffer. Deliberately free of Pipeline state
// so they stay unit-testable.

import (
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------
// Normalization
// ---------------------------------------------------------------------------

// normalizedTranscriptText lowercases an utterance and reduces it to
// space-separated words, keeping only letters, digits, and apostrophes.
// Every predicate below operates on this form.
func normalizedTranscriptText(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '\'':
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// lastWord returns the final word of an utterance, lowercased and stripped of
// surrounding punctuation, or "" if there is none. Shared by the
// end-of-utterance predicates, which differ only in the vocabulary they
// match that word against.
func lastWord(s string) string {
	const punct = ",.!?;:-—'\""
	fields := strings.Fields(strings.ToLower(s))
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[len(fields)-1], punct)
}

// ---------------------------------------------------------------------------
// Barge-in predicates
// ---------------------------------------------------------------------------

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

// isMeaningfulBargeInTranscript reports whether a partial transcript is worth
// cutting the agent off for.
func isMeaningfulBargeInTranscript(s string) bool {
	tokens := strings.Fields(normalizedTranscriptText(s))
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
	return isBackchannelOnly(strings.Fields(normalizedTranscriptText(s)))
}

// isStrongBargeInCommand returns true only for the small set of commands
// that should always cut through a readback: "stop", "cancel", and
// "hang up" (which also subsumes "stop talking" via the "stop" token).
// Anything else — including the bargeInCommandTokens set used elsewhere
// — is treated as a weak correction when the readback guard is engaged.
func isStrongBargeInCommand(s string) bool {
	tokens := strings.Fields(normalizedTranscriptText(s))
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

// ---------------------------------------------------------------------------
// End-of-utterance predicates (turn merging)
// ---------------------------------------------------------------------------

// endsTerminal reports whether the utterance ends with sentence-terminal
// punctuation — the strongest available signal that the speaker finished a
// complete thought. Used to shrink the merge window for the common case.
// Trailing quotes/brackets after the terminal mark are tolerated.
func endsTerminal(s string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(s), "'\")]}")
	if trimmed == "" {
		return false
	}
	switch trimmed[len(trimmed)-1] {
	case '.', '!', '?':
		return true
	}
	return false
}

// endsIncomplete reports whether the utterance trails off mid-thought, e.g. on
// a conjunction, article, or filler word.
func endsIncomplete(s string) bool {
	return incompleteTailWords[lastWord(s)]
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
	last := lastWord(s)
	if last == "" {
		return false
	}
	if dictationTailWords[last] {
		return true
	}
	if len(last) == 1 && last[0] >= 'a' && last[0] <= 'z' {
		return true
	}
	for _, r := range last {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Vocabularies
// ---------------------------------------------------------------------------

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

// backchannelWordTokens is the per-word acknowledgement vocabulary. A turn is
// rejected as a backchannel only when EVERY token is in this set, so a caller
// stacking acknowledgement words ("yep okay", "yeah yeah, right") is still
// treated as listening rather than interrupting. Includes common STT spelling
// variants ("mmhmm", "yup", "gotcha") so transcription drift doesn't punch
// through. Because rejection requires a full match, listing common words like
// "it"/"i" here is safe — any real content word breaks it.
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

// bargeInCommandTokens are words that interrupt the agent on their own, no
// matter what else the utterance contains.
var bargeInCommandTokens = map[string]bool{
	"stop": true, "wait": true, "pause": true, "cancel": true,
	"actually": true, "no": true, "hang": true, "hold": true,
	"repeat": true, "transfer": true, "operator": true, "human": true,
	"help": true,
}

// bargeInWeakTokens carry no content on their own, so they don't count toward
// the content-word threshold that lets a phrase interrupt the agent.
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

// incompleteTailWords, as the last word of an utterance, strongly suggest the
// speaker was cut off by their own pause rather than finished.
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
