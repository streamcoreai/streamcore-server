package github

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

// stubTokens records what was asked for and can refuse a repository the way a
// GitHub App installation that lacks access does.
type stubTokens struct {
	calls    atomic.Int32
	denied   map[string]bool
	lastRepo atomic.Value
	lastPerm atomic.Value
}

func (s *stubTokens) InstallationToken(_ context.Context, repo string, perms Permissions) (string, error) {
	s.calls.Add(1)
	s.lastRepo.Store(repo)
	s.lastPerm.Store(perms.key())
	if s.denied[strings.ToLower(repo)] {
		return "", fmt.Errorf("422 — the app is not installed on that repository")
	}
	return "ghs_stub", nil
}

func TestAuthorizeRequiresAllowlist(t *testing.T) {
	tokens := &stubTokens{}
	client := NewClient(tokens, []string{"streamcoreai/streamcore-server"}, "")

	_, err := client.Authorize(context.Background(), "attacker/evil")
	if err == nil {
		t.Fatal("a repository outside the allowlist was accepted")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.calls.Load() != 0 {
		t.Fatal("a disallowed repository reached the credential path")
	}
}

// Being listed in config is a statement of intent. The installation is the
// actual grant, and both have to hold.
func TestAuthorizeRequiresInstallation(t *testing.T) {
	tokens := &stubTokens{denied: map[string]bool{"streamcoreai/esp32": true}}
	client := NewClient(tokens, []string{"streamcoreai/esp32"}, "")

	_, err := client.Authorize(context.Background(), "streamcoreai/esp32")
	if err == nil {
		t.Fatal("an allowlisted repository with no installation was accepted")
	}
	if !strings.Contains(err.Error(), "cannot access") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthorizeIsCaseInsensitiveAndCached(t *testing.T) {
	tokens := &stubTokens{}
	client := NewClient(tokens, []string{"StreamCoreAI/StreamCore-Server"}, "")
	ctx := context.Background()

	full, err := client.Authorize(ctx, "streamcoreai/streamcore-server")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if full != "streamcoreai/streamcore-server" {
		t.Fatalf("normalised name is %q", full)
	}
	if _, err := client.Authorize(ctx, "streamcoreai/streamcore-server"); err != nil {
		t.Fatalf("second authorize: %v", err)
	}
	if tokens.calls.Load() != 1 {
		t.Fatalf("the access check was repeated (%d mints)", tokens.calls.Load())
	}
}

// Authorize proves access with a read token. A write token must never be minted
// as a side effect of checking whether a repository is reachable.
func TestAuthorizeUsesReadPermissions(t *testing.T) {
	tokens := &stubTokens{}
	client := NewClient(tokens, []string{"a/b"}, "")

	if _, err := client.Authorize(context.Background(), "a/b"); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if got := tokens.lastPerm.Load().(string); got != ReadPermissions().key() {
		t.Fatalf("authorize minted %q, not a read token", got)
	}
}

func TestValidateRepoPath(t *testing.T) {
	for _, path := range []string{"../etc/passwd", "/etc/passwd", "a/../../b", `..\windows`, ""} {
		if err := validateRepoPath(path); err == nil {
			t.Fatalf("%q was accepted as a repository path", path)
		}
	}
	if err := validateRepoPath("internal/config/config.go"); err != nil {
		t.Fatalf("a normal path was rejected: %v", err)
	}
}
