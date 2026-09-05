package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceRefusesPathsOutsideRoot(t *testing.T) {
	workspace := newWorkspace(t)

	for _, path := range []string{
		"/etc",
		"/etc/passwd",
		os.TempDir(),
		filepath.Join(workspace.Root(), "..", "elsewhere"),
		filepath.Join(workspace.Root(), worktreeDir, "..", "..", "escape"),
		"relative/path",
		"",
	} {
		if err := workspace.contains(path); err == nil {
			t.Fatalf("%q was accepted as inside the workspace", path)
		}
	}
	if err := workspace.contains(filepath.Join(workspace.Root(), worktreeDir, "task_00")); err != nil {
		t.Fatalf("a path inside the workspace was refused: %v", err)
	}
}

// A symlink planted inside the workspace must not become a way out of it.
func TestWorkspaceRefusesSymlinkEscape(t *testing.T) {
	workspace := newWorkspace(t)
	outside := t.TempDir()
	link := filepath.Join(workspace.Root(), worktreeDir, "sneaky")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := workspace.contains(link); err == nil {
		t.Fatal("a symlink out of the workspace was accepted")
	}
	if err := workspace.Remove(context.Background(), link); err == nil {
		t.Fatal("a symlink out of the workspace was removable")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("the directory outside the workspace was deleted")
	}
}

func TestWorkspaceRemoveOnlyDeletesTaskTrees(t *testing.T) {
	workspace := newWorkspace(t)
	ctx := context.Background()

	cache := filepath.Join(workspace.Root(), repoCacheDir, "a__b")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Remove(ctx, cache); err == nil {
		t.Fatal("the shared repository cache was removable as a task worktree")
	}
	if _, err := os.Stat(cache); err != nil {
		t.Fatal("the repository cache was deleted")
	}

	tree := filepath.Join(workspace.Root(), worktreeDir, NewTaskID())
	if err := os.MkdirAll(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Remove(ctx, tree); err != nil {
		t.Fatalf("a task worktree could not be removed: %v", err)
	}
}

func TestPrepareRejectsUntrustedNames(t *testing.T) {
	workspace := newWorkspace(t)
	credentials := func(context.Context, string) (string, string, error) {
		t.Fatal("a rejected name reached the credential path")
		return "", "", nil
	}

	for _, repo := range []string{"../../etc", "a/../b", "/etc/passwd", "a", "~/x", "a/b/c"} {
		if _, err := workspace.Prepare(context.Background(), repo, NewTaskID(), "", credentials); err == nil {
			t.Fatalf("%q was accepted as a repository", repo)
		}
	}
	if _, err := workspace.Prepare(context.Background(), "a/b", "task_../../etc", "", credentials); err == nil {
		t.Fatal("a forged task id was accepted")
	}
}

// The end-to-end check: a real clone, a real worktree, and a remote that
// carries no credential afterwards.
func TestPrepareCreatesIsolatedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := newWorkspace(t)
	origin := seedRepo(t)

	credentials := func(context.Context, string) (string, string, error) {
		return "file://" + origin, "unused-for-a-local-clone", nil
	}

	taskID := NewTaskID()
	tree, err := workspace.Prepare(context.Background(), "streamcoreai/fixture", taskID, "origin/HEAD", credentials)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := workspace.contains(tree); err != nil {
		t.Fatalf("the worktree is outside the workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, "a.txt")); err != nil {
		t.Fatalf("the worktree is missing repository content: %v", err)
	}

	// Nothing inside the workspace may hold a GitHub credential.
	config, err := os.ReadFile(filepath.Join(workspace.Root(), repoCacheDir, "streamcoreai__fixture", ".git", "config"))
	if err != nil {
		t.Fatalf("read cache config: %v", err)
	}
	if strings.Contains(string(config), "x-access-token") || strings.Contains(string(config), "unused-for-a-local-clone") {
		t.Fatalf("a credential was written into the checkout:\n%s", config)
	}

	// A second call for the same task reuses the worktree rather than
	// scattering copies of the repository around.
	again, err := workspace.Prepare(context.Background(), "streamcoreai/fixture", taskID, "origin/HEAD", credentials)
	if err != nil || again != tree {
		t.Fatalf("prepare was not idempotent: %q %v", again, err)
	}
}

func TestWorkspaceDiffIsBounded(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := newWorkspace(t)
	tree := filepath.Join(workspace.Root(), worktreeDir, NewTaskID())
	if err := os.MkdirAll(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, tree, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(tree, "big.txt"), []byte(strings.Repeat("x\n", 50000)), 0o600); err != nil {
		t.Fatal(err)
	}

	diff, truncated, err := workspace.Diff(context.Background(), tree, 512)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !truncated || len(diff) > 512 {
		t.Fatalf("diff was not capped: %d bytes, truncated=%v", len(diff), truncated)
	}
}

func newWorkspace(t *testing.T) *Workspace {
	t.Helper()
	workspace, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	return workspace
}

func seedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-m", "seed")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
}

func TestOrphansSkipsLiveTasks(t *testing.T) {
	workspace := newWorkspace(t)
	live := NewTaskID()
	stale := NewTaskID()

	for _, id := range []string{live, stale} {
		if err := os.MkdirAll(filepath.Join(workspace.Root(), worktreeDir, id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A directory that is not a task worktree at all.
	if err := os.MkdirAll(filepath.Join(workspace.Root(), worktreeDir, "not-a-task"), 0o700); err != nil {
		t.Fatal(err)
	}

	orphans := workspace.Orphans(0, map[string]bool{live: true})
	if len(orphans) != 1 || filepath.Base(orphans[0]) != stale {
		t.Fatalf("orphan list is %v", orphans)
	}

	// Nothing is old enough under the real retention window.
	if late := workspace.Orphans(WorktreeRetention, nil); len(late) != 0 {
		t.Fatalf("fresh worktrees were swept: %v", late)
	}
}
