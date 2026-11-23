package stt

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestOpenAITranscriptionModel(t *testing.T) {
	for _, tc := range []struct {
		name        string
		configModel string
		wantModel   string
	}{
		{"default", "", "gpt-transcribe"},
		{"explicit override", "gpt-4o-transcribe", "gpt-4o-transcribe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testOpenAITranscriptionModel(t, tc.configModel, tc.wantModel)
		})
	}
}

func testOpenAITranscriptionModel(t *testing.T, configModel, wantModel string) {
	t.Helper()

	var gotModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse transcription request: %v", err)
			http.Error(w, "invalid multipart form", http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("request path = %q, want /v1/audio/transcriptions", r.URL.Path)
		}
		gotModel = r.FormValue("model")
		audio, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("read audio file: %v", err)
			http.Error(w, "missing WAV file", http.StatusBadRequest)
			return
		}
		defer audio.Close()
		magic := make([]byte, 4)
		if n, err := io.ReadFull(audio, magic); n != 4 || err != nil || string(magic) != "RIFF" || header.Filename != "audio.wav" {
			t.Errorf("audio file = %q, header = %q, error = %v; want audio.wav with RIFF header", header.Filename, magic, err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":"hello"}`)
	}))
	defer server.Close()

	clientConfig := openai.DefaultConfig("test-key")
	clientConfig.BaseURL = server.URL + "/v1"
	var gotTranscript TranscriptResult
	client, err := NewOpenAIClient(context.Background(), "test-key", configModel, func(result TranscriptResult) {
		gotTranscript = result
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.Close()
	client.client = openai.NewClientWithConfig(clientConfig)

	client.transcribe([]byte{0, 0, 1, 0})

	if gotModel != wantModel {
		t.Errorf("transcription model = %q, want %q", gotModel, wantModel)
	}
	if gotTranscript.Text != "hello" || !gotTranscript.IsFinal {
		t.Errorf("transcript = %+v, want final transcript %q", gotTranscript, "hello")
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
