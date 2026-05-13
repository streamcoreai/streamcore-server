package testturn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/streamcoreai/server/internal/llm"
	"github.com/streamcoreai/server/internal/plugin"
)

func TestHandlerReturnsSpokenResponse(t *testing.T) {
	handler := newHTTPHandler(func(ctx context.Context, req TurnRequest) (TurnResponse, error) {
		if got := req.latestCustomerText(); got != "what does StreamCoreAI do?" {
			t.Fatalf("latestCustomerText() = %q", got)
		}
		if prompt := req.prompt(); !strings.Contains(prompt, "Conversation transcript:") {
			t.Fatalf("prompt() missing transcript context: %q", prompt)
		}
		return TurnResponse{Spoken: "StreamCoreAI runs realtime voice agents.", LatencyMs: 12}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/test-turn", strings.NewReader(`{
		"suiteName": "Voice Agent TestOps",
		"customerText": "what does StreamCoreAI do?",
		"merchant": {"name": "StreamCoreAI demo"},
		"messages": [
			{"role": "customer", "text": "what does StreamCoreAI do?"}
		]
	}`))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body TurnResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Spoken != "StreamCoreAI runs realtime voice agents." {
		t.Fatalf("spoken = %q", body.Spoken)
	}
}

func TestHandlerRejectsMissingText(t *testing.T) {
	handler := newHTTPHandler(func(ctx context.Context, req TurnRequest) (TurnResponse, error) {
		t.Fatal("runner should not be called")
		return TurnResponse{}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/test-turn", strings.NewReader(`{"messages":[]}`))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandlerMapsRunnerErrors(t *testing.T) {
	handler := newHTTPHandler(func(ctx context.Context, req TurnRequest) (TurnResponse, error) {
		return TurnResponse{}, errors.New("provider unavailable")
	})

	req := httptest.NewRequest(http.MethodPost, "/test-turn", strings.NewReader(`{"text":"hello"}`))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "test turn failed") {
		t.Fatalf("body missing generic runner error: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "provider unavailable") {
		t.Fatalf("body leaked runner error: %s", rec.Body.String())
	}
}

func TestHandlerRejectsTooLargeBody(t *testing.T) {
	handler := newHTTPHandler(func(ctx context.Context, req TurnRequest) (TurnResponse, error) {
		t.Fatal("runner should not be called")
		return TurnResponse{}, nil
	})

	body := `{"text":"` + strings.Repeat("a", maxRequestBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/test-turn", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAgentConfigureClientSkipsVisionTool(t *testing.T) {
	pluginMgr := plugin.NewManager("")
	pluginMgr.RegisterNative(fakeTool{name: unsupportedVisionTool})
	pluginMgr.RegisterNative(fakeTool{name: "math.calculate"})

	client := &fakeLLMClient{}
	newAgent(nil, pluginMgr, nil).configureClient(client)

	if len(client.tools) != 1 {
		t.Fatalf("configured tools = %d, want 1", len(client.tools))
	}
	if client.tools[0].Name != "math.calculate" {
		t.Fatalf("configured tool = %q", client.tools[0].Name)
	}
}

type fakeTool struct {
	name string
}

func (t fakeTool) Name() string                            { return t.name }
func (t fakeTool) Description() string                     { return "test tool" }
func (t fakeTool) Parameters() json.RawMessage             { return json.RawMessage(`{"type":"object"}`) }
func (t fakeTool) Execute(json.RawMessage) (string, error) { return "ok", nil }
func (t fakeTool) ConfirmationRequired() bool              { return false }
func (t fakeTool) ThinkingSound() bool                     { return false }

type fakeLLMClient struct {
	tools []llm.ToolDefinition
}

func (c *fakeLLMClient) Chat(context.Context, string, func(string), func(string)) (string, error) {
	return "", nil
}
func (c *fakeLLMClient) SetTools(tools []llm.ToolDefinition) { c.tools = tools }
func (c *fakeLLMClient) SetToolHandler(func(context.Context, llm.ToolCall) (string, error)) {
}
func (c *fakeLLMClient) AppendSystemPrompt(string) {}
func (c *fakeLLMClient) Reset()                    {}
