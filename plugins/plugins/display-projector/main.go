// Command display-projector turns each completed voice turn into a small
// screen card and pushes it at the caller's client.
//
// It is an event-only plugin: it declares no tools, so the model can neither
// see it nor call it, and it cannot change what the agent says. It observes
// finished turns and emits a display.card packet alongside them.
package main

import (
	"log"

	streamcore "github.com/streamcoreai/plugin-sdk/go"
)

// cardTopic is what the client listens on; cardType is what it matches inside
// the packet. Neither may change without the clients changing with them.
const (
	cardTopic = "display.card"
	cardType  = "display.card"
)

// settings is this plugin's table from the server's config.toml, under
// [plugins.config."display-projector"].
type settings struct {
	FastPathMaxChars int `json:"fast_path_max_chars"`
}

// completedTurn is the payload of assistant.response.completed.
type completedTurn struct {
	Transcript  string `json:"transcript"`
	Response    string `json:"response"`
	Interrupted bool   `json:"interrupted"`
}

// cardEvent is what the client receives inside the packet.
type cardEvent struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	TurnSeq   uint64 `json:"turn_seq"`
	Card      Card   `json:"card"`
}

func main() {
	plugin := streamcore.New()
	var config settings

	plugin.OnInitialize(func(init streamcore.Init) ([]streamcore.Tool, error) {
		// No tools: this plugin observes, and declaring none keeps it
		// invisible to the model.
		return nil, init.Bind(&config)
	})

	plugin.OnEvent(func(event streamcore.Event) (any, error) {
		var turn completedTurn
		if err := event.Bind(&turn); err != nil {
			return nil, err
		}
		// The completion belongs to the conversation it describes, so the
		// session comes from the event rather than from startup.
		projector := NewProjector(func(system, prompt string) (string, error) {
			return plugin.Complete(event.SessionID, prompt, system)
		}, config.FastPathMaxChars)

		return projectTurn(projector, event, turn)
	})

	if err := plugin.Run(); err != nil {
		log.Fatalf("display-projector: %v", err)
	}
}

// projectTurn is the event handler's body, kept apart from the wiring so it can
// be exercised without a running server.
func projectTurn(projector *Projector, event streamcore.Event, turn completedTurn) (any, error) {
	// An interrupted turn never settled into an answer, so there is nothing
	// worth leaving on a screen.
	if turn.Interrupted {
		return nil, nil
	}

	card, err := projector.Project(turn.Transcript, turn.Response)
	if err != nil {
		// A failed projection is a missing card, not a broken call. The server
		// logs it and the conversation carries on.
		return nil, err
	}

	return streamcore.Result{Emit: []streamcore.Emission{{
		Topic: cardTopic,
		Payload: cardEvent{
			Type:      cardType,
			Version:   cardVersion,
			SessionID: event.SessionID,
			TurnID:    event.TurnID,
			TurnSeq:   event.TurnSeq,
			Card:      card,
		},
	}}}, nil
}
