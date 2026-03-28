package plugin

import "encoding/json"

// Manifest describes a plugin loaded from plugin.yaml.
type Manifest struct {
	Name                 string      `yaml:"name"`
	Description          string      `yaml:"description"`
	Version              int         `yaml:"version"`
	Language             string      `yaml:"language"`
	Entrypoint           string      `yaml:"entrypoint"`
	ParametersRaw        interface{} `yaml:"parameters"`
	ConfirmationRequired bool        `yaml:"confirmation_required"`
}

// Parameters returns the parameters as JSON raw message for OpenAI tool compatibility.
func (m *Manifest) Parameters() json.RawMessage {
	if m.ParametersRaw == nil {
		return []byte("{}")
	}

	// Convert the interface{} back to JSON bytes
	jsonBytes, err := json.Marshal(m.ParametersRaw)
	if err != nil {
		// Fallback to empty object if conversion fails
		return []byte("{}")
	}
	return jsonBytes
}

// Tool is the interface for anything callable by the LLM via function calling.
// Both external (subprocess) plugins and native Go plugins implement this.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Execute(params json.RawMessage) (string, error)
	ConfirmationRequired() bool
}

// Skill is a parsed markdown skill file that augments the system prompt.
type Skill struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Version     int      `yaml:"version"`
	Triggers    []string `yaml:"triggers"`
	Plugins     []string `yaml:"plugins"`
	Content     string   // The markdown content after frontmatter
}

// JSONRPCRequest is a JSON-RPC 2.0 request sent to plugin subprocesses.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      int             `json:"id"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response from plugin subprocesses.
type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	Result  string        `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
	ID      int           `json:"id"`
}

// JSONRPCError represents a JSON-RPC error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
