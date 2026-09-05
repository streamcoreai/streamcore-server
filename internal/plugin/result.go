package plugin

import (
	"encoding/json"
	"fmt"
)

// Result is what a tool call produced: something for the model to say, and any
// number of packets to push down the session's data channel.
//
// A tool returns it encoded as JSON from Execute. Plugins that predate the
// envelope return a bare string, which ParseResult reports as Speak with no
// emissions, so the whole existing plugin corpus keeps working untouched.
type Result struct {
	Speak string     `json:"speak,omitempty"`
	Emit  []Emission `json:"emit,omitempty"`
}

// Emission is one topic-addressed packet bound for the client. The wire framing
// — the data packet envelope and its base64 payload — belongs to the pipeline;
// this only carries what to send.
type Emission struct {
	Topic   string          `json:"topic"`
	Payload json.RawMessage `json:"payload,omitempty"`

	// SessionID targets a specific conversation. Empty means the session that
	// made the call, which is what every tool-triggered emission wants. Only an
	// unsolicited emission — one a plugin pushes without being asked — has to
	// name its own.
	SessionID string `json:"session_id,omitempty"`
}

// ParseResult interprets whatever a tool returned.
//
// A JSON object carrying "speak" or "emit" is an envelope. Anything else is
// spoken text verbatim, including a JSON object that happens to lack both keys
// and a quoted JSON string — the SDKs stringify results, so "hello" arriving as
// a JSON string must still read as hello and not as a parse failure.
func ParseResult(raw string) Result {
	envelope, ok := decodeEnvelope(raw)
	if ok {
		return envelope
	}
	return Result{Speak: raw}
}

func decodeEnvelope(raw string) (Result, bool) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return Result{}, false
	}
	_, hasSpeak := probe["speak"]
	_, hasEmit := probe["emit"]
	if !hasSpeak && !hasEmit {
		return Result{}, false
	}

	var result Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return Result{}, false
	}
	for _, emission := range result.Emit {
		if emission.Topic == "" {
			return Result{}, false
		}
	}
	return result, true
}

// Encode renders a Result as the JSON a tool returns from Execute.
func (r Result) Encode() (string, error) {
	encoded, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("encode plugin result: %w", err)
	}
	return string(encoded), nil
}
