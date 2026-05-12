package testturn

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/streamcoreai/server/internal/config"
	"github.com/streamcoreai/server/internal/llm"
	"github.com/streamcoreai/server/internal/plugin"
	"github.com/streamcoreai/server/internal/rag"
)

const maxRequestBytes = 1 << 20

type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
	At   string `json:"at,omitempty"`
}

type TurnRequest struct {
	Text         string    `json:"text,omitempty"`
	CustomerText string    `json:"customerText,omitempty"`
	Messages     []Message `json:"messages,omitempty"`
}

type Event struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Stage string `json:"stage,omitempty"`
	Ms    int64  `json:"ms,omitempty"`
}

type TurnResponse struct {
	Spoken    string  `json:"spoken"`
	Events    []Event `json:"events,omitempty"`
	LatencyMs int64   `json:"latencyMs"`
}

type turnRunner func(context.Context, TurnRequest) (TurnResponse, error)

// NewHandler returns the disabled-by-default text turn harness used by local
// regression tools. It bypasses WebRTC, STT, and TTS, but reuses the configured
// LLM, plugins, skills, and optional RAG context.
func NewHandler(cfg *config.Config, pluginMgr *plugin.Manager, ragClient rag.Client) http.HandlerFunc {
	return newHTTPHandler(newAgent(cfg, pluginMgr, ragClient).run)
}

func newHTTPHandler(run turnRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var req TurnRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
			return
		}

		if req.latestCustomerText() == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text or customerText is required"})
			return
		}

		resp, err := run(r.Context(), req)
		if err != nil {
			log.Printf("[test-turn] error: %v", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

type agent struct {
	cfg       *config.Config
	pluginMgr *plugin.Manager
	ragClient rag.Client
}

func newAgent(cfg *config.Config, pluginMgr *plugin.Manager, ragClient rag.Client) *agent {
	return &agent{cfg: cfg, pluginMgr: pluginMgr, ragClient: ragClient}
}

func (a *agent) run(ctx context.Context, req TurnRequest) (TurnResponse, error) {
	started := time.Now()

	client, err := llm.NewClient(a.cfg)
	if err != nil {
		return TurnResponse{}, err
	}
	a.configureClient(client)

	input := req.prompt()
	if a.ragClient != nil {
		chunks, err := a.ragClient.Search(ctx, req.latestCustomerText(), 0)
		if err != nil {
			log.Printf("[test-turn] RAG search error: %v", err)
		} else if len(chunks) > 0 {
			input = fmt.Sprintf("[Context:\n%s]\n\n%s", strings.Join(chunks, "\n---\n"), input)
		}
	}

	events := make([]Event, 0, 8)
	spoken, err := client.Chat(ctx, input, func(chunk string) {
		events = append(events, Event{Type: "response", Text: chunk})
	}, nil)
	if err != nil {
		return TurnResponse{}, err
	}
	spoken = strings.TrimSpace(spoken)
	if spoken == "" {
		return TurnResponse{}, fmt.Errorf("LLM returned an empty response")
	}

	latency := time.Since(started).Milliseconds()
	events = append(events, Event{Type: "timing", Stage: "llm_complete", Ms: latency})
	return TurnResponse{
		Spoken:    spoken,
		Events:    events,
		LatencyMs: latency,
	}, nil
}

func (a *agent) configureClient(client llm.Client) {
	if a.pluginMgr == nil {
		return
	}

	tools := a.pluginMgr.Tools()
	if len(tools) > 0 {
		defs := make([]llm.ToolDefinition, 0, len(tools))
		for _, tool := range tools {
			defs = append(defs, llm.ToolDefinition{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters:  tool.Parameters(),
			})
		}
		client.SetTools(defs)
		client.SetToolHandler(func(callCtx context.Context, call llm.ToolCall) (string, error) {
			tool, ok := a.pluginMgr.GetTool(call.Name)
			if !ok {
				return "", fmt.Errorf("unknown tool: %s", call.Name)
			}
			return tool.Execute(call.Arguments)
		})
	}

	if skillsPrompt := a.pluginMgr.SkillsPrompt(); skillsPrompt != "" {
		client.AppendSystemPrompt(skillsPrompt)
	}
}

func (req TurnRequest) latestCustomerText() string {
	if text := strings.TrimSpace(req.CustomerText); text != "" {
		return text
	}
	if text := strings.TrimSpace(req.Text); text != "" {
		return text
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if strings.EqualFold(req.Messages[i].Role, "customer") || strings.EqualFold(req.Messages[i].Role, "user") {
			return strings.TrimSpace(req.Messages[i].Text)
		}
	}
	return ""
}

func (req TurnRequest) prompt() string {
	messages := req.normalizedMessages()
	if len(messages) == 0 {
		return req.latestCustomerText()
	}

	var b strings.Builder
	b.WriteString("Conversation transcript:\n")
	for _, msg := range messages {
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "assistant":
			b.WriteString("Assistant: ")
		default:
			b.WriteString("User: ")
		}
		b.WriteString(strings.TrimSpace(msg.Text))
		b.WriteString("\n")
	}
	b.WriteString("\nRespond to the latest user turn. Keep the reply concise and natural for voice.")
	return b.String()
}

func (req TurnRequest) normalizedMessages() []Message {
	messages := make([]Message, 0, len(req.Messages)+1)
	for _, msg := range req.Messages {
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			continue
		}
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		messages = append(messages, Message{Role: role, Text: text, At: msg.At})
	}

	latest := req.latestCustomerText()
	if latest == "" {
		return messages
	}
	if len(messages) == 0 || strings.TrimSpace(messages[len(messages)-1].Text) != latest {
		messages = append(messages, Message{Role: "user", Text: latest})
	}
	return messages
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("[test-turn] write response error: %v", err)
	}
}
