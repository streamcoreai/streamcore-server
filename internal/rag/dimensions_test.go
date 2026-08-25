package rag

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDimensionsFor(t *testing.T) {
	tests := []struct {
		model string
		want  int
		ok    bool
	}{
		{"", 1536, true},
		{"text-embedding-3-small", 1536, true},
		{"text-embedding-3-large", 3072, true},
		{"text-embedding-ada-002", 1536, true},
		{"some-model-shipped-next-year", 0, false},
	}

	for _, tt := range tests {
		got, ok := dimensionsFor(tt.model)
		if got != tt.want || ok != tt.ok {
			t.Errorf("dimensionsFor(%q) = (%d, %v), want (%d, %v)", tt.model, got, ok, tt.want, tt.ok)
		}
	}
}

func TestNewEmbeddingClientTracksWidth(t *testing.T) {
	if c := newEmbeddingClient("k", ""); c.model != defaultEmbeddingModel || c.dims != 1536 {
		t.Errorf("default client = (%q, %d), want (%q, 1536)", c.model, c.dims, defaultEmbeddingModel)
	}
	// An unknown model disables the check rather than failing every Embed call.
	if c := newEmbeddingClient("k", "mystery-model"); c.dims != 0 {
		t.Errorf("unknown model dims = %d, want 0", c.dims)
	}
}

func TestVectorWidth(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
		ok   bool
	}{
		{"postgrest text form", `"[0.1,0.2,-0.3]"`, 3, true},
		{"text form with spaces", `"[0.1, 0.2]"`, 2, true},
		{"json array", `[0.1,0.2,0.3,0.4]`, 4, true},
		{"null", `null`, 0, false},
		{"empty vector", `"[]"`, 0, false},
		{"not numbers", `"[a,b]"`, 0, false},
		{"empty array", `[]`, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := vectorWidth(json.RawMessage(tt.raw))
			if got != tt.want || ok != tt.ok {
				t.Errorf("vectorWidth(%s) = (%d, %v), want (%d, %v)", tt.raw, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestDimensionMismatchMessage(t *testing.T) {
	err := dimensionMismatch("documents", "text-embedding-3-large", 3072, 1536)
	for _, want := range []string{"documents", "vector(1536)", "3072", "text-embedding-3-large", "streamcore-cli"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q missing %q", err.Error(), want)
		}
	}
}

func TestModelMismatchMessage(t *testing.T) {
	err := modelMismatch("documents", "text-embedding-3-small", "text-embedding-ada-002")
	for _, want := range []string{"documents", "text-embedding-ada-002", "text-embedding-3-small", "streamcore-cli"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q missing %q", err.Error(), want)
		}
	}

	// A null model reads as an unmigrated row, not an empty string.
	if got := modelMismatch("documents", "text-embedding-3-small", "").Error(); !strings.Contains(got, "(null)") {
		t.Errorf("null model message = %q, want it to mention (null)", got)
	}
}

func TestMissingModelColumnCarriesMigration(t *testing.T) {
	err := missingModelColumn("chunks")
	for _, want := range []string{"ALTER TABLE chunks ADD COLUMN embedding_model", "SET NOT NULL", "chunks_embedding_model_idx"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("migration %q missing %q", err.Error(), want)
		}
	}
}

func TestIsMissingColumn(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{errors.New(`returned 400: {"code":"42703","message":"column documents.embedding_model does not exist"}`), true},
		{errors.New(`returned 400: {"message":"column embedding_model does not exist"}`), true},
		{errors.New(`returned 401: {"message":"invalid api key"}`), false},
		{errors.New("connection refused"), false},
	}
	for _, tt := range tests {
		if got := isMissingColumn(tt.err); got != tt.want {
			t.Errorf("isMissingColumn(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestVerifySupabaseStore(t *testing.T) {
	vec := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = "0.1"
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	row := func(width int, model string) string {
		return fmt.Sprintf(`[{"embedding":"%s","embedding_model":"%s"}]`, vec(width), model)
	}

	tests := []struct {
		name  string
		model string
		// mismatch* is the reply to the "any row with a different model" probe;
		// sampleBody answers the width query that follows it.
		mismatchStatus int
		mismatchBody   string
		sampleBody     string
		wantErr        string
	}{
		{
			name:           "store agrees",
			model:          "text-embedding-3-small",
			mismatchStatus: 200,
			mismatchBody:   `[]`,
			sampleBody:     row(1536, "text-embedding-3-small"),
		},
		{
			name:           "same width, different model",
			model:          "text-embedding-3-small",
			mismatchStatus: 200,
			mismatchBody:   row(1536, "text-embedding-ada-002"),
			wantErr:        "text-embedding-ada-002",
		},
		{
			name:           "unmigrated rows read as null",
			model:          "text-embedding-3-small",
			mismatchStatus: 200,
			mismatchBody:   `[{"embedding":"[0.1]","embedding_model":null}]`,
			wantErr:        "(null)",
		},
		{
			name:           "column predates the contract",
			model:          "text-embedding-3-small",
			mismatchStatus: 400,
			mismatchBody:   `{"code":"42703","message":"column documents.embedding_model does not exist"}`,
			wantErr:        "ALTER TABLE documents ADD COLUMN embedding_model",
		},
		{
			name:           "empty table",
			model:          "text-embedding-3-small",
			mismatchStatus: 200,
			mismatchBody:   `[]`,
			sampleBody:     `[]`,
		},
		{
			name:           "width disagrees",
			model:          "text-embedding-3-large",
			mismatchStatus: 200,
			mismatchBody:   `[]`,
			sampleBody:     row(1536, "text-embedding-3-large"),
			wantErr:        "vector(1536)",
		},
		{
			name:           "unauthorized leaves the store unverified",
			model:          "text-embedding-3-small",
			mismatchStatus: 401,
			mismatchBody:   `{"message":"invalid api key"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					if !strings.Contains(r.URL.RawQuery, "embedding_model.neq.") {
						t.Errorf("first query = %q, want the model mismatch probe", r.URL.RawQuery)
					}
					w.WriteHeader(tt.mismatchStatus)
					_, _ = w.Write([]byte(tt.mismatchBody))
					return
				}
				_, _ = w.Write([]byte(tt.sampleBody))
			}))
			defer srv.Close()

			err := verifySupabaseStore(srv.Client(), srv.URL, "key", "documents", tt.model)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("err = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("err = nil, want one mentioning %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestVerifySupabaseStoreUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	client := srv.Client()
	srv.Close()

	// A project that is down at boot must not take the server down with it.
	if err := verifySupabaseStore(client, srv.URL, "key", "documents", "text-embedding-3-small"); err != nil {
		t.Fatalf("unreachable project returned %v, want nil", err)
	}
}
