package codex

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Directory layout under the configured workspace root:
//
//	repo-cache/<owner>__<name>/   one clone per repository, shared
//	worktrees/<task-id>/          one git worktree per developer task
//
// Every path is generated here from a repository name and a task id. Nothing
// derived from voice or model input ever reaches the filesystem.
const (
	repoCacheDir = "repo-cache"
	worktreeDir  = "worktrees"
)

// repoNamePattern is what a repository is allowed to look like before it is
// turned into a directory name. Anything else is refused rather than escaped.
var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

var taskIDPattern = regexp.MustCompile(`^task_[0-9a-f]{16}$`)

// Workspace manages isolated checkouts. The live StreamCore checkout is never
// one of them: everything lives under the configured root, and every delete is
// re-checked against the canonicalised root before it runs.
type Workspace struct {
	root      string
	gitBinary string
	// cloneTimeout bounds a first clone, which for a large repository is the
	// slowest thing this package does.
	cloneTimeout time.Duration
}

// NewWorkspace prepares the root, resolving symlinks once so every later
// containment check compares canonical paths.
func NewWorkspace(root string) (*Workspace, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("codex.workspace_root is required when codex is enabled")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(absolute, repoCacheDir), 0o700); err != nil {
		return nil, fmt.Errorf("create codex workspace: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(absolute, worktreeDir), 0o700); err != nil {
		return nil, fmt.Errorf("create codex workspace: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	return &Workspace{root: resolved, gitBinary: "git", cloneTimeout: 10 * time.Minute}, nil
}

// Root is the canonical workspace root.
func (w *Workspace) Root() string { return w.root }

// NewTaskID mints an opaque, filesystem-safe identifier for one developer task.
func NewTaskID() string {
	raw := make([]byte, 8)
	rand.Read(raw)
	return "task_" + hex.EncodeToString(raw)
}

// CloneURLFunc supplies a clone URL and a credential for one repository.
//
// The token it returns is used by StreamCore's own git process and is never
// written to the checkout: the remote is stored without credentials, so a later
// fetch from inside the worktree — by Codex, say — has nothing to authenticate
// with.
type CloneURLFunc func(ctx context.Context, repo string) (url string, token string, err error)

// Prepare returns an isolated worktree for one task, cloning the repository
// into the shared cache on first use.
func (w *Workspace) Prepare(ctx context.Context, repo, taskID, baseRef string, credentials CloneURLFunc) (string, error) {
	if !repoNamePattern.MatchString(repo) {
		return "", fmt.Errorf("repository %q is not a usable owner/name", repo)
	}
	if !taskIDPattern.MatchString(taskID) {
		return "", fmt.Errorf("internal error: malformed task id")
	}

	cache := filepath.Join(w.root, repoCacheDir, strings.ReplaceAll(repo, "/", "__"))
	if err := w.contains(cache); err != nil {
		return "", err
	}

	if _, err := os.Stat(filepath.Join(cache, ".git")); err != nil {
		if err := w.clone(ctx, repo, cache, credentials); err != nil {
			return "", err
		}
	} else if err := w.fetch(ctx, repo, cache, credentials); err != nil {
		// A stale cache is worth using; a failed fetch is not worth failing on.
		logDebug("fetch %s: %v", repo, err)
	}

	if baseRef == "" {
		baseRef = "origin/HEAD"
	}

	tree := filepath.Join(w.root, worktreeDir, taskID)
	if err := w.contains(tree); err != nil {
		return "", err
	}
	if _, err := os.Stat(tree); err == nil {
		return tree, nil
	}

	branch := "streamcore/" + taskID
	if _, err := w.git(ctx, cache, nil, "worktree", "add", "--force", "-B", branch, tree, baseRef); err != nil {
		return "", fmt.Errorf("create isolated worktree: %w", err)
	}
	return tree, nil
}

func (w *Workspace) clone(ctx context.Context, repo, cache string, credentials CloneURLFunc) error {
	url, token, err := credentials(ctx, repo)
	if err != nil {
		return err
	}
	env, cleanup, err := askpassEnv(token)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(ctx, w.cloneTimeout)
	defer cancel()

	parent := filepath.Dir(cache)
	// x-access-token is the username half of an installation credential; the
	// token arrives from the askpass helper, so it never enters argv and never
	// lands in the clone's .git/config.
	authenticated := strings.Replace(url, "https://", "https://x-access-token@", 1)
	if _, err := w.git(ctx, parent, env, "clone", "--no-tags", authenticated, cache); err != nil {
		return fmt.Errorf("clone %s: %w", repo, err)
	}
	// Rewrite the remote without the username so nothing inside the workspace
	// can authenticate to GitHub on its own.
	if _, err := w.git(ctx, cache, nil, "remote", "set-url", "origin", url); err != nil {
		return err
	}
	return nil
}

func (w *Workspace) fetch(ctx context.Context, repo, cache string, credentials CloneURLFunc) error {
	url, token, err := credentials(ctx, repo)
	if err != nil {
		return err
	}
	env, cleanup, err := askpassEnv(token)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	authenticated := strings.Replace(url, "https://", "https://x-access-token@", 1)
	_, err = w.git(ctx, cache, env, "fetch", "--prune", "--no-tags", authenticated, "+refs/heads/*:refs/remotes/origin/*")
	return err
}

// Diff returns the change a task has made, capped so a runaway edit cannot
// flood the model.
func (w *Workspace) Diff(ctx context.Context, tree string, limit int) (string, bool, error) {
	if err := w.contains(tree); err != nil {
		return "", false, err
	}
	// --intent-to-add so a brand new file shows up in the diff instead of being
	// invisible until it is staged.
	if _, err := w.git(ctx, tree, nil, "add", "--all", "--intent-to-add"); err != nil {
		return "", false, err
	}
	out, err := w.git(ctx, tree, nil, "diff", "--stat", "--patch")
	if err != nil {
		return "", false, err
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// Remove deletes one task's worktree. It refuses anything outside the root even
// if a caller managed to hand it a path from somewhere else.
func (w *Workspace) Remove(ctx context.Context, tree string) error {
	if err := w.contains(tree); err != nil {
		return err
	}
	if filepath.Dir(tree) != filepath.Join(w.root, worktreeDir) {
		return fmt.Errorf("refusing to remove %s: not a task worktree", tree)
	}
	if !taskIDPattern.MatchString(filepath.Base(tree)) {
		return fmt.Errorf("refusing to remove %s: not a task worktree", tree)
	}
	return os.RemoveAll(tree)
}

// contains is the single place that decides whether a path is inside the
// workspace. It resolves symlinks on the deepest existing ancestor, so a
// symlink planted mid-path cannot point the result somewhere else.
func (w *Workspace) contains(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("workspace paths must be absolute")
	}
	clean := filepath.Clean(path)

	probe := clean
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			// The resolved ancestor must sit inside the root, and the
			// unresolved remainder must not climb back out.
			remainder := strings.TrimPrefix(clean, probe)
			if strings.Contains(remainder, "..") {
				return fmt.Errorf("refusing path %s: it climbs out of the codex workspace", path)
			}
			if resolved != w.root && !strings.HasPrefix(resolved, w.root+string(os.PathSeparator)) {
				return fmt.Errorf("refusing path %s: it is outside the codex workspace", path)
			}
			return nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return fmt.Errorf("refusing path %s: nothing on it exists", path)
		}
		probe = parent
	}
}

func (w *Workspace) git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, w.gitBinary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, env...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], truncate(redactCredentials(stderr.String()), 300))
	}
	return stdout.String(), nil
}

// askpassEnv writes a throwaway helper that echoes the token from the
// environment, so git never sees it in argv.
func askpassEnv(token string) ([]string, func(), error) {
	file, err := os.CreateTemp("", "streamcore-codex-askpass-*.sh")
	if err != nil {
		return nil, nil, err
	}
	name := file.Name()
	cleanup := func() { os.Remove(name) }

	if _, err := file.WriteString("#!/bin/sh\nprintf '%s' \"$STREAMCORE_GIT_TOKEN\"\n"); err != nil {
		file.Close()
		cleanup()
		return nil, nil, err
	}
	file.Close()
	if err := os.Chmod(name, 0o700); err != nil {
		cleanup()
		return nil, nil, err
	}
	return []string{"GIT_ASKPASS=" + name, "STREAMCORE_GIT_TOKEN=" + token}, cleanup, nil
}

var urlCredentials = regexp.MustCompile(`(https?://)[^/@\s]+@`)

func redactCredentials(text string) string {
	return urlCredentials.ReplaceAllString(text, "$1***@")
}

// Orphans lists task worktrees older than maxAge that no live task refers to.
// Deciding and deleting are separate so the manager can protect the tasks it
// still owns without this package knowing anything about sessions.
func (w *Workspace) Orphans(maxAge time.Duration, live map[string]bool) []string {
	entries, err := os.ReadDir(filepath.Join(w.root, worktreeDir))
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-maxAge)

	var stale []string
	for _, entry := range entries {
		if !entry.IsDir() || !taskIDPattern.MatchString(entry.Name()) || live[entry.Name()] {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		stale = append(stale, filepath.Join(w.root, worktreeDir, entry.Name()))
	}
	return stale
}
