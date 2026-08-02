package tts

import "regexp"

var (
	mdImage      = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLink       = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdCodeFence  = regexp.MustCompile("(?s)```[A-Za-z0-9_-]*\\n?(.*?)```")
	mdInlineCode = regexp.MustCompile("`+([^`]+)`+")
	mdBold       = regexp.MustCompile(`\*\*+([^*]+)\*\*+`)
	mdItalicStar = regexp.MustCompile(`(^|[\s(])\*([^\s*][^*]*?)\*([\s.,!?;:)]|$)`)
	mdStrike     = regexp.MustCompile(`~~([^~]+)~~`)
	mdLineLead   = regexp.MustCompile(`(?m)^[ \t]*(?:#{1,6}|>+|[-*+]|\d+\.)[ \t]+`)
	multiSpace   = regexp.MustCompile(`[ \t]{2,}`)
)

// SanitizeForTTS removes Markdown formatting characters that TTS engines
// would otherwise read aloud literally (e.g. "asterisk asterisk Nissan
// Leaf"). It preserves the underlying text content. RAG chunks and LLM
// output frequently contain bullet lists, bold spans, and code fences;
// stripping them here ensures the synthesizer never voices a stray '*'.
func SanitizeForTTS(text string) string {
	if text == "" {
		return text
	}
	s := text
	s = mdImage.ReplaceAllString(s, "$1")
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdCodeFence.ReplaceAllString(s, "$1")
	s = mdInlineCode.ReplaceAllString(s, "$1")
	s = mdStrike.ReplaceAllString(s, "$1")
	s = mdBold.ReplaceAllString(s, "$1")
	s = mdItalicStar.ReplaceAllString(s, "$1$2$3")
	s = mdLineLead.ReplaceAllString(s, "")
	s = multiSpace.ReplaceAllString(s, " ")
	return s
}
