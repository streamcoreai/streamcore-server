// Package codex embeds the official Codex App Server in StreamCore.
//
// StreamCore launches `codex app-server` as a child process and speaks its
// JSON-RPC protocol over stdio. Authentication is whatever the Codex harness
// already holds — the operator signs in once with "Sign in with ChatGPT" and
// Codex owns that credential state from then on. StreamCore never reads it,
// never stores it, and never falls back to an API key.
//
// The message shapes below were generated from the protocol schema of the
// pinned Codex version (`codex app-server generate-json-schema`), not from
// memory.
package codex

import "encoding/json"

// Protocol method names, from the App Server schema.
const (
	methodInitialize    = "initialize"
	methodInitialized   = "initialized"
	methodAccountRead   = "account/read"
	methodThreadStart   = "thread/start"
	methodThreadResume  = "thread/resume"
	methodTurnStart     = "turn/start"
	methodTurnInterrupt = "turn/interrupt"

	// Server notifications StreamCore reacts to. Everything else is ignored.
	notifyThreadStarted   = "thread/started"
	notifyTurnStarted     = "turn/started"
	notifyTurnCompleted   = "turn/completed"
	notifyTurnDiffUpdated = "turn/diff/updated"
	notifyItemStarted     = "item/started"
	notifyItemCompleted   = "item/completed"
	notifyError           = "error"

	// Server requests. Every approval is answered by policy; nothing here ever
	// reaches the voice model as a question.
	requestCommandApproval     = "item/commandExecution/requestApproval"
	requestFileChangeApproval  = "item/fileChange/requestApproval"
	requestPermissionsApproval = "item/permissions/requestApproval"
	requestExecCommandApproval = "execCommandApproval"
	requestApplyPatchApproval  = "applyPatchApproval"
	requestAuthTokenRefresh    = "account/chatgptAuthTokens/refresh"
)

// jsonrpcMessage is the union of everything that can arrive on stdout. A
// message with an id and a method is a server request; with an id and no
// method it is a response to one of ours; with no id it is a notification.
type jsonrpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      *json.Number    `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *jsonrpcError) Error() string { return e.Message }

type initializeParams struct {
	ClientInfo clientInfo `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type initializeResult struct {
	UserAgent      string `json:"userAgent"`
	CodexHome      string `json:"codexHome"`
	PlatformOS     string `json:"platformOs"`
	PlatformFamily string `json:"platformFamily"`
}

// accountResult is how StreamCore answers "is Codex signed in, and with what".
// The chatgpt variant is the only one this integration accepts.
type accountResult struct {
	Account *struct {
		Type     string `json:"type"`
		Email    string `json:"email"`
		PlanType string `json:"planType"`
	} `json:"account"`
	RequiresOpenAIAuth bool `json:"requiresOpenaiAuth"`
}

type threadStartParams struct {
	CWD                   string `json:"cwd"`
	Sandbox               string `json:"sandbox,omitempty"`
	ApprovalPolicy        string `json:"approvalPolicy,omitempty"`
	Model                 string `json:"model,omitempty"`
	ModelProvider         string `json:"modelProvider,omitempty"`
	DeveloperInstructions string `json:"developerInstructions,omitempty"`
}

type threadResumeParams struct {
	ThreadID       string `json:"threadId"`
	CWD            string `json:"cwd,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
	ApprovalPolicy string `json:"approvalPolicy,omitempty"`
	Model          string `json:"model,omitempty"`
	ModelProvider  string `json:"modelProvider,omitempty"`
}

type threadResult struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
	Model         string `json:"model"`
	ModelProvider string `json:"modelProvider"`
	CWD           string `json:"cwd"`
}

type turnStartParams struct {
	ThreadID       string          `json:"threadId"`
	Input          []turnInput     `json:"input"`
	CWD            string          `json:"cwd,omitempty"`
	SandboxPolicy  *sandboxPolicy  `json:"sandboxPolicy,omitempty"`
	ApprovalPolicy string          `json:"approvalPolicy,omitempty"`
	Model          string          `json:"model,omitempty"`
	Summary        string          `json:"summary,omitempty"`
	OutputSchema   json.RawMessage `json:"outputSchema,omitempty"`
}

type turnInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// sandboxPolicy mirrors the App Server union. Only the two variants StreamCore
// ever uses are expressible here: read-only for analysis, and workspace-write
// pinned to the task's own worktree for a fix.
type sandboxPolicy struct {
	Type          string   `json:"type"`
	NetworkAccess bool     `json:"networkAccess"`
	WritableRoots []string `json:"writableRoots,omitempty"`
}

func readOnlySandbox(network bool) *sandboxPolicy {
	return &sandboxPolicy{Type: "readOnly", NetworkAccess: network}
}

func workspaceWriteSandbox(root string, network bool) *sandboxPolicy {
	return &sandboxPolicy{Type: "workspaceWrite", NetworkAccess: network, WritableRoots: []string{root}}
}

type turnResult struct {
	Turn turnState `json:"turn"`
}

type turnState struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	DurationMs  int64      `json:"durationMs"`
	Error       *turnError `json:"error"`
	CompletedAt int64      `json:"completedAt"`
}

type turnError struct {
	Message        string          `json:"message"`
	CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
	Additional     string          `json:"additionalDetails"`
}

type turnCompletedParams struct {
	ThreadID string    `json:"threadId"`
	Turn     turnState `json:"turn"`
}

type turnDiffParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Diff     string `json:"diff"`
}

type errorNotification struct {
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	WillRetry bool   `json:"willRetry"`
	Error     struct {
		Message        string          `json:"message"`
		CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
	} `json:"error"`
}

// itemNotification carries one thread item. Only a handful of item types are
// read; reasoning items are matched precisely so they can be dropped.
type itemNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Item     struct {
		ID   string `json:"id"`
		Type string `json:"type"`

		// agentMessage
		Text  string `json:"text"`
		Phase string `json:"phase"`

		// commandExecution
		Command  string `json:"command"`
		ExitCode *int   `json:"exitCode"`
		Status   string `json:"status"`

		// fileChange
		Changes []struct {
			Path string `json:"path"`
		} `json:"changes"`
	} `json:"item"`
}

// approvalDecision is the ReviewDecision enum. StreamCore only ever sends
// "decline": a spoken confirmation authorises work inside the assigned
// worktree, and anything Codex has to ask about is by definition outside it.
const approvalDecline = "decline"

type approvalResponse struct {
	Decision string `json:"decision"`
}

// turnInterruptParams cancels an in-flight turn through the official mechanism
// rather than killing the process.
type turnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}
