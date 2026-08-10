// Package tools holds native Go plugins compiled into the server.
//
// Car tools control a remote two-wheel desktop-car robot connected over
// WebRTC. The tools themselves only describe the surface — Execute is a
// no-op. The real work happens in the pipeline package, which intercepts
// any "car.*" tool call and writes a topic-addressed data-channel packet
// to the device. Same pattern as vision.analyze.
package tools

import (
	"encoding/json"
	"fmt"

	"github.com/streamcoreai/streamcore-server/internal/plugin"
)

// CarCommandTopic is the data-channel topic the firmware listens on for
// drivetrain commands. Keep in sync with the firmware's on_data handler.
const CarCommandTopic = "car.command"

// CarTool is a metadata-only Tool. Its Execute method exists to satisfy
// the plugin.Tool interface; the pipeline never reaches it for "car.*"
// names because it intercepts those calls and emits a data-channel
// packet directly.
type CarTool struct {
	name        string
	description string
	parameters  json.RawMessage
}

func (t *CarTool) Name() string                { return t.name }
func (t *CarTool) Description() string         { return t.description }
func (t *CarTool) Parameters() json.RawMessage { return t.parameters }
func (t *CarTool) ConfirmationRequired() bool  { return false }
func (t *CarTool) ThinkingSound() bool         { return false }

// Execute is never called in production — pipeline.go intercepts.
func (t *CarTool) Execute(params json.RawMessage) (string, error) {
	return "", fmt.Errorf("car.* tool calls must be intercepted by the pipeline")
}

// All returns the full set of drivetrain tools to register with the
// plugin manager. Wire them in `main.go` with `pluginMgr.RegisterNative`.
func All() []plugin.Tool {
	move := paramsSchema(`How long to drive in milliseconds (clamped to 100..10000).`, 1500)
	short := paramsSchema(`How long to drive in milliseconds (clamped to 100..10000).`, 800)
	fancy := paramsSchema(`Total length of the fancy-moves routine in milliseconds.`, 3000)

	return []plugin.Tool{
		&CarTool{"car.forward", "Drive the desktop-car robot straight forward. Use when the user asks the robot to go, move, advance, or come closer.", move},
		&CarTool{"car.backward", "Drive the desktop-car robot straight backward. Use when the user asks the robot to back up, reverse, or move away.", move},
		&CarTool{"car.turn_left", "Spin the desktop-car robot in place to its left (counter-clockwise). Use when the user asks to turn left or look left.", short},
		&CarTool{"car.turn_right", "Spin the desktop-car robot in place to its right (clockwise). Use when the user asks to turn right or look right.", short},
		&CarTool{"car.pivot_forward_left", "Curve forward and to the left by driving only the right wheel. Gentler than a turn-in-place.", short},
		&CarTool{"car.pivot_forward_right", "Curve forward and to the right by driving only the left wheel. Gentler than a turn-in-place.", short},
		&CarTool{"car.pivot_back_left", "Curve backward and to the left by driving only the right wheel in reverse.", short},
		&CarTool{"car.pivot_back_right", "Curve backward and to the right by driving only the left wheel in reverse.", short},
		&CarTool{"car.stop", "Immediately stop both wheels. Use when the user asks the robot to stop, halt, freeze, or wait.", emptySchema()},
		&CarTool{"car.fancy", "Play a short choreographed sequence of fancy moves — alternating pivots and wiggles. Use when the user asks the robot to do fancy moves, show off, dance, party, celebrate, do tricks, or otherwise put on a little show.", fancy},
		&CarTool{"car.shake", "Quick left-right wiggle in place. Use when the user asks the robot to shake, wiggle, or nod no.", short},
	}
}

func paramsSchema(durationDesc string, defaultDuration int) json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"duration_ms": map[string]any{
				"type":        "integer",
				"description": durationDesc,
				"default":     defaultDuration,
				"minimum":     100,
				"maximum":     10000,
			},
			"speed_percent": map[string]any{
				"type":        "integer",
				"description": "Motor duty cycle, 0..100. Defaults to 80 if omitted.",
				"default":     80,
				"minimum":     0,
				"maximum":     100,
			},
		},
	}
	b, _ := json.Marshal(schema)
	return b
}

func emptySchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
