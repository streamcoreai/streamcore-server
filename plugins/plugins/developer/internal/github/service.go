package github

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Service is the GitHub half of the developer integration. It owns the API
// client and the credential path, and exposes exactly the operations the tools
// and the PR path need.
//
// Nothing here is ever handed to Codex. Codex receives an isolated checkout and
// a description of the failure — never a token, never the private key.
type Service struct {
	client *Client
	auth   *AppAuth
	// git is how a branch actually reaches GitHub. It is only used by the
	// confirmation-gated PR path.
	git *gitRunner
}

// NewService wires a Service over an App credential and a repository allowlist.
func NewService(auth *AppAuth, repositories []string, baseURL string) *Service {
	return &Service{
		client: NewClient(auth, repositories, baseURL),
		auth:   auth,
		git:    newGitRunner(),
	}
}

// CIFailure is the bounded result of investigating one failed workflow run. It
// is what both the LLM and Codex see; neither gets the raw log.
type CIFailure struct {
	Repository string      `json:"repository"`
	Branch     string      `json:"branch"`
	Workflow   string      `json:"workflow"`
	RunID      int64       `json:"run_id"`
	RunNumber  int         `json:"run_number"`
	Commit     string      `json:"commit"`
	Conclusion string      `json:"conclusion"`
	URL        string      `json:"url"`
	FinishedAt string      `json:"finished_at,omitempty"`
	FailedJobs []FailedJob `json:"failed_jobs"`
}

// FailedJob is one job that did not pass, reduced to the parts that explain it.
type FailedJob struct {
	Name        string   `json:"name"`
	Conclusion  string   `json:"conclusion"`
	FailedStep  string   `json:"failed_step,omitempty"`
	Annotations []string `json:"annotations,omitempty"`
	LogExcerpt  string   `json:"log_excerpt,omitempty"`
	URL         string   `json:"url,omitempty"`
}

// LatestCIFailure finds the most recent failed run on a branch and reduces it.
// An empty branch searches every branch; an empty workflow matches any.
func (s *Service) LatestCIFailure(ctx context.Context, repo, branch, workflow string) (*CIFailure, error) {
	full, err := s.client.Authorize(ctx, repo)
	if err != nil {
		return nil, err
	}

	runs, err := s.client.listWorkflowRuns(ctx, full, branch, "completed", 30)
	if err != nil {
		return nil, err
	}

	var failed *workflowRun
	for i := range runs {
		run := runs[i]
		if run.Conclusion != "failure" && run.Conclusion != "timed_out" {
			continue
		}
		if workflow != "" && !strings.EqualFold(run.Name, workflow) {
			continue
		}
		failed = &run
		break
	}
	if failed == nil {
		scope := "any branch"
		if branch != "" {
			scope = "branch " + branch
		}
		return nil, fmt.Errorf("no failed workflow run found for %s on %s in the last %d completed runs", full, scope, len(runs))
	}

	return s.describeRun(ctx, full, *failed)
}

// RunFailure reduces one specific run, failed or not.
func (s *Service) RunFailure(ctx context.Context, repo string, runID int64) (*CIFailure, error) {
	full, err := s.client.Authorize(ctx, repo)
	if err != nil {
		return nil, err
	}
	run, err := s.client.getWorkflowRun(ctx, full, runID)
	if err != nil {
		return nil, err
	}
	return s.describeRun(ctx, full, run)
}

func (s *Service) describeRun(ctx context.Context, repo string, run workflowRun) (*CIFailure, error) {
	failure := &CIFailure{
		Repository: repo,
		Branch:     run.HeadBranch,
		Workflow:   run.Name,
		RunID:      run.ID,
		RunNumber:  run.RunNumber,
		Commit:     shortSHA(run.HeadSHA),
		Conclusion: run.Conclusion,
		URL:        run.HTMLURL,
	}
	if !run.UpdatedAt.IsZero() {
		failure.FinishedAt = run.UpdatedAt.UTC().Format(time.RFC3339)
	}

	jobs, err := s.client.listRunJobs(ctx, repo, run.ID)
	if err != nil {
		return nil, err
	}

	for _, job := range jobs {
		if job.Conclusion != "failure" && job.Conclusion != "timed_out" {
			continue
		}
		if len(failure.FailedJobs) >= maxFailedJobs {
			break
		}
		entry := FailedJob{
			Name:       job.Name,
			Conclusion: job.Conclusion,
			FailedStep: failedStep(job),
			URL:        job.HTMLURL,
		}
		// Annotations are cheap and pre-extracted; a job with good ones often
		// needs no log at all. Neither failure is fatal — a partial
		// explanation still beats sending the user back to the browser.
		if annotations, err := s.client.jobAnnotations(ctx, repo, job.ID); err == nil {
			entry.Annotations = summarizeAnnotations(annotations)
		}
		if raw, err := s.client.jobLog(ctx, repo, job.ID); err == nil {
			entry.LogExcerpt = ReduceLog(raw)
		} else {
			entry.LogExcerpt = "log unavailable: " + truncate(err.Error(), 120)
		}
		failure.FailedJobs = append(failure.FailedJobs, entry)
	}

	return failure, nil
}

// RepoStatus is a small snapshot for "how is the repo doing".
type RepoStatus struct {
	Repository    string `json:"repository"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Description   string `json:"description,omitempty"`
	OpenIssues    int    `json:"open_issues"`
	LatestRun     *struct {
		Workflow   string `json:"workflow"`
		Branch     string `json:"branch"`
		Conclusion string `json:"conclusion"`
		Status     string `json:"status"`
		RunID      int64  `json:"run_id"`
		Commit     string `json:"commit"`
	} `json:"latest_run,omitempty"`
}

func (s *Service) RepoStatus(ctx context.Context, repo string) (*RepoStatus, error) {
	full, err := s.client.Authorize(ctx, repo)
	if err != nil {
		return nil, err
	}
	info, err := s.client.getRepo(ctx, full)
	if err != nil {
		return nil, err
	}
	status := &RepoStatus{
		Repository:    info.FullName,
		DefaultBranch: info.DefaultBranch,
		Private:       info.Private,
		Description:   truncate(info.Description, 200),
		OpenIssues:    info.OpenIssues,
	}
	runs, err := s.client.listWorkflowRuns(ctx, full, info.DefaultBranch, "", 1)
	if err == nil && len(runs) > 0 {
		run := runs[0]
		status.LatestRun = &struct {
			Workflow   string `json:"workflow"`
			Branch     string `json:"branch"`
			Conclusion string `json:"conclusion"`
			Status     string `json:"status"`
			RunID      int64  `json:"run_id"`
			Commit     string `json:"commit"`
		}{
			Workflow:   run.Name,
			Branch:     run.HeadBranch,
			Conclusion: run.Conclusion,
			Status:     run.Status,
			RunID:      run.ID,
			Commit:     shortSHA(run.HeadSHA),
		}
	}
	return status, nil
}

// WorkflowRuns lists recent runs on a branch, newest first.
func (s *Service) WorkflowRuns(ctx context.Context, repo, branch string, limit int) (any, error) {
	full, err := s.client.Authorize(ctx, repo)
	if err != nil {
		return nil, err
	}
	runs, err := s.client.listWorkflowRuns(ctx, full, branch, "", limit)
	if err != nil {
		return nil, err
	}
	type entry struct {
		Workflow   string `json:"workflow"`
		RunID      int64  `json:"run_id"`
		Branch     string `json:"branch"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Commit     string `json:"commit"`
		Title      string `json:"title,omitempty"`
	}
	out := make([]entry, 0, len(runs))
	for _, run := range runs {
		out = append(out, entry{
			Workflow:   run.Name,
			RunID:      run.ID,
			Branch:     run.HeadBranch,
			Status:     run.Status,
			Conclusion: run.Conclusion,
			Commit:     shortSHA(run.HeadSHA),
			Title:      truncate(run.DisplayTitle, 120),
		})
	}
	return map[string]any{"repository": full, "runs": out}, nil
}

// WorkflowLogs returns the reduced excerpt for one job, or for every failed job
// in a run when jobName is empty.
func (s *Service) WorkflowLogs(ctx context.Context, repo string, runID int64, jobName string) (any, error) {
	full, err := s.client.Authorize(ctx, repo)
	if err != nil {
		return nil, err
	}
	jobs, err := s.client.listRunJobs(ctx, full, runID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })

	excerpts := make([]FailedJob, 0, maxFailedJobs)
	for _, job := range jobs {
		if jobName != "" && !strings.EqualFold(job.Name, jobName) {
			continue
		}
		if jobName == "" && job.Conclusion != "failure" && job.Conclusion != "timed_out" {
			continue
		}
		if len(excerpts) >= maxFailedJobs {
			break
		}
		entry := FailedJob{Name: job.Name, Conclusion: job.Conclusion, FailedStep: failedStep(job), URL: job.HTMLURL}
		raw, err := s.client.jobLog(ctx, full, job.ID)
		if err != nil {
			return nil, err
		}
		entry.LogExcerpt = ReduceLog(raw)
		excerpts = append(excerpts, entry)
	}
	if len(excerpts) == 0 {
		return nil, fmt.Errorf("no matching job with logs in run %d", runID)
	}
	return map[string]any{"repository": full, "run_id": runID, "jobs": excerpts}, nil
}

func (s *Service) PullRequest(ctx context.Context, repo string, number int) (any, error) {
	full, err := s.client.Authorize(ctx, repo)
	if err != nil {
		return nil, err
	}
	pr, err := s.client.getPullRequest(ctx, full, number)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"repository":    full,
		"number":        pr.Number,
		"title":         pr.Title,
		"state":         pr.State,
		"draft":         pr.Draft,
		"merged":        pr.Merged,
		"author":        pr.User.Login,
		"head":          pr.Head.Ref,
		"base":          pr.Base.Ref,
		"changed_files": pr.ChangedFiles,
		"additions":     pr.Additions,
		"deletions":     pr.Deletions,
		"url":           pr.HTMLURL,
		"body":          truncate(collapseBlankLines(pr.Body), 600),
	}, nil
}

func (s *Service) Commit(ctx context.Context, repo, ref string) (any, error) {
	full, err := s.client.Authorize(ctx, repo)
	if err != nil {
		return nil, err
	}
	c, err := s.client.getCommit(ctx, full, ref)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, 12)
	for i, f := range c.Files {
		if i >= 12 {
			break
		}
		files = append(files, fmt.Sprintf("%s (%s +%d -%d)", f.Filename, f.Status, f.Additions, f.Deletions))
	}
	return map[string]any{
		"repository": full,
		"sha":        shortSHA(c.SHA),
		"message":    truncate(collapseBlankLines(c.Commit.Message), 400),
		"author":     c.Commit.Author.Name,
		"date":       c.Commit.Author.Date.UTC().Format(time.RFC3339),
		"additions":  c.Stats.Additions,
		"deletions":  c.Stats.Deletions,
		"files":      files,
		"url":        c.HTMLURL,
	}, nil
}

func (s *Service) File(ctx context.Context, repo, path, ref string) (any, error) {
	full, err := s.client.Authorize(ctx, repo)
	if err != nil {
		return nil, err
	}
	if err := validateRepoPath(path); err != nil {
		return nil, err
	}
	content, err := s.client.getFile(ctx, full, path, ref)
	if err != nil {
		return nil, err
	}
	truncated := len(content) > MaxExcerptBytes*2
	if truncated {
		content = content[:MaxExcerptBytes*2]
	}
	return map[string]any{
		"repository": full,
		"path":       path,
		"ref":        ref,
		"truncated":  truncated,
		"content":    content,
	}, nil
}

func (s *Service) Diff(ctx context.Context, repo, base, head string) (any, error) {
	full, err := s.client.Authorize(ctx, repo)
	if err != nil {
		return nil, err
	}
	diff, err := s.client.compare(ctx, full, base, head)
	if err != nil {
		return nil, err
	}
	truncated := len(diff) >= MaxDiffBytes
	return map[string]any{
		"repository": full,
		"base":       base,
		"head":       head,
		"truncated":  truncated,
		"diff":       diff,
	}, nil
}

// validateRepoPath rejects paths that try to climb out of the repository. The
// GitHub API would reject most of these itself, but the check belongs on the
// side that decides what to ask for.
func validateRepoPath(path string) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.Contains(path, "\\") {
		return fmt.Errorf("path must be a relative path inside the repository")
	}
	return nil
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// CloneCredentials hands back the plain clone URL for a repository and a
// read-scoped installation token to fetch it with.
//
// The token is for StreamCore's own git process. It is passed to git through an
// askpass helper and never written into the checkout, so nothing running inside
// a Codex worktree can authenticate to GitHub with it.
func (s *Service) CloneCredentials(ctx context.Context, repo string) (string, string, error) {
	full, err := s.client.Authorize(ctx, repo)
	if err != nil {
		return "", "", err
	}
	token, err := s.auth.InstallationToken(ctx, full, ReadPermissions())
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%s/%s.git", githubHost(s.client.baseURL), full), token, nil
}
