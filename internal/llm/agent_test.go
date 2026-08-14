package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureAgent stands in for an external agent, recording what it is sent and
// replying with a fixed line.
type captureAgent struct {
	srv      *httptest.Server
	requests []agentRequest
}

func newCaptureAgent(t *testing.T) *captureAgent {
	t.Helper()

	c := &captureAgent{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req agentRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("agent received unparseable body %q: %v", body, err)
		}
		c.requests = append(c.requests, req)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"ok."}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *captureAgent) client(resourceID string) Client {
	return NewAgentClient(c.srv.URL, "", 0, resourceID)
}

func TestChatForwardsTheResourceID(t *testing.T) {
	agent := newCaptureAgent(t)
	client := agent.client("user_8891")

	if _, err := client.Chat(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if len(agent.requests) != 1 {
		t.Fatalf("agent saw %d requests, want 1", len(agent.requests))
	}
	if got := agent.requests[0].ResourceID; got != "user_8891" {
		t.Fatalf("resource_id = %q, want user_8891", got)
	}
}

// The rolling summary uses the same endpoint, so it needs the resource too.
func TestOneShotForwardsTheResourceID(t *testing.T) {
	agent := newCaptureAgent(t)
	client := agent.client("user_8891")

	if _, err := client.OneShot(context.Background(), "summarise", "the call"); err != nil {
		t.Fatalf("OneShot: %v", err)
	}

	if got := agent.requests[0].ResourceID; got != "user_8891" {
		t.Fatalf("resource_id = %q, want user_8891", got)
	}
}

// Omit the field entirely when anonymous. An empty string would pool every
// anonymous caller into one shared resource.
func TestResourceIDIsOmittedWhenUnknown(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"ok."}`))
	}))
	defer srv.Close()

	client := NewAgentClient(srv.URL, "", 0, "")
	if _, err := client.Chat(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if _, present := fields["resource_id"]; present {
		t.Fatalf("resource_id present in an anonymous request: %s", body)
	}
}

// Reset drops the history, not the person.
func TestResetRotatesTheSessionButKeepsTheResource(t *testing.T) {
	agent := newCaptureAgent(t)
	client := agent.client("user_8891")

	if _, err := client.Chat(context.Background(), "first", nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	client.Reset()
	if _, err := client.Chat(context.Background(), "second", nil, nil); err != nil {
		t.Fatalf("Chat after reset: %v", err)
	}

	before, after := agent.requests[0], agent.requests[1]
	if before.SessionID == after.SessionID {
		t.Fatal("session_id survived a Reset")
	}
	if after.ResourceID != "user_8891" {
		t.Fatalf("resource_id after reset = %q, want user_8891", after.ResourceID)
	}
}
