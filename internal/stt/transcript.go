package stt

import "strings"

// normalizeTranscript lowercases and collapses whitespace so two transcripts
// that differ only in spacing or case compare equal.
func normalizeTranscript(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// sameTranscript reports whether two transcripts are the same modulo case and
// whitespace.
func sameTranscript(a, b string) bool {
	return normalizeTranscript(a) == normalizeTranscript(b)
}

// mergeTranscriptChunks joins two consecutive final chunks from a streaming
// STT provider, removing the overlap they usually share. Providers commonly
// re-send the tail of the previous chunk at the head of the next one, so a
// naive concatenation yields "book a table book a table for two".
//
// The longest suffix of base that matches a prefix of next is treated as the
// overlap and dropped from next.
func mergeTranscriptChunks(base, next string) string {
	base = strings.TrimSpace(base)
	next = strings.TrimSpace(next)
	if base == "" {
		return next
	}
	if next == "" {
		return base
	}

	baseWords := strings.Fields(base)
	nextWords := strings.Fields(next)
	if len(baseWords) == 0 {
		return next
	}
	if len(nextWords) == 0 {
		return base
	}

	maxOverlap := min(len(baseWords), len(nextWords))
	overlap := 0
	for k := maxOverlap; k >= 1; k-- {
		match := true
		for i := 0; i < k; i++ {
			if !strings.EqualFold(baseWords[len(baseWords)-k+i], nextWords[i]) {
				match = false
				break
			}
		}
		if match {
			overlap = k
			break
		}
	}

	if preservesRepeatedSpokenDigitBoundary(baseWords, nextWords, overlap) {
		overlap = 0
	}

	if overlap == len(nextWords) {
		return strings.Join(baseWords, " ")
	}
	if overlap == len(baseWords) && len(nextWords) >= len(baseWords) {
		return strings.Join(nextWords, " ")
	}

	merged := append(append([]string{}, baseWords...), nextWords[overlap:]...)
	return strings.Join(merged, " ")
}

// preservesRepeatedSpokenDigitBoundary guards the one case where a genuine
// repetition must not be collapsed: a caller dictating digits. In "four five
// five / five six seven" the shared "five" is real speech, not provider
// overlap, and dropping it corrupts the phone number. Only a single-word
// overlap between digit tokens on both sides qualifies.
func preservesRepeatedSpokenDigitBoundary(baseWords, nextWords []string, overlap int) bool {
	if overlap != 1 || len(baseWords) < 2 || len(nextWords) < 2 {
		return false
	}
	word := strings.ToLower(baseWords[len(baseWords)-1])
	if word != strings.ToLower(nextWords[0]) || !isSpokenDigitToken(word) {
		return false
	}
	return isSpokenDigitToken(baseWords[len(baseWords)-2]) && isSpokenDigitToken(nextWords[1])
}

func isSpokenDigitToken(word string) bool {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9",
		"zero", "oh", "o", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine":
		return true
	default:
		return false
	}
}
