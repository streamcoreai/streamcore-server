package pipeline

import (
	"strings"
	"unicode"
)

// fillerWords are interjections, courtesy phrases, common English
// stopwords, written-out numbers, and date/time vocabulary — words
// whose presence in a user turn does not indicate the caller is
// asking a corpus-answerable question. When a turn contains nothing
// but words from this set (plus digits/punctuation), retrieval has
// nothing useful to anchor on and is skipped.
//
// The list is intentionally narrow: only words that almost never
// carry a content-bearing query on their own. Anything that could
// plausibly name a product, brand, place, or topic stays out — the
// gate's bias is to run RAG when in doubt.
var fillerWords = map[string]bool{
	// confirmations / negations / interjections
	"yes": true, "yeah": true, "yep": true, "yup": true,
	"no": true, "nope": true, "nah": true,
	"sure": true, "okay": true, "alright": true, "right": true, "fine": true,
	// courtesy
	"thanks": true, "thank": true, "please": true, "welcome": true,
	// greetings / sign-offs
	"hi": true, "hello": true, "hey": true, "bye": true, "goodbye": true,
	// hesitation / backchannel
	"um": true, "uh": true, "uhh": true, "umm": true, "hmm": true, "mhm": true,
	// pronouns / aux verbs / common english stopwords
	"the": true, "and": true, "but": true, "for": true, "not": true, "with": true,
	"you": true, "your": true, "yours": true, "yourself": true,
	"are": true, "was": true, "were": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "having": true,
	"can": true, "could": true, "will": true, "would": true,
	"shall": true, "should": true, "may": true, "might": true, "must": true,
	"this": true, "that": true, "these": true, "those": true,
	"what": true, "which": true, "who": true, "whom": true, "whose": true,
	"where": true, "when": true, "why": true, "how": true,
	"just": true, "very": true, "too": true, "also": true, "now": true,
	"some": true, "any": true, "all": true, "more": true, "most": true,
	"here": true, "there": true, "then": true, "than": true,
	"like": true, "want": true, "need": true,
	"about": true, "from": true, "into": true, "onto": true,
	// number words
	"one": true, "two": true, "three": true, "four": true, "five": true,
	"six": true, "seven": true, "eight": true, "nine": true, "ten": true,
	"eleven": true, "twelve": true, "thirteen": true, "fourteen": true,
	"fifteen": true, "sixteen": true, "seventeen": true, "eighteen": true, "nineteen": true,
	"twenty": true, "thirty": true, "forty": true, "fifty": true,
	"sixty": true, "seventy": true, "eighty": true, "ninety": true,
	"hundred": true, "thousand": true, "million": true,
	"first": true, "second": true, "third": true, "fourth": true, "fifth": true,
	// time-of-day / date words
	"morning": true, "afternoon": true, "evening": true, "night": true, "midday": true,
	"today": true, "tomorrow": true, "yesterday": true, "tonight": true,
	"oclock": true,
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true,
	"friday": true, "saturday": true, "sunday": true,
	"january": true, "february": true, "march": true, "april": true, "june": true,
	"july": true, "august": true, "september": true, "october": true, "november": true, "december": true,
	"week": true, "month": true, "year": true, "day": true, "hour": true, "minute": true,
	"next": true, "last": true,
}

// shouldSkipRAG returns (true, reason) when the user's utterance has
// essentially no chance of benefiting from corpus retrieval, so we
// can avoid an OpenAI embedding call and a Supabase RPC for it.
//
// The gate is intentionally conservative — when in doubt, run RAG.
//
// Skipped when:
//   - The utterance has no content-bearing words after stripping
//     fillers, common English stopwords, and number/date vocabulary.
//     A "content-bearing word" is any token of length >= 3 that is
//     not in fillerWords. Brand names and short product codes
//     (e.g. "BYD") therefore still trigger retrieval.
func shouldSkipRAG(p *Pipeline, userText string) (bool, string) {
	var b strings.Builder
	for _, r := range userText {
		if unicode.IsLetter(r) {
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteByte(' ')
		}
	}
	for _, tok := range strings.Fields(b.String()) {
		if len(tok) >= 3 && !fillerWords[tok] {
			return false, ""
		}
	}
	return true, "no content words"
}
