package signaling

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func requestWith(claim, header string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/whip", nil)
	if header != "" {
		r.Header.Set(ResourceIDHeader, header)
	}
	if claim != "" {
		r = r.WithContext(WithResourceID(r.Context(), claim))
	}
	return r
}

// If a header could override a claim, anything able to reach /whip could claim
// to be someone else.
func TestVerifiedClaimBeatsTheHeader(t *testing.T) {
	got := resolveResourceID(requestWith("user_8891", "attacker"))
	if got != "user_8891" {
		t.Fatalf("resource id = %q, want user_8891", got)
	}
}

func TestHeaderIsUsedWhenNoClaimIsPresent(t *testing.T) {
	got := resolveResourceID(requestWith("", "+14155550123"))
	if got != "+14155550123" {
		t.Fatalf("resource id = %q, want +14155550123", got)
	}
}

func TestNoIdentityIsANormalAnswer(t *testing.T) {
	if got := resolveResourceID(requestWith("", "")); got != "" {
		t.Fatalf("resource id = %q, want empty", got)
	}
}

// Empty is dropped rather than stored. Absent and empty mean the same thing.
func TestEmptyIdentityIsNotStoredInContext(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/whip", nil)
	ctx := WithResourceID(r.Context(), "")
	if got := ResourceIDFromContext(ctx); got != "" {
		t.Fatalf("resource id = %q, want empty", got)
	}
}
