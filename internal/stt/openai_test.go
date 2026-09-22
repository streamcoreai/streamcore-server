package stt

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestOpenAITranscriptionUsesConfiguredModel(t *testing.T) {
	const wantModel = "gpt-4o-transcribe"

	var gotModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse transcription request: %v", err)
			http.Error(w, "invalid multipart form", http.StatusBadRequest)
			return
		}
		gotModel = r.FormValue("model")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":"hello"}`)
	}))
	defer server.Close()

	clientConfig := openai.DefaultConfig("test-key")
	clientConfig.BaseURL = server.URL + "/v1"
	var gotTranscript string
	client := &openaiClient{
		client: openai.NewClientWithConfig(clientConfig),
		model:  wantModel,
		ctx:    context.Background(),
		onResult: func(result TranscriptResult) {
			gotTranscript = result.Text
		},
	}

	client.transcribe([]byte{0, 0, 1, 0})

	if gotModel != wantModel {
		t.Errorf("transcription model = %q, want %q", gotModel, wantModel)
	}
	if gotTranscript != "hello" {
		t.Errorf("transcript = %q, want %q", gotTranscript, "hello")
	}
}

// The transcription API is batch-oriented: only finals ever arrive, so the
// client must report finals-only and let the pipeline fall back to
// VAD-only barge-in (issue #75).
func TestOpenAIClientEmitsNoPartials(t *testing.T) {
	client, err := NewOpenAIClient(context.Background(), "test-key", "", func(TranscriptResult) {})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.Close()

	var c Client = client
	ep, ok := c.(PartialsEmitter)
	if !ok {
		t.Fatal("openaiClient does not satisfy PartialsEmitter")
	}
	if ep.EmitsPartials() {
		t.Error("openaiClient EmitsPartials = true, want false (batch transcription is finals-only)")
	}
}
