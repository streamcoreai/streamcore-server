package github

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// protectedBranches can never be a push target. This is a floor, not a policy
// engine: GitHub's own branch protection is the real defence, but StreamCore
// should not need it to be configured correctly before refusing.
var protectedBranches = map[string]bool{
	"main":       true,
	"master":     true,
	"production": true,
	"release":    true,
	"trunk":      true,
	"develop":    true,
}

// secretFilePatterns are paths a generated change has no business touching. A
// match aborts the PR rather than asking; a model that wants to commit a
// private key is not going to argue its way out of this.
var secretFilePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(^|/)\.env(\.|$)`),
	regexp.MustCompile(`(?i)\.(pem|key|p12|pfx|jks|keystore)$`),
	regexp.MustCompile(`(?i)(^|/)id_(rsa|dsa|ecdsa|ed25519)$`),
	regexp.MustCompile(`(?i)(^|/)(credentials|auth)\.json$`),
	regexp.MustCompile(`(?i)(^|/)\.npmrc$`),
	regexp.MustCompile(`(?i)(^|/)\.netrc$`),
	regexp.MustCompile(`(?i)secret`),
	// Workflow files are explicitly out of scope for this phase, and the App
	// does not request the permission that would let them be pushed anyway.
	regexp.MustCompile(`(?i)^\.github/workflows/`),
}

var branchNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,100}$`)

// PullRequestRequest describes a branch to publish. WorktreePath must already
// have been validated by whoever owns the workspace; this package only ever
// reads it and refuses anything that does not look like a git checkout.
type PullRequestRequest struct {
	Repository   string
	WorktreePath string
	Branch       string
	Base         string
	Title        string
	Body         string

	// TestsRun records whether the developer task actually ran a test command.
	// A PR is refused without it: "open a PR" after an unverified edit is the
	// one path where a confident wrong answer costs someone a review cycle.
	TestsRun bool
}

// PullRequestResult is what the agent reports back.
type PullRequestResult struct {
	Repository   string `json:"repository"`
	Number       int    `json:"number"`
	URL          string `json:"url"`
	Branch       string `json:"branch"`
	Base         string `json:"base"`
	Commit       string `json:"commit"`
	ChangedFiles int    `json:"changed_files"`
}

// CreatePullRequest commits the worktree, pushes the branch, and opens a pull
// request. Every precondition is checked before the write-scoped token is
// minted, so a rejected request never creates a credential at all.
func (s *Service) CreatePullRequest(ctx context.Context, req PullRequestRequest) (*PullRequestResult, error) {
	full, err := s.client.Authorize(ctx, req.Repository)
	if err != nil {
		return nil, err
	}
	if !req.TestsRun {
		return nil, fmt.Errorf("no tests have been run for this change yet; run the tests before opening a pull request")
	}
	if req.WorktreePath == "" {
		return nil, fmt.Errorf("no isolated worktree is associated with this task")
	}
	if info, err := os.Stat(filepath.Join(req.WorktreePath, ".git")); err != nil || info == nil {
		return nil, fmt.Errorf("%s is not a git worktree", req.WorktreePath)
	}

	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = fmt.Sprintf("streamcore/fix-%s", time.Now().UTC().Format("20060102-150405"))
	}
	if err := validateBranch(branch); err != nil {
		return nil, err
	}

	base := strings.TrimSpace(req.Base)
	if base == "" {
		info, err := s.client.getRepo(ctx, full)
		if err != nil {
			return nil, err
		}
		base = info.DefaultBranch
	}
	if strings.EqualFold(base, branch) {
		return nil, fmt.Errorf("the pull request base and head are both %q", base)
	}

	changed, err := s.git.changedFiles(ctx, req.WorktreePath)
	if err != nil {
		return nil, err
	}
	if len(changed) == 0 {
		return nil, fmt.Errorf("the worktree has no changes to open a pull request for")
	}
	if offending := scanForSecrets(changed); offending != "" {
		return nil, fmt.Errorf("refusing to open a pull request: %s must not be committed by an agent", offending)
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "StreamCore: automated fix"
	}

	if err := s.git.checkoutBranch(ctx, req.WorktreePath, branch); err != nil {
		return nil, err
	}
	sha, err := s.git.commitAll(ctx, req.WorktreePath, title, req.Body)
	if err != nil {
		return nil, err
	}

	// The write token is minted here, at the last possible moment, scoped to
	// this repository and to contents+pull_requests only.
	token, err := s.auth.InstallationToken(ctx, full, WritePermissions())
	if err != nil {
		return nil, err
	}
	remote := fmt.Sprintf("%s/%s.git", githubHost(s.client.baseURL), full)
	if err := s.git.push(ctx, req.WorktreePath, remote, branch, token); err != nil {
		return nil, err
	}

	var created struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	err = s.client.do(ctx, full, WritePermissions(), request{
		method: "POST",
		path:   "/repos/" + full + "/pulls",
		body: map[string]any{
			"title": title,
			"head":  branch,
			"base":  base,
			"body":  req.Body,
			"draft": false,
		},
	}, &created)
	if err != nil {
		return nil, err
	}

	return &PullRequestResult{
		Repository:   full,
		Number:       created.Number,
		URL:          created.HTMLURL,
		Branch:       branch,
		Base:         base,
		Commit:       shortSHA(sha),
		ChangedFiles: len(changed),
	}, nil
}

func validateBranch(branch string) error {
	if protectedBranches[strings.ToLower(branch)] {
		return fmt.Errorf("refusing to push to protected branch %q", branch)
	}
	if !branchNamePattern.MatchString(branch) {
		return fmt.Errorf("branch name %q is not acceptable", branch)
	}
	if strings.Contains(branch, "..") {
		return fmt.Errorf("branch name %q is not acceptable", branch)
	}
	return nil
}

func scanForSecrets(paths []string) string {
	for _, path := range paths {
		for _, pattern := range secretFilePatterns {
			if pattern.MatchString(path) {
				return path
			}
		}
	}
	return ""
}

// githubHost turns an API base URL into the matching git host. api.github.com
// is the one case where the two differ.
func githubHost(apiBase string) string {
	if apiBase == "" || strings.Contains(apiBase, "api.github.com") {
		return "https://github.com"
	}
	return strings.TrimSuffix(strings.TrimRight(apiBase, "/"), "/api/v3")
}

// gitRunner shells out to git. It exists so the push path can be swapped in
// tests without a network, and so every invocation goes through one place that
// keeps the token out of argv and out of .git/config.
type gitRunner struct {
	binary     string
	authorName string
	authorMail string
}

func newGitRunner() *gitRunner {
	return &gitRunner{
		binary:     "git",
		authorName: "StreamCore",
		authorMail: "streamcore@users.noreply.github.com",
	}
}

func (g *gitRunner) run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, g.binary, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], truncate(redactURLCredentials(stderr.String()), 300))
	}
	return stdout.String(), nil
}

func (g *gitRunner) changedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := g.run(ctx, dir, nil, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		// Renames are reported as "old -> new"; the destination is what lands.
		if _, after, ok := strings.Cut(path, " -> "); ok {
			path = after
		}
		files = append(files, strings.Trim(path, `"`))
	}
	return files, nil
}

func (g *gitRunner) checkoutBranch(ctx context.Context, dir, branch string) error {
	if _, err := g.run(ctx, dir, nil, "checkout", "-B", branch); err != nil {
		return err
	}
	return nil
}

func (g *gitRunner) commitAll(ctx context.Context, dir, title, body string) (string, error) {
	if _, err := g.run(ctx, dir, nil, "add", "--all"); err != nil {
		return "", err
	}
	message := title
	if strings.TrimSpace(body) != "" {
		message += "\n\n" + body
	}
	_, err := g.run(ctx, dir, nil,
		"-c", "user.name="+g.authorName,
		"-c", "user.email="+g.authorMail,
		"commit", "--message", message)
	if err != nil {
		return "", err
	}
	sha, err := g.run(ctx, dir, nil, "rev-parse", "HEAD")
	return strings.TrimSpace(sha), err
}

// push sends the branch to GitHub using a token supplied through GIT_ASKPASS,
// so it never appears in argv, in .git/config, or in the reflog.
func (g *gitRunner) push(ctx context.Context, dir, remote, branch, token string) error {
	askpass, cleanup, err := writeAskpass()
	if err != nil {
		return err
	}
	defer cleanup()

	env := []string{
		"GIT_ASKPASS=" + askpass,
		"STREAMCORE_GIT_TOKEN=" + token,
		"GIT_TERMINAL_PROMPT=0",
	}
	// x-access-token is GitHub's username for installation tokens; the token
	// itself arrives as the password, from the askpass helper.
	authenticated := strings.Replace(remote, "https://", "https://x-access-token@", 1)
	_, err = g.run(ctx, dir, env, "push", authenticated, "HEAD:refs/heads/"+branch)
	return err
}

// writeAskpass drops the helper outside the worktree on purpose: anything left
// inside it would show up as an untracked file in the very change being
// reviewed.
func writeAskpass() (string, func(), error) {
	file, err := os.CreateTemp("", "streamcore-askpass-*.sh")
	if err != nil {
		return "", nil, fmt.Errorf("prepare git credentials: %w", err)
	}
	name := file.Name()
	cleanup := func() { os.Remove(name) }

	if _, err := file.WriteString("#!/bin/sh\nprintf '%s' \"$STREAMCORE_GIT_TOKEN\"\n"); err != nil {
		file.Close()
		cleanup()
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.Chmod(name, 0o700); err != nil {
		cleanup()
		return "", nil, err
	}
	return name, cleanup, nil
}

// redactURLCredentials strips any user:password that git echoed back in an
// error message.
var urlCredentials = regexp.MustCompile(`(https?://)[^/@\s]+@`)

func redactURLCredentials(text string) string {
	return urlCredentials.ReplaceAllString(text, "$1***@")
}
