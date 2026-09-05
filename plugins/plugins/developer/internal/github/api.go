package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The REST shapes below carry only the fields StreamCore actually reads.
// Decoding into a narrow struct is the first stage of keeping tool output
// bounded: the rest of GitHub's payload never enters the process.

type workflowRun struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	DisplayTitle string    `json:"display_title"`
	HeadBranch   string    `json:"head_branch"`
	HeadSHA      string    `json:"head_sha"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	Event        string    `json:"event"`
	HTMLURL      string    `json:"html_url"`
	RunNumber    int       `json:"run_number"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	CheckSuiteID int64     `json:"check_suite_id"`
}

type workflowRunList struct {
	TotalCount int           `json:"total_count"`
	Runs       []workflowRun `json:"workflow_runs"`
}

type workflowJob struct {
	ID          int64  `json:"id"`
	RunID       int64  `json:"run_id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	HTMLURL     string `json:"html_url"`
	CheckRunURL string `json:"check_run_url"`
	Steps       []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Number     int    `json:"number"`
	} `json:"steps"`
}

type workflowJobList struct {
	TotalCount int           `json:"total_count"`
	Jobs       []workflowJob `json:"jobs"`
}

type checkAnnotation struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	AnnotationLevel string `json:"annotation_level"`
	Title           string `json:"title"`
	Message         string `json:"message"`
}

// listWorkflowRuns returns the most recent runs, newest first. branch and
// status are optional filters.
func (c *Client) listWorkflowRuns(ctx context.Context, repo, branch, status string, limit int) ([]workflowRun, error) {
	query := url.Values{}
	query.Set("per_page", strconv.Itoa(clampInt(limit, 1, 50)))
	if branch != "" {
		query.Set("branch", branch)
	}
	if status != "" {
		query.Set("status", status)
	}
	var out workflowRunList
	err := c.do(ctx, repo, ReadPermissions(), request{
		method: "GET",
		path:   "/repos/" + repo + "/actions/runs",
		query:  query,
	}, &out)
	return out.Runs, err
}

func (c *Client) getWorkflowRun(ctx context.Context, repo string, runID int64) (workflowRun, error) {
	var out workflowRun
	err := c.do(ctx, repo, ReadPermissions(), request{
		method: "GET",
		path:   fmt.Sprintf("/repos/%s/actions/runs/%d", repo, runID),
	}, &out)
	return out, err
}

func (c *Client) listRunJobs(ctx context.Context, repo string, runID int64) ([]workflowJob, error) {
	query := url.Values{}
	query.Set("per_page", "50")
	query.Set("filter", "latest")
	var out workflowJobList
	err := c.do(ctx, repo, ReadPermissions(), request{
		method: "GET",
		path:   fmt.Sprintf("/repos/%s/actions/runs/%d/jobs", repo, runID),
		query:  query,
	}, &out)
	return out.Jobs, err
}

// jobLog downloads one job's plain-text log. GitHub answers with a 302 to a
// short-lived signed URL rather than the bytes themselves.
func (c *Client) jobLog(ctx context.Context, repo string, jobID int64) (string, error) {
	var location string
	// GitHub normally answers with a 302 to a signed URL, which do() surfaces
	// as the Location header. Reading the body as text covers the case where it
	// serves the log inline instead — JSON decoding would fail on plain text.
	err := c.do(ctx, repo, ReadPermissions(), request{
		method:     "GET",
		path:       fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", repo, jobID),
		noRedirect: true,
		raw:        true,
		rawLimit:   maxRawLogBytes,
	}, &location)
	if err != nil {
		return "", err
	}
	if location == "" {
		return "", fmt.Errorf("github returned no log for job %d", jobID)
	}
	if !strings.HasPrefix(location, "http") {
		return location, nil
	}
	return c.fetchURL(ctx, location, maxRawLogBytes)
}

// jobAnnotations reads the check-run annotations attached to a job. These are
// the compiler and test-runner messages GitHub already extracted, so they are
// worth more per byte than the raw log.
func (c *Client) jobAnnotations(ctx context.Context, repo string, jobID int64) ([]checkAnnotation, error) {
	query := url.Values{}
	query.Set("per_page", "20")
	var out []checkAnnotation
	err := c.do(ctx, repo, ReadPermissions(), request{
		method: "GET",
		path:   fmt.Sprintf("/repos/%s/check-runs/%d/annotations", repo, jobID),
		query:  query,
	}, &out)
	return out, err
}

type pullRequest struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
	Merged  bool   `json:"merged"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Additions    int `json:"additions"`
	Deletions    int `json:"deletions"`
	ChangedFiles int `json:"changed_files"`
}

func (c *Client) getPullRequest(ctx context.Context, repo string, number int) (pullRequest, error) {
	var out pullRequest
	err := c.do(ctx, repo, ReadPermissions(), request{
		method: "GET",
		path:   fmt.Sprintf("/repos/%s/pulls/%d", repo, number),
	}, &out)
	return out, err
}

type commit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	HTMLURL string `json:"html_url"`
	Stats   struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
		Total     int `json:"total"`
	} `json:"stats"`
	Files []struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	} `json:"files"`
}

func (c *Client) getCommit(ctx context.Context, repo, ref string) (commit, error) {
	var out commit
	err := c.do(ctx, repo, ReadPermissions(), request{
		method: "GET",
		path:   "/repos/" + repo + "/commits/" + url.PathEscape(ref),
	}, &out)
	return out, err
}

type repoStatus struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Description   string `json:"description"`
	OpenIssues    int    `json:"open_issues_count"`
	PushedAt      string `json:"pushed_at"`
}

func (c *Client) getRepo(ctx context.Context, repo string) (repoStatus, error) {
	var out repoStatus
	err := c.do(ctx, repo, ReadPermissions(), request{
		method: "GET",
		path:   "/repos/" + repo,
	}, &out)
	return out, err
}

// getFile reads one file at a ref. Binary and oversized files are refused
// rather than fed to the model as base64 noise.
func (c *Client) getFile(ctx context.Context, repo, path, ref string) (string, error) {
	query := url.Values{}
	if ref != "" {
		query.Set("ref", ref)
	}
	var out struct {
		Type     string `json:"type"`
		Size     int    `json:"size"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	err := c.do(ctx, repo, ReadPermissions(), request{
		method: "GET",
		path:   "/repos/" + repo + "/contents/" + path,
		query:  query,
	}, &out)
	if err != nil {
		return "", err
	}
	if out.Type != "file" {
		return "", fmt.Errorf("%s is a %s, not a file", path, out.Type)
	}
	if out.Encoding != "base64" {
		return "", fmt.Errorf("%s came back %s-encoded", path, out.Encoding)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", path, err)
	}
	return string(decoded), nil
}

// compare returns the unified diff between two refs, capped so a large branch
// cannot flood the model's context.
func (c *Client) compare(ctx context.Context, repo, base, head string) (string, error) {
	var raw string
	err := c.do(ctx, repo, ReadPermissions(), request{
		method:   "GET",
		path:     "/repos/" + repo + "/compare/" + url.PathEscape(base) + "..." + url.PathEscape(head),
		accept:   "application/vnd.github.v3.diff",
		raw:      true,
		rawLimit: MaxDiffBytes,
	}, &raw)
	return raw, err
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
