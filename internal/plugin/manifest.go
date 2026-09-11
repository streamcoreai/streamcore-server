package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
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
	Requires             []string      `yaml:"requires"`
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

	// Requires names capabilities the server must supply before the call —
	// things a plugin cannot obtain for itself because they come from the
	// client on the other end of the conversation. The captured fields are
	// merged into the arguments the tool receives.
	//
	// A camera frame is the motivating one: the plugin analysing it has no way
	// to ask the device for a picture, and hardcoding one tool's name in the
	// pipeline to do it for them is what this replaces.
	Requires []string `yaml:"requires"`

	// Internal keeps a tool out of the model's view while leaving it callable
	// by other plugins. It is for the joins between plugins — one asking
	// another for a credential or a worktree — which the model has no business
	// invoking and would only be confused by.
	Internal bool `yaml:"internal"`

	// ConfirmationPrompt is what the agent says out loud to ask permission.
	// Without it a gated tool falls back to naming itself, which is accurate
	// but rarely what you want read to a user.
	ConfirmationPrompt string `yaml:"confirmation_prompt"`

	// Dispatch makes this tool pure data: the server maps arguments to a
	// data-channel packet itself and no process is involved.
	Dispatch *DispatchSpec `yaml:"dispatch"`

	// OnPartial lets a half-spoken sentence fire this tool, before the
	// transcript is final and long before the model has read it.
	OnPartial *PartialSpec `yaml:"on_partial"`
}

// PartialSpec makes a tool reachable from a partial transcript.
//
// A brake is the case that justifies it. Routing "stop" through endpointing,
// then a model turn, then a tool call, spends the better part of a second
// deciding to do the one thing nobody wants deliberated. A manifest that says
// which words mean stop skips all of it, and the server still learns nothing
// about what the tool does — the packet is the manifest's, unchanged.
//
// Only dispatch tools qualify. Firing a subprocess from an unfinished sentence
// means running it repeatedly as the sentence revises, which is a different and
// much worse idea.
type PartialSpec struct {
	// Match fires when the phrase appears as a whole word run anywhere in the
	// partial, so "no, stop" counts. Substrings do not: "stop" must not fire
	// on "stopwatch".
	Match []string `yaml:"match"`

	// StartsWith fires when the utterance opens with the phrase.
	//
	// It commits on sight, and that is not a bug to be fixed: a partial
	// arrives as it is spoken, so "hold on to it" passes through the state
	// "hold on" on its way and there is no revision to wait for that would not
	// also cost the latency this exists to avoid.
	//
	// So only list phrases where every continuation still means the same
	// thing. "wait" survives "wait a second". "hold on" does not survive
	// "hold on to it", and belongs in the model's hands instead.
	StartsWith []string `yaml:"starts_with"`

	// OncePerTurn stops one utterance firing repeatedly as STT revises it.
	// Partials arrive several times a second and each revision still contains
	// the word that matched.
	//
	// A capture does not need it and is usually worse for having it: each
	// settled value already fires once, and one sentence can legitimately
	// carry two — "grab the red one, no the blue one" is a grab and then a
	// correction, and suppressing the second leaves the arm on the wrong
	// object. Set it only where a second firing in the same breath is always
	// wrong.
	OncePerTurn bool `yaml:"once_per_turn"`

	// Capture fills an argument from the words after a phrase.
	Capture *CaptureSpec `yaml:"capture"`

	// SettleMs is how long a capture must stop changing before the tool fires.
	//
	// Without it a capture fires on the first revision that has any text at
	// all: "grab the red mug" is spoken as "grab the", "grab the red", "grab
	// the red mug", and acting on the first would send the label "red" — or
	// worse, an empty one. Waiting for quiet costs a fraction of what waiting
	// for endpointing and a model turn costs.
	//
	// Only meaningful with Capture. Defaults to DefaultSettleMs.
	SettleMs int `yaml:"settle_ms"`
}

// CaptureSpec fills a tool argument with whatever follows an anchor phrase.
//
// The anchors are not prefixes. One sentence can carry two instructions —
// "grab the red one, no the blue one" — so the last anchor in the utterance is
// the one that counts, and what follows it is the argument. Firing again needs
// a new anchor to appear, which is what keeps "grab the red mug please" one
// instruction rather than two with different labels.
type CaptureSpec struct {
	// Arg is the argument name to fill.
	Arg string `yaml:"arg"`

	// After lists the phrases the argument follows. Matched at word
	// boundaries anywhere in the utterance; the longest match at a position
	// wins, so listing both "grab" and "grab the" captures "red mug" rather
	// than "the red mug".
	After []string `yaml:"after"`
}

// DefaultSettleMs is the quiet period a capture waits out before firing. Long
// enough to let a two-word object name finish arriving, short enough to stay
// well inside the endpointing silence it is beating.
const DefaultSettleMs = 250

// Settle returns the configured quiet period, or the default.
func (ps *PartialSpec) Settle() time.Duration {
	if ps == nil || ps.SettleMs <= 0 {
		return DefaultSettleMs * time.Millisecond
	}
	return time.Duration(ps.SettleMs) * time.Millisecond
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
	if len(m.Tools) == 0 && m.Name != "" && m.describesTool() {
		m.Tools = []ToolSpec{{
			Name:                 m.Name,
			Description:          m.Description,
			ParametersRaw:        m.ParametersRaw,
			ConfirmationRequired: m.ConfirmationRequired,
			ThinkingSound:        m.ThinkingSound,
			Requires:             m.Requires,
			Dispatch:             m.Dispatch,
		}}
	}
	if len(m.Exec) == 0 && m.Entrypoint != "" {
		m.Exec = expandLanguage(m.Language, m.Entrypoint)
	}
}

// describesTool reports whether the top-level fields are describing a tool
// rather than just naming the plugin. A schema, a dispatch block, or a declared
// requirement all say "this is a tool"; a name on its own does not.
func (m *Manifest) describesTool() bool {
	return m.ParametersRaw != nil || m.Dispatch != nil || len(m.Requires) > 0
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
		if err := tool.OnPartial.validate(tool.Name, tool.Dispatch); err != nil {
			return err
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

func (ps *PartialSpec) validate(toolName string, dispatch *DispatchSpec) error {
	if ps == nil {
		return nil
	}
	if dispatch == nil {
		return fmt.Errorf("tool %q has on_partial without dispatch: only a packet may be sent from an unfinished sentence", toolName)
	}
	if len(ps.Match) == 0 && len(ps.StartsWith) == 0 && ps.Capture == nil {
		return fmt.Errorf("tool %q has on_partial with nothing to match", toolName)
	}
	if ps.Capture != nil {
		if strings.TrimSpace(ps.Capture.Arg) == "" {
			return fmt.Errorf("tool %q has a capture with no arg", toolName)
		}
		if len(ps.Capture.After) == 0 {
			return fmt.Errorf("tool %q captures into %q with no after phrases: there is nothing to anchor the argument to", toolName, ps.Capture.Arg)
		}
		for _, phrase := range ps.Capture.After {
			if strings.TrimSpace(phrase) == "" {
				return fmt.Errorf("tool %q has an empty capture anchor", toolName)
			}
		}
	}
	if ps.Capture == nil && ps.SettleMs > 0 {
		return fmt.Errorf("tool %q sets settle_ms without capture: there is nothing to wait for", toolName)
	}
	for _, phrase := range append(append([]string{}, ps.Match...), ps.StartsWith...) {
		if strings.TrimSpace(phrase) == "" {
			return fmt.Errorf("tool %q has an empty on_partial phrase", toolName)
		}
	}
	return nil
}

// Phrases returns every phrase this spec listens for, normalised the same way
// the matcher normalises a transcript. Empty when the spec is nil.
func (ps *PartialSpec) Phrases() (match, startsWith, after []string) {
	if ps == nil {
		return nil, nil, nil
	}
	for _, phrase := range ps.Match {
		match = append(match, NormalizePartial(phrase))
	}
	for _, phrase := range ps.StartsWith {
		startsWith = append(startsWith, NormalizePartial(phrase))
	}
	if ps.Capture != nil {
		for _, phrase := range ps.Capture.After {
			after = append(after, NormalizePartial(phrase))
		}
	}
	return match, startsWith, after
}

// Arg is the argument a capture fills, or "" when the spec does not capture.
func (ps *PartialSpec) Arg() string {
	if ps == nil || ps.Capture == nil {
		return ""
	}
	return ps.Capture.Arg
}

// NormalizePartial reduces a transcript or a manifest phrase to the form the
// matcher compares: lower case, punctuation gone, runs of whitespace collapsed.
//
// Punctuation goes because STT inserts it mid-utterance and moves it as the
// sentence revises, so "no, stop" and "no stop" are the same instruction
// arriving twice.
func NormalizePartial(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	space := true // leading whitespace is dropped
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'':
			b.WriteRune(r)
			space = false
		case !space:
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}
