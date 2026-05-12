package testturn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if !strings.Contains(rec.Body.String(), "provider unavailable") {
		t.Fatalf("body missing runner error: %s", rec.Body.String())
	}
}
