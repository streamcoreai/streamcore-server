package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Projector converts a completed voice turn into a Card.
//
// It holds no model client. The server already has one configured, and asking
// for a completion through the plugin protocol keeps one provider choice for
// the deployment rather than a second key that can drift from it.
type Projector struct {
	complete    func(system, prompt string) (string, error)
	fastPathMax int
}

// NewProjector builds a projector over a completion function.
func NewProjector(complete func(system, prompt string) (string, error), fastPathMax int) *Projector {
	if fastPathMax <= 0 {
		fastPathMax = defaultFastPath
	}
	return &Projector{complete: complete, fastPathMax: fastPathMax}
}

// Project turns one completed turn into a card.
func (p *Projector) Project(transcript, response string) (Card, error) {
	if card, ok := p.simpleCard(response); ok {
		return SanitizeCard(card)
	}

	input, err := json.Marshal(struct {
		Transcript string `json:"transcript"`
		Response   string `json:"response"`
	}{
		Transcript: transcript,
		Response:   response,
	})
	if err != nil {
		return Card{}, fmt.Errorf("encode projection input: %w", err)
	}

	output, err := p.complete(projectorSystemPrompt, string(input))
	if err != nil {
		return Card{}, fmt.Errorf("model projection: %w", err)
	}
	return DecodeModelCard([]byte(output))
}

const projectorSystemPrompt = `You create persistent screen cards for a small 400x300 display.

Convert the JSON object's user question and assistant answer into the smallest useful visual representation.

Choose exactly one layout: hero, text, list, or status.

Rules:
- Preserve important names, numbers, dates, times and units.
- Remove conversational filler.
- Do not repeat the user's question unless necessary for meaning.
- Prefer facts over prose.
- Prefer a large primary value when one fact dominates.
- Maximum title: 32 characters.
- Maximum primary: 24 characters.
- Maximum secondary: 48 characters.
- Maximum detail: 80 characters.
- Maximum body: 180 characters.
- Maximum 4 list items, each at most 40 characters.
- No markdown, HTML, commentary, or code fences.
- Output one valid JSON object only.

JSON shape:
hero/status: {"layout":"hero|status","title":"...","primary":"...","secondary":"optional"}
text: {"layout":"text","title":"...","body":"..."}
list: {"layout":"list","title":"...","items":["..."]}`

// simpleCard is deliberately conservative: only a short, single-line answer
// with no formatting is projected without another model call.
func (p *Projector) simpleCard(response string) (Card, bool) {
	limit := p.fastPathMax
	if limit <= 0 {
		limit = defaultFastPath
	}
	text := normalizeSpace(response)
	if text == "" || utf8.RuneCountInString(text) > limit ||
		strings.ContainsAny(text, "\n\r#*`_<>[]") {
		return Card{}, false
	}

	if utf8.RuneCountInString(text) <= MaxPrimary {
		primary := strings.TrimRight(text, ".!?")
		if primary != "" {
			return Card{Layout: "hero", Title: "Answer", Primary: primary}, true
		}
	}
	return Card{Layout: "text", Title: "Answer", Body: text}, true
}

// DecodeModelCard accepts only a single strict JSON object. Unknown fields are
