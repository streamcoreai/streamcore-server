package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/streamcoreai/streamcore-server/internal/signaling"
)

// mintToken drives tokenHandler and returns the signed JWT it issued.
func mintToken(t *testing.T, secret, apiKey, body string) string {
	t.Helper()

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(http.MethodPost, "/token", reader)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	rec := httptest.NewRecorder()
	tokenHandler(secret, apiKey)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("token request: status %d, body %q", rec.Code, rec.Body.String())
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	return out.Token
}

// resourceSeenBy runs a token through jwtMiddleware and reports what the
// wrapped handler saw.
func resourceSeenBy(t *testing.T, secret, token string) string {
	t.Helper()

	var seen string
	req := httptest.NewRequest(http.MethodPost, "/whip", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	jwtMiddleware(secret, func(w http.ResponseWriter, r *http.Request) {
		seen = signaling.ResourceIDFromContext(r.Context())
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("middleware rejected a token it minted: status %d", rec.Code)
	}
	return seen
}

func TestTokenCarriesTheRequestedResourceID(t *testing.T) {
	const secret = "test-secret"

	token := mintToken(t, secret, "", `{"resource_id":"user_8891"}`)
	if got := resourceSeenBy(t, secret, token); got != "user_8891" {
		t.Fatalf("resource id = %q, want user_8891", got)
	}
}

// The common case — the SDKs send no body at all.
func TestTokenWithoutAResourceIDYieldsNoIdentity(t *testing.T) {
	const secret = "test-secret"

	for _, body := range []string{"", "{}", "not json at all"} {
		token := mintToken(t, secret, "", body)
		if got := resourceSeenBy(t, secret, token); got != "" {
			t.Fatalf("body %q: resource id = %q, want empty", body, got)
		}
	}
}

// A whitespace-only resource_id would otherwise be forwarded as if it
// identified someone.
func TestBlankResourceIDIsNotMintedAsAClaim(t *testing.T) {
	const secret = "test-secret"

	token := mintToken(t, secret, "", `{"resource_id":"   "}`)
	if got := resourceSeenBy(t, secret, token); got != "" {
		t.Fatalf("resource id = %q, want empty", got)
	}
}

// Wrong signature: reject the token instead of reading its claims.
func TestForgedTokenIsRejectedBeforeItsClaimsAreRead(t *testing.T) {
	forged := mintToken(t, "attacker-secret", "", `{"resource_id":"admin"}`)

	var reached bool
	req := httptest.NewRequest(http.MethodPost, "/whip", nil)
	req.Header.Set("Authorization", "Bearer "+forged)
	rec := httptest.NewRecorder()
	jwtMiddleware("real-secret", func(w http.ResponseWriter, r *http.Request) {
		reached = true
	})(rec, req)

	if reached {
		t.Fatal("a token signed with the wrong secret reached the handler")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// With an api_key configured, you need it to mint anything at all.
func TestResourceIDCannotBeMintedWithoutTheAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(`{"resource_id":"user_8891"}`))
	rec := httptest.NewRecorder()
	tokenHandler("test-secret", "the-api-key")(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
