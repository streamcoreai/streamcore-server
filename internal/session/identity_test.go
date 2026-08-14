package session

import (
	"testing"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

func identitySession(t *testing.T) *Session {
	t.Helper()
	return NewSession("s1", &config.Config{}, nil, nil)
}

func TestResourceIDIsEmptyUntilAsserted(t *testing.T) {
	s := identitySession(t)
	if got := s.ResourceID(); got != "" {
		t.Fatalf("resource id = %q, want empty", got)
	}
}

func TestSetResourceIDBindsTheCaller(t *testing.T) {
	s := identitySession(t)
	s.SetResourceID("user_8891")
	if got := s.ResourceID(); got != "user_8891" {
		t.Fatalf("resource id = %q, want user_8891", got)
	}
}

// A resume runs the same WHIP path, calling this again mid-conversation. If the
// second call won, a redial could hand the call to someone else.
func TestSetResourceIDDoesNotOverwrite(t *testing.T) {
	s := identitySession(t)
	s.SetResourceID("user_8891")
	s.SetResourceID("someone_else")

	if got := s.ResourceID(); got != "user_8891" {
		t.Fatalf("resource id = %q, want user_8891", got)
	}
}

// An anonymous redial must not clear an identity already set.
func TestSetResourceIDIgnoresEmpty(t *testing.T) {
	s := identitySession(t)
	s.SetResourceID("user_8891")
	s.SetResourceID("")

	if got := s.ResourceID(); got != "user_8891" {
		t.Fatalf("resource id = %q, want user_8891", got)
	}
}
