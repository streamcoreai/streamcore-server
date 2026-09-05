package github

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtectedBranchesAreRefused(t *testing.T) {
	for _, branch := range []string{"main", "Master", "production", "release", "develop"} {
		if err := validateBranch(branch); err == nil {
			t.Fatalf("%q was accepted as a push target", branch)
		}
	}
	for _, branch := range []string{"..", "a b", "-leading", "x/../y", strings.Repeat("a", 200)} {
		if err := validateBranch(branch); err == nil {
			t.Fatalf("%q was accepted as a branch name", branch)
		}
	}
	if err := validateBranch("streamcore/fix-20260101-120000"); err != nil {
		t.Fatalf("a generated branch name was rejected: %v", err)
	}
}

func TestSecretFilesAbortAPullRequest(t *testing.T) {
	cases := []string{
		".env",
		"deploy/.env.production",
		"secrets/app.pem",
		"config/id_rsa",
		"credentials.json",
		".npmrc",
		".github/workflows/ci.yml",
	}
	for _, path := range cases {
		if scanForSecrets([]string{"internal/ok.go", path}) == "" {
			t.Fatalf("%q was not caught by the secret scan", path)
		}
	}
	if offending := scanForSecrets([]string{"internal/a.go", "README.md"}); offending != "" {
		t.Fatalf("an ordinary change was flagged: %q", offending)
	}
}

func TestGitHostDerivation(t *testing.T) {
	if got := githubHost("https://api.github.com"); got != "https://github.com" {
		t.Fatalf("public host is %q", got)
	}
	if got := githubHost("https://ghe.example.com/api/v3"); got != "https://ghe.example.com" {
		t.Fatalf("enterprise host is %q", got)
	}
}

func TestRedactURLCredentials(t *testing.T) {
	message := "fatal: could not read from https://x-access-token:ghs_supersecret@github.com/a/b.git"
	redacted := redactURLCredentials(message)
	if strings.Contains(redacted, "ghs_supersecret") {
		t.Fatalf("a token survived redaction: %s", redacted)
	}
}

// The whole point of the write path is that it refuses more often than it
// fires. Each of these must fail before any credential is minted.
func TestCreatePullRequestPreconditions(t *testing.T) {
	tokens := &stubTokens{}
	service := &Service{
		client: NewClient(tokens, []string{"a/b"}, ""),
		git:    newGitRunner(),
	}
	ctx := context.Background()

	t.Run("tests not run", func(t *testing.T) {
		_, err := service.CreatePullRequest(ctx, PullRequestRequest{
			Repository:   "a/b",
			WorktreePath: t.TempDir(),
			TestsRun:     false,
		})
		if err == nil || !strings.Contains(err.Error(), "tests") {
			t.Fatalf("an unverified change was publishable: %v", err)
		}
	})

	t.Run("no worktree", func(t *testing.T) {
		_, err := service.CreatePullRequest(ctx, PullRequestRequest{
			Repository: "a/b",
			TestsRun:   true,
		})
		if err == nil || !strings.Contains(err.Error(), "worktree") {
			t.Fatalf("a task with no worktree was publishable: %v", err)
		}
	})

	t.Run("not a git checkout", func(t *testing.T) {
		_, err := service.CreatePullRequest(ctx, PullRequestRequest{
			Repository:   "a/b",
			WorktreePath: t.TempDir(),
			TestsRun:     true,
		})
		if err == nil || !strings.Contains(err.Error(), "git worktree") {
			t.Fatalf("an arbitrary directory was publishable: %v", err)
		}
	})

	t.Run("protected branch", func(t *testing.T) {
		_, err := service.CreatePullRequest(ctx, PullRequestRequest{
			Repository:   "a/b",
			WorktreePath: initRepo(t, true),
			Branch:       "main",
			TestsRun:     true,
		})
		if err == nil || !strings.Contains(err.Error(), "protected branch") {
			t.Fatalf("a push to main was allowed: %v", err)
		}
	})

	t.Run("no changes", func(t *testing.T) {
		_, err := service.CreatePullRequest(ctx, PullRequestRequest{
			Repository:   "a/b",
			WorktreePath: initRepo(t, false),
			Branch:       "streamcore/fix",
			Base:         "main",
			TestsRun:     true,
		})
		if err == nil || !strings.Contains(err.Error(), "no changes") {
			t.Fatalf("an empty change was publishable: %v", err)
		}
	})

	t.Run("secret file", func(t *testing.T) {
		tree := initRepo(t, false)
		if err := os.WriteFile(filepath.Join(tree, ".env"), []byte("TOKEN=hunter2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := service.CreatePullRequest(ctx, PullRequestRequest{
			Repository:   "a/b",
			WorktreePath: tree,
			Branch:       "streamcore/fix",
			Base:         "main",
			TestsRun:     true,
		})
		if err == nil || !strings.Contains(err.Error(), "must not be committed") {
			t.Fatalf("a .env file was publishable: %v", err)
		}
	})

	// Nothing above got as far as needing a credential.
	if tokens.lastPerm.Load() != nil && tokens.lastPerm.Load().(string) == WritePermissions().key() {
		t.Fatal("a write token was minted for a refused pull request")
	}
}

// initRepo builds a throwaway git checkout. withChange leaves an uncommitted
// file so the "has changes" precondition passes.
func initRepo(t *testing.T, withChange bool) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	if withChange {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
