package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	cardVersion = 1

	MaxTitle        = 32
	MaxPrimary      = 24
	MaxSecondary    = 48
	MaxDetail       = 80
	MaxBody         = 180
	MaxListItems    = 4
	MaxListItem     = 40
	defaultFastPath = 80
)

// Card is the deliberately small v1 display semantics payload. It contains no
// coordinates, fonts, colours, or pixels; rendering belongs to the client.
type Card struct {
	Layout    string   `json:"layout"`
	Title     string   `json:"title"`
	Primary   string   `json:"primary,omitempty"`
	Secondary string   `json:"secondary,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	Body      string   `json:"body,omitempty"`
	Items     []string `json:"items,omitempty"`
}

// DecodeModelCard accepts only a single strict JSON object. Unknown fields are
// rejected rather than guessed into the versioned device protocol.
func DecodeModelCard(data []byte) (Card, error) {
	var card Card
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&card); err != nil {
		return Card{}, fmt.Errorf("decode JSON object: %w", err)
	}
	if decoder.More() {
		return Card{}, fmt.Errorf("trailing data after JSON object")
	}
	card = normalizeModelCard(card)
	return SanitizeCard(card)
}

// normalizeModelCard repairs common, harmless field-selection mistakes before
// strict validation. The projector prompt asks for one layout-specific shape,
// but models sometimes include the generic example's optional fields anyway.
// Moving a missing required field into place is safer than discarding a whole
// completed turn's projection; SanitizeCard still performs the final checks.
func normalizeModelCard(card Card) Card {
	switch strings.ToLower(strings.TrimSpace(card.Layout)) {
	case "hero", "status":
		if card.Primary == "" && card.Body != "" {
			card.Primary = card.Body
		}
		card.Body = ""
		card.Items = nil
	case "text":
		if card.Body == "" && card.Primary != "" {
			card.Body = card.Primary
		}
		card.Primary = ""
		card.Items = nil
	case "list":
		if len(card.Items) == 0 && card.Primary != "" {
			card.Items = append(card.Items, card.Primary)
		}
		if len(card.Items) == 0 && card.Body != "" {
			card.Items = append(card.Items, card.Body)
		}
		card.Primary = ""
		card.Body = ""
	}
	return card
}

// SanitizeCard normalizes whitespace and enforces every v1 field limit before
// the card can leave the server. Markdown and HTML are rejected rather than
// sent for a constrained client to interpret.
func SanitizeCard(card Card) (Card, error) {
	card.Layout = strings.ToLower(strings.TrimSpace(card.Layout))
	card.Title = normalizeSpace(card.Title)
	card.Primary = normalizeSpace(card.Primary)
	card.Secondary = normalizeSpace(card.Secondary)
	card.Detail = normalizeSpace(card.Detail)
	card.Body = normalizeSpace(card.Body)

	for _, text := range []string{card.Title, card.Primary, card.Secondary, card.Detail, card.Body} {
		if containsMarkup(text) {
			return Card{}, fmt.Errorf("markup is not allowed in display.card")
		}
	}

	card.Title = truncateRunes(card.Title, MaxTitle)
	card.Primary = truncateRunes(card.Primary, MaxPrimary)
	card.Secondary = truncateRunes(card.Secondary, MaxSecondary)
	card.Detail = truncateRunes(card.Detail, MaxDetail)
	card.Body = truncateRunes(card.Body, MaxBody)

	itemLimit := min(len(card.Items), MaxListItems)
	items := make([]string, 0, itemLimit)
	for _, item := range card.Items[:itemLimit] {
		item = normalizeSpace(item)
		if containsMarkup(item) {
			return Card{}, fmt.Errorf("markup is not allowed in display.card items")
		}
		if item != "" {
			items = append(items, truncateRunes(item, MaxListItem))
		}
	}
	card.Items = items

	switch card.Layout {
	case "hero", "status":
		if card.Body != "" || len(card.Items) != 0 {
			return Card{}, fmt.Errorf("%s does not allow body or items", card.Layout)
		}
		card.Items = nil
		card.Body = ""
		if card.Title == "" || card.Primary == "" {
			return Card{}, fmt.Errorf("%s requires title and primary", card.Layout)
		}
	case "text":
		if card.Primary != "" || len(card.Items) != 0 {
			return Card{}, fmt.Errorf("text does not allow primary or items")
		}
		card.Items = nil
		card.Primary = ""
		if card.Title == "" || card.Body == "" {
			return Card{}, fmt.Errorf("text requires title and body")
		}
	case "list":
		if card.Primary != "" || card.Body != "" {
			return Card{}, fmt.Errorf("list does not allow primary or body")
		}
		card.Primary = ""
		card.Body = ""
		if card.Title == "" || len(card.Items) == 0 {
			return Card{}, fmt.Errorf("list requires title and at least one item")
		}
	default:
		return Card{}, fmt.Errorf("unknown layout %q", card.Layout)
	}
	return card, nil
}

func normalizeSpace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func containsMarkup(text string) bool {
	return strings.ContainsAny(text, "#*`_<>[]") || strings.Contains(text, "```")
}

func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}
