package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Manifest describes a plugin loaded from plugin.yaml.
//
// It carries two shapes. The original one names a single tool at the top level;
// the newer `tools:` list declares several from one folder, which is what a
// family like the gesture set wants rather than fourteen directories. Normalize
// folds the first into the second so everything downstream sees only a list.
type Manifest struct {
	// Single-tool form.
	Name                 string        `yaml:"name"`
	Description          string        `yaml:"description"`
	ParametersRaw        interface{}   `yaml:"parameters"`
	ConfirmationRequired bool          `yaml:"confirmation_required"`
	ThinkingSound        bool          `yaml:"thinking_sound"`
	Dispatch             *DispatchSpec `yaml:"dispatch"`

	// Multi-tool form.
	Tools []ToolSpec `yaml:"tools"`

	Version int `yaml:"version"`

	// Enabled turns a plugin off without deleting its folder. Absent means on.
	// An operator overrides it per deployment with `enabled` in the plugin's
	// own config table, which is how a plugin that ships in the repo can stay
	// dormant until someone asks for it.
	Enabled *bool `yaml:"enabled"`

	// Language and Entrypoint are the original way to say how to start a
	// plugin. They expand to Exec, which is the general form: an argv the
	// server runs without caring what language is on the other end.
	Language   string   `yaml:"language"`
	Entrypoint string   `yaml:"entrypoint"`
	Exec       []string `yaml:"exec"`

	// Build runs once at load, before Exec, when the plugin needs compiling or
	// its dependencies installing. Failure is fatal to this plugin alone.
	Build []string `yaml:"build"`

	// TimeoutMs bounds one execute call. Zero means DefaultExecuteTimeout.
	TimeoutMs int `yaml:"timeout_ms"`

	// Events subscribes the plugin to server lifecycle events, the same set a
	// Go EventHandler can observe.
	Events []string `yaml:"events"`

	// config is the plugin's own table from config.toml, injected by the
	// loader and forwarded at initialize. It is not part of the YAML: a
	// plugin's secrets belong in the operator's config, not next to its code.
	config json.RawMessage
}

// ToolSpec is one callable tool. A manifest's single-tool fields collapse into
// one of these.
type ToolSpec struct {
	Name                 string      `yaml:"name"`
	Description          string      `yaml:"description"`
	ParametersRaw        interface{} `yaml:"parameters"`
	ConfirmationRequired bool        `yaml:"confirmation_required"`
	ThinkingSound        bool        `yaml:"thinking_sound"`

	// ConfirmationPrompt is what the agent says out loud to ask permission.
	// Without it a gated tool falls back to naming itself, which is accurate
	// but rarely what you want read to a user.
	ConfirmationPrompt string `yaml:"confirmation_prompt"`

	// Dispatch makes this tool pure data: the server maps arguments to a
	// data-channel packet itself and no process is involved.
	Dispatch *DispatchSpec `yaml:"dispatch"`
}

// DispatchSpec declares a tool that only ever turns its arguments into one
// topic-addressed packet. Locomotion and gestures are the whole motivation:
// they have no logic to run, so making them subprocesses would buy nothing and
// cost a round trip on every call.
type DispatchSpec struct {
	Topic string `yaml:"topic"`

	// Payload is merged over the caller's arguments, so it holds the constants
	// that identify the action while the schema supplies the variables.
	Payload map[string]any `yaml:"payload"`

	Speak string `yaml:"speak"`

	// OmitZero drops arguments that came through as zero, false, or empty,
	// leaving the client to apply its own default for the field.
	//
	// It exists because the clients treat an absent field and an explicit zero
	// as different things — absent means "use your default", zero means zero —
	// and the tools this replaced dropped zeroes as a side effect of Go's
	// omitempty. A plugin that wants a zero forwarded simply leaves it unset.
	OmitZero bool `yaml:"omit_zero"`

	// SpeakWhen overrides Speak when an argument matches. The first rule that
	// matches wins. It exists because a couple of moves genuinely read
	// differently — a continuous "forward" has to tell the user how to stop it.
	SpeakWhen []SpeakRule `yaml:"speak_when"`
}

// SpeakRule is a conditional acknowledgement.
type SpeakRule struct {
	When  map[string]any `yaml:"when"`
	Speak string         `yaml:"speak"`
}

// Parameters returns a tool's JSON Schema for the model. An absent schema
// becomes an empty object rather than null, which some providers reject.
func (t ToolSpec) Parameters() json.RawMessage {
	if t.ParametersRaw == nil {
		return []byte(`{"type":"object","properties":{}}`)
	}
	encoded, err := json.Marshal(t.ParametersRaw)
	if err != nil {
		return []byte(`{"type":"object","properties":{}}`)
	}
	return encoded
}

// Normalize folds the single-tool form into Tools and expands Language and
// Entrypoint into Exec. Call it once after unmarshalling, before anything else
// reads the manifest.
func (m *Manifest) Normalize() {
	// The single-tool form is recognised by a manifest describing a tool at the
	// top level, not merely by having a name. Every plugin has a name; one that
	// carries neither a schema nor a dispatch block is not offering a tool
	// called after itself — it is an observer, or it will declare its tools at
	// initialize.
	if len(m.Tools) == 0 && m.Name != "" && (m.ParametersRaw != nil || m.Dispatch != nil) {
		m.Tools = []ToolSpec{{
			Name:                 m.Name,
			Description:          m.Description,
			ParametersRaw:        m.ParametersRaw,
			ConfirmationRequired: m.ConfirmationRequired,
			ThinkingSound:        m.ThinkingSound,
			Dispatch:             m.Dispatch,
		}}
	}
	if len(m.Exec) == 0 && m.Entrypoint != "" {
		m.Exec = expandLanguage(m.Language, m.Entrypoint)
	}
}

// expandLanguage turns the legacy language/entrypoint pair into an argv. An
// unknown language yields nothing, and Validate reports it as such.
func expandLanguage(language, entrypoint string) []string {
	switch language {
	case "python":
		return []string{"python3", entrypoint}
	case "typescript":
		return []string{"npx", "tsx", entrypoint}
	case "javascript":
		return []string{"node", entrypoint}
	default:
		return nil
	}
}

// overrides are the manifest fields an operator may set from config.toml. They
// are the ones that belong to a deployment rather than to the plugin: whether
// it runs at all, and how long it gets.
type overrides struct {
	Enabled   *bool `json:"enabled"`
	TimeoutMs *int  `json:"timeout_ms"`
}

func (m *Manifest) overrides() overrides {
	var settings overrides
	if len(m.config) > 0 {
		json.Unmarshal(m.config, &settings)
	}
	return settings
}

// IsEnabled reports whether this plugin should load. The config table wins over
// the manifest, so an operator can turn on something shipped off, or off
// something shipped on, without editing a file they do not own.
func (m *Manifest) IsEnabled() bool {
	if enabled := m.overrides().Enabled; enabled != nil {
		return *enabled
	}
	if m.Enabled != nil {
		return *m.Enabled
	}
	return true
}

// Timeout is how long one call gets, with the operator's setting taking
// precedence over the manifest's.
func (m *Manifest) Timeout() time.Duration {
	if override := m.overrides().TimeoutMs; override != nil && *override > 0 {
		return time.Duration(*override) * time.Millisecond
	}
	if m.TimeoutMs > 0 {
		return time.Duration(m.TimeoutMs) * time.Millisecond
	}
	return DefaultExecuteTimeout
}

// NeedsProcess reports whether this manifest starts a subprocess. A manifest
// whose tools all dispatch, and which subscribes to nothing, does not.
func (m *Manifest) NeedsProcess() bool {
	return len(m.Exec) > 0
}

// Validate rejects a manifest the loader cannot act on. The error names the
// plugin folder's problem in the terms the author wrote it in.
func (m *Manifest) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("manifest has no name")
	}
	// A process may declare its tools at initialize instead of here, which is
	// how a plugin whose surface depends on its configuration works. Only a
	// manifest with no process has to list everything up front.
	if len(m.Tools) == 0 && len(m.Events) == 0 && !m.NeedsProcess() {
		return fmt.Errorf("declares no tools and subscribes to no events")
	}

	seen := make(map[string]bool, len(m.Tools))
	for i, tool := range m.Tools {
		if tool.Name == "" {
			return fmt.Errorf("tools[%d] has no name", i)
		}
		if seen[tool.Name] {
			return fmt.Errorf("duplicate tool name %q", tool.Name)
		}
		seen[tool.Name] = true

		if tool.Dispatch != nil && strings.TrimSpace(tool.Dispatch.Topic) == "" {
			return fmt.Errorf("tool %q dispatches without a topic", tool.Name)
		}
	}

	if m.NeedsProcess() {
		return nil
	}

	// No process: every tool must be able to run without one, and a
	// subscription with nothing to deliver to is an author mistake worth
	// naming rather than silently ignoring.
	for _, tool := range m.Tools {
		if tool.Dispatch == nil {
			return fmt.Errorf("tool %q needs a process: set exec, or give it a dispatch block", tool.Name)
		}
	}
	if len(m.Events) > 0 {
		return fmt.Errorf("subscribes to events but has no exec to deliver them to")
	}
	if m.Language != "" && m.Entrypoint != "" {
		return fmt.Errorf("unsupported language %q: use exec instead", m.Language)
	}
	return nil
}
