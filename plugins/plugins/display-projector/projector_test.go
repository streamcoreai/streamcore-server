package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	streamcore "github.com/streamcoreai/plugin-sdk/go"
)

// stubModel stands in for the server's model. Its calls channel records what
// the projector actually sent, so a test can assert the turn reached the model
// intact rather than only that a card came back.
type stubModel struct {
	calls    chan string
	response string
	err      error
}

func (s *stubModel) complete(_, prompt string) (string, error) {
	if s.calls != nil {
		s.calls <- prompt
	}
	return s.response, s.err
}

func failingModel(t *testing.T) func(string, string) (string, error) {
	return func(string, string) (string, error) {
		t.Helper()
		t.Fatal("the fast path should not have reached the model")
		return "", nil
	}
}

func completedEvent() streamcore.Event {
	return streamcore.Event{
		Type:      "assistant.response.completed",
		SessionID: "session-7",
		TurnID:    "turn_7",
		TurnSeq:   7,
	}
}

func emittedCard(t *testing.T, result any) cardEvent {
	t.Helper()
	out, ok := result.(streamcore.Result)
	if !ok {
		t.Fatalf("result = %T, want streamcore.Result", result)
	}
	if len(out.Emit) != 1 || out.Emit[0].Topic != cardTopic {
		t.Fatalf("emissions = %+v", out.Emit)
	}
	payload, ok := out.Emit[0].Payload.(cardEvent)
	if !ok {
		t.Fatalf("payload = %T, want cardEvent", out.Emit[0].Payload)
	}
	return payload
}

func TestSimpleResponseUsesFastPathWithoutModel(t *testing.T) {
	projector := NewProjector(failingModel(t), 80)

	result, err := projectTurn(projector, completedEvent(), completedTurn{
		Transcript: "What is the capital of France?",
		Response:   " Paris. ",
	})
	if err != nil {
		t.Fatalf("projectTurn: %v", err)
	}

	event := emittedCard(t, result)
	if event.Type != cardType || event.Version != cardVersion {
		t.Errorf("envelope = %+v", event)
	}
	if event.SessionID != "session-7" || event.TurnID != "turn_7" || event.TurnSeq != 7 {
		t.Errorf("envelope lost its turn identity: %+v", event)
	}
	if event.Card.Layout != "hero" || event.Card.Primary != "Paris" {
		t.Errorf("card = %+v", event.Card)
	}
}

func TestLongerSimpleResponseUsesTextFastPath(t *testing.T) {
	projector := NewProjector(failingModel(t), 80)
	response := "The shop opens at nine and closes at five on weekdays"

	result, err := projectTurn(projector, completedEvent(), completedTurn{
		Transcript: "When does it open?",
		Response:   response,
	})
	if err != nil {
		t.Fatalf("projectTurn: %v", err)
	}

	card := emittedCard(t, result).Card
	if card.Layout != "text" || card.Body != response {
		t.Errorf("card = %+v", card)
	}
}

func TestComplexResponseGoesThroughTheServersModel(t *testing.T) {
	model := &stubModel{
		calls:    make(chan string, 1),
		response: `{"layout":"hero","title":"Auckland · Tomorrow","primary":"17°C","secondary":"Rain after lunch"}`,
	}
	projector := NewProjector(model.complete, 24)

	result, err := projectTurn(projector, completedEvent(), completedTurn{
		Transcript: "What is the weather tomorrow?",
		Response:   "Tomorrow in Auckland will be mostly cloudy with rain around lunchtime.",
	})
	if err != nil {
		t.Fatalf("projectTurn: %v", err)
	}

	select {
	case input := <-model.calls:
		var decoded struct {
			Transcript string `json:"transcript"`
			Response   string `json:"response"`
		}
		if err := json.Unmarshal([]byte(input), &decoded); err != nil {
			t.Fatalf("projection input was not JSON: %v", err)
		}
		if decoded.Transcript == "" || decoded.Response == "" {
			t.Fatalf("projection input omitted a turn half: %+v", decoded)
		}
	default:
		t.Fatal("projector did not reach the model")
	}

	card := emittedCard(t, result).Card
	if card.Layout != "hero" || card.Primary != "17°C" || card.Secondary != "Rain after lunch" {
		t.Errorf("card = %+v", card)
	}
}

func TestInterruptedResponseIsIgnored(t *testing.T) {
	projector := NewProjector(failingModel(t), 80)

	result, err := projectTurn(projector, completedEvent(), completedTurn{
		Transcript:  "new question",
		Response:    "partial answer",
		Interrupted: true,
	})
	if err != nil {
		t.Fatalf("projectTurn: %v", err)
	}
	if result != nil {
		t.Fatalf("interrupted response produced %+v", result)
	}
}

func TestModelFailureIsReportedNotEmitted(t *testing.T) {
	model := &stubModel{err: fmt.Errorf("provider unavailable")}
	projector := NewProjector(model.complete, 1)

	if _, err := projectTurn(projector, completedEvent(), completedTurn{
		Transcript: "q",
		Response:   strings.Repeat("x", 40),
	}); err == nil {
		t.Fatal("a failed projection reported success")
	}
}

func TestDisplayCardEventSerialization(t *testing.T) {
	event := cardEvent{
		Type:      cardType,
		Version:   cardVersion,
		SessionID: "session-123",
		TurnID:    "turn_123",
		TurnSeq:   123,
		Card: Card{
			Layout:    "hero",
			Title:     "Auckland · Tomorrow",
			Primary:   "17°C",
			Secondary: "Rain after lunch",
			Detail:    "Mostly cloudy · light winds",
		},
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"type", "version", "session_id", "turn_id", "turn_seq", "card"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("serialized event missing %q: %s", key, data)
		}
	}
	card := decoded["card"].(map[string]any)
	if card["layout"] != "hero" || card["primary"] != "17°C" {
		t.Fatalf("card = %+v", card)
	}
}

func TestSanitizeCardEnforcesV1Limits(t *testing.T) {
	card, err := SanitizeCard(Card{
		Layout: "list",
		Title:  strings.Repeat("T", 40),
		Items:  []string{strings.Repeat("a", 50), strings.Repeat("b", 20), "third", "fourth", "fifth", "sixth"},
	})
	if err != nil {
		t.Fatalf("SanitizeCard: %v", err)
	}
	if got := utf8.RuneCountInString(card.Title); got != MaxTitle {
		t.Fatalf("title runes = %d, want %d", got, MaxTitle)
	}
	if len(card.Items) != MaxListItems {
		t.Fatalf("items = %d, want %d", len(card.Items), MaxListItems)
	}
	if got := utf8.RuneCountInString(card.Items[0]); got != MaxListItem {
		t.Fatalf("first item runes = %d, want %d", got, MaxListItem)
	}
}

func TestModelCardValidation(t *testing.T) {
	valid := []Card{
		{Layout: "hero", Title: "Everest", Primary: "8,848.86 m", Secondary: "Highest above sea level", Detail: "Surveyed value"},
		{Layout: "text", Title: "Sky", Body: "Air scatters blue light."},
		{Layout: "list", Title: "Shopping", Items: []string{"Milk", "Coffee"}},
		{Layout: "status", Title: "Reminder created", Primary: "Call David", Secondary: "Tomorrow"},
	}
	for _, card := range valid {
		if _, err := SanitizeCard(card); err != nil {
			t.Fatalf("SanitizeCard(%s): %v", card.Layout, err)
		}
	}
	invalid := []Card{
		{Layout: "unknown", Title: "x", Primary: "y"},
		{Layout: "hero", Title: "x"},
		{Layout: "text", Title: "x"},
		{Layout: "list", Title: "x"},
		{Layout: "hero", Title: "x", Primary: "y", Body: "not allowed"},
		{Layout: "text", Title: "x", Body: "<script>y</script>"},
	}
	for _, card := range invalid {
		if _, err := SanitizeCard(card); err == nil {
			t.Fatalf("SanitizeCard accepted invalid card %+v", card)
		}
	}
}

func TestMalformedProjectorOutputIsRejected(t *testing.T) {
	outputs := []string{
		`not json`,
		`{"layout":"hero","title":"x","primary":"y"} trailing`,
		"```json\n{\"layout\":\"hero\",\"title\":\"x\",\"primary\":\"y\"}\n```",
		`{"layout":"hero","title":"x","primary":"y","extra":true}`,
	}
	for _, output := range outputs {
		if _, err := DecodeModelCard([]byte(output)); err == nil {
			t.Fatalf("DecodeModelCard accepted %q", output)
		}
	}
}

func TestModelFieldMistakesAreNormalizedBeforeValidation(t *testing.T) {
	tests := []struct {
		name string
		json string
		want Card
	}{
		{
			name: "text with irrelevant primary and items",
			json: `{"layout":"text","title":"Sky","primary":"blue","body":"Air scatters blue light.","items":["extra"]}`,
			want: Card{Layout: "text", Title: "Sky", Body: "Air scatters blue light."},
		},
		{
			name: "text content supplied as primary",
			json: `{"layout":"text","title":"Sky","primary":"Air scatters blue light."}`,
			want: Card{Layout: "text", Title: "Sky", Body: "Air scatters blue light."},
		},
		{
			name: "hero content supplied as body",
			json: `{"layout":"hero","title":"Everest","body":"8,848.86 m"}`,
			want: Card{Layout: "hero", Title: "Everest", Primary: "8,848.86 m"},
		},
		{
			name: "list item supplied as primary",
			json: `{"layout":"list","title":"Shopping","primary":"Milk"}`,
			want: Card{Layout: "list", Title: "Shopping", Items: []string{"Milk"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeModelCard([]byte(tc.json))
			if err != nil {
				t.Fatalf("DecodeModelCard: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("card = %+v, want %+v", got, tc.want)
			}
		})
	}
}
