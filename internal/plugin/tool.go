package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

// externalTool is one of the tools a subprocess plugin advertises. Several of
// them share a single process, which is what makes a family like the gesture
// set cost one plugin rather than fourteen.
type externalTool struct {
	host *ExternalPlugin
	spec ToolSpec
}

func (t *externalTool) Name() string                { return t.spec.Name }
func (t *externalTool) Internal() bool              { return t.spec.Internal }
func (t *externalTool) Description() string         { return t.spec.Description }
func (t *externalTool) Parameters() json.RawMessage { return t.spec.Parameters() }
func (t *externalTool) ConfirmationRequired() bool  { return t.spec.ConfirmationRequired }
func (t *externalTool) ThinkingSound() bool         { return t.spec.ThinkingSound }

func (t *externalTool) Execute(params json.RawMessage) (string, error) {
	return t.ExecuteInSession(context.Background(), "", params)
}

// ExecuteInSession hands the plugin the conversation the call belongs to, so a
// plugin holding per-conversation state — an isolated worktree, a cart, a
// draft — can keep two callers apart without the server knowing what it keeps.
func (t *externalTool) ExecuteInSession(ctx context.Context, sessionID string, params json.RawMessage) (string, error) {
	params, err := t.capture(ctx, sessionID, params)
	if err != nil {
		// A capture that fails is something to tell the user about, not a
		// broken turn: the client may simply have no camera pointed at
		// anything. The model reads this back and can ask them to try again.
		return err.Error(), nil
	}
	return t.host.Execute(ctx, t.spec.Name, sessionID, params)
}

// capture fulfils the tool's declared requirements and merges what came back
// into its arguments, so the plugin receives them as ordinary parameters.
func (t *externalTool) capture(ctx context.Context, sessionID string, params json.RawMessage) (json.RawMessage, error) {
	if len(t.spec.Requires) == 0 {
		return params, nil
	}
	if t.host.callbacks.Capture == nil {
		return nil, fmt.Errorf("%s needs %v, which this server cannot supply", t.spec.Name, t.spec.Requires)
	}

	fields := map[string]any{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &fields); err != nil {
			fields = map[string]any{}
		}
	}

	for _, capability := range t.spec.Requires {
		captured, err := t.host.callbacks.Capture(ctx, sessionID, capability)
		if err != nil {
			return nil, fmt.Errorf("Could not get %s: %v. Ask the user to try again.",
				strings.ReplaceAll(capability, "_", " "), err)
		}
		var extra map[string]any
		if err := json.Unmarshal(captured, &extra); err != nil {
			return nil, fmt.Errorf("Could not read the %s. Ask the user to try again.",
				strings.ReplaceAll(capability, "_", " "))
		}
		for key, value := range extra {
			fields[key] = value
		}
	}

	merged, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("Could not prepare the request. Ask the user to try again.")
	}
	return merged, nil
}

// ConfirmationPrompt is what the agent reads out before a gated tool runs.
//
// A manifest template covers most cases and costs nothing. When there is none,
// the plugin is asked directly, because some prompts are not substitution — one
// that reads an instruction back as part of a sentence has to reword it. A
// plugin that cannot answer falls back to the generic prompt rather than
// holding up the question.
func (t *externalTool) ConfirmationPrompt(params json.RawMessage) string {
	if t.spec.ConfirmationPrompt != "" {
		return renderPrompt(t.spec.ConfirmationPrompt, params)
	}
	if !t.spec.ConfirmationRequired {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), confirmTimeout)
	defer cancel()

	raw, err := t.host.call(ctx, JSONRPCRequest{
		Method: "confirm",
		Tool:   t.spec.Name,
		Params: params,
	})
	if err != nil {
		log.Printf("[plugin:%s] %s could not describe its confirmation: %v", t.host.manifest.Name, t.spec.Name, err)
		return ""
	}
	return resultText(raw)
}

// confirmTimeout bounds the prompt round trip. The user is waiting to be asked
// a question, so a plugin that dawdles gets the generic prompt instead.
const confirmTimeout = 5 * time.Second

func (t *DispatchTool) Internal() bool { return t.spec.Internal }

func (t *DispatchTool) ConfirmationPrompt(params json.RawMessage) string {
	return renderPrompt(t.spec.ConfirmationPrompt, params)
}

// renderPrompt fills {field} placeholders from the call's own arguments.
//
// A spoken confirmation has to name what it is about — "open a pull request on
// acme/api", not "run github.create_pull_request" — and the thing it names is
// almost always an argument. Substitution keeps that in the manifest instead of
// requiring a round trip to the plugin before the question can be asked.
func renderPrompt(template string, params json.RawMessage) string {
	if template == "" || !strings.Contains(template, "{") {
		return template
	}

	var fields map[string]any
	if err := json.Unmarshal(params, &fields); err != nil {
		return template
	}

	return placeholder.ReplaceAllStringFunc(template, func(match string) string {
		value, ok := fields[strings.Trim(match, "{}")]
		if !ok || value == nil {
			// Leave an unresolved placeholder out rather than reading braces
			// aloud to a user.
			return ""
		}
		return fmt.Sprint(value)
	})
}

var placeholder = regexp.MustCompile(`\{[A-Za-z0-9_]+\}`)
