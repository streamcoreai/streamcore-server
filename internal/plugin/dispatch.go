package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DispatchTool is a tool that only turns its arguments into one packet for the
// client. No process runs and nothing is compiled in: the manifest says which
// topic to address and which constants identify the action, and the schema
// supplies the rest.
//
// Locomotion and gestures are the whole reason it exists. They have no logic to
// run, so a subprocess would add a round trip to every call and buy nothing.
type DispatchTool struct {
	spec ToolSpec
}

// NewDispatchTool builds a tool from a spec that carries a dispatch block.
func NewDispatchTool(spec ToolSpec) (*DispatchTool, error) {
	if spec.Dispatch == nil {
		return nil, fmt.Errorf("tool %q has no dispatch block", spec.Name)
	}
	if strings.TrimSpace(spec.Dispatch.Topic) == "" {
		return nil, fmt.Errorf("tool %q dispatches without a topic", spec.Name)
	}
	return &DispatchTool{spec: spec}, nil
}

// Partial reports which half-spoken phrases may fire this tool, or nil if the
// manifest did not opt in. It satisfies PartialTool.
func (t *DispatchTool) Partial() *PartialSpec { return t.spec.OnPartial }

func (t *DispatchTool) Name() string                { return t.spec.Name }
func (t *DispatchTool) Description() string         { return t.spec.Description }
func (t *DispatchTool) Parameters() json.RawMessage { return t.spec.Parameters() }
func (t *DispatchTool) ConfirmationRequired() bool  { return t.spec.ConfirmationRequired }
func (t *DispatchTool) ThinkingSound() bool         { return t.spec.ThinkingSound }

// Execute builds the packet and the line the model reads back.
func (t *DispatchTool) Execute(params json.RawMessage) (string, error) {
	fields, err := decodeArgs(params)
	if err != nil {
		return "", fmt.Errorf("tool %s: %w", t.spec.Name, err)
	}

	applySchema(fields, t.spec.Parameters())

	speak := t.speak(fields)

	if t.spec.Dispatch.OmitZero {
		for key, value := range fields {
			if isZero(value) {
				delete(fields, key)
			}
		}
	}

	// Constants win: the action a tool stands for is not the model's to change.
	for key, value := range t.spec.Dispatch.Payload {
		fields[key] = value
	}

	payload, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("tool %s: encode payload: %w", t.spec.Name, err)
	}

	return Result{
		Speak: speak,
		Emit:  []Emission{{Topic: t.spec.Dispatch.Topic, Payload: payload}},
	}.Encode()
}

// speak picks the acknowledgement, preferring the first conditional rule whose
// arguments all match.
func (t *DispatchTool) speak(fields map[string]any) string {
	for _, rule := range t.spec.Dispatch.SpeakWhen {
		if matchesAll(fields, rule.When) {
			return rule.Speak
		}
	}
	return t.spec.Dispatch.Speak
}

func matchesAll(fields map[string]any, when map[string]any) bool {
	if len(when) == 0 {
		return false
	}
	for key, want := range when {
		got, ok := fields[key]
		if !ok || !valueEquals(got, want) {
			return false
		}
	}
	return true
}

// valueEquals compares a decoded argument against a manifest literal. YAML and
// JSON disagree about number types often enough that comparing their rendered
// forms is more reliable than reflecting over them.
func valueEquals(got, want any) bool {
	if gotNum, ok := toFloat(got); ok {
		if wantNum, ok := toFloat(want); ok {
			return gotNum == wantNum
		}
		return false
	}
	return fmt.Sprint(got) == fmt.Sprint(want)
}

// decodeArgs reads a tool's arguments, keeping numbers in their original
// notation so an untouched value is forwarded exactly as the model wrote it.
func decodeArgs(params json.RawMessage) (map[string]any, error) {
	fields := map[string]any{}
	if len(params) == 0 {
		return fields, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(params)))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return nil, fmt.Errorf("decode arguments: %w", err)
	}
	return fields, nil
}

// applySchema drops arguments the tool never advertised and holds the rest
// inside the bounds it did.
//
// Both halves matter. A tool that does not offer an argument must not forward
// it: a turn-in-place accepting "continuous" would let the model set the flag,
// be told it worked, and leave the robot doing a normal timed move instead.
// And telling the model one range while forwarding another is a lie it has no
// way to notice, which the client then acts on.
//
// Absent arguments stay absent. Every client applies its own default for a
// field the model omitted, so filling one in here would silently override it.
func applySchema(fields map[string]any, schema json.RawMessage) {
	var parsed struct {
		AdditionalProperties *bool `json:"additionalProperties"`
		Properties           map[string]struct {
			Type    string       `json:"type"`
			Minimum *json.Number `json:"minimum"`
			Maximum *json.Number `json:"maximum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return
	}

	// A schema may opt back into passthrough, for a dispatch tool whose packet
	// is genuinely open-ended.
	if parsed.AdditionalProperties == nil || !*parsed.AdditionalProperties {
		for key := range fields {
			if _, declared := parsed.Properties[key]; !declared {
				delete(fields, key)
			}
		}
	}

	for key, property := range parsed.Properties {
		value, ok := fields[key]
		if !ok {
			continue
		}
		number, ok := toFloat(value)
		if !ok {
			continue
		}

		clamped := number
		if property.Minimum != nil {
			if min, err := property.Minimum.Float64(); err == nil && clamped < min {
				clamped = min
			}
		}
		if property.Maximum != nil {
			if max, err := property.Maximum.Float64(); err == nil && clamped > max {
				clamped = max
			}
		}
		if clamped == number {
			continue
		}
		if property.Type == "integer" {
			fields[key] = int64(clamped)
		} else {
			fields[key] = clamped
		}
	}
}

func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	}
	return 0, false
}

// isZero reports the values Go's encoding/json would have dropped under
// omitempty, which is the behaviour OmitZero reproduces.
func isZero(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case bool:
		return !typed
	case string:
		return typed == ""
	}
	if number, ok := toFloat(value); ok {
		return number == 0
	}
	return false
}
