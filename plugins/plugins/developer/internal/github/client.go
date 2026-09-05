package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const apiVersion = "2022-11-28"

// maxResponseBytes bounds any single API response held in memory. Log bodies
// go through a separate, larger limit in logs.go.
const maxResponseBytes = 4 << 20

// Client is a small GitHub REST client that mints the right token for each
// call. It holds no long-lived credential of its own.
type Client struct {
	tokens  TokenProvider
	baseURL string
	http    *http.Client

	// allowed is the operator's repository allowlist, lowercased. A repository
	// absent from it is rejected before any network call.
	allowed map[string]bool

	// installed caches which allowlisted repositories the App installation can
	// actually reach, so the second half of the two-condition check does not
	// cost a round trip per tool call.
	mu        sync.Mutex
	installed map[string]bool
}

// NewClient builds a client over a token provider and an allowlist.
func NewClient(tokens TokenProvider, repositories []string, baseURL string) *Client {
	allowed := make(map[string]bool, len(repositories))
	for _, repo := range repositories {
		repo = strings.ToLower(strings.TrimSpace(repo))
		if repo != "" {
			allowed[repo] = true
		}
	}
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Client{
		tokens:    tokens,
		baseURL:   strings.TrimRight(baseURL, "/"),
		http:      &http.Client{Timeout: 30 * time.Second},
		allowed:   allowed,
		installed: make(map[string]bool),
	}
}

// Repositories returns the allowlist in a stable order, for logging and for the
// error message a caller sees when they name something else.
func (c *Client) Repositories() []string {
	repos := make([]string, 0, len(c.allowed))
	for repo := range c.allowed {
		repos = append(repos, repo)
	}
	return repos
}

// Authorize enforces both conditions before any repository is touched: the
// operator listed it, and the App installation can actually reach it. Either
// one alone is not enough — configuration is a statement of intent, the
// installation is the actual grant.
func (c *Client) Authorize(ctx context.Context, repo string) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	full := owner + "/" + name
	if !c.allowed[strings.ToLower(full)] {
		return "", fmt.Errorf("repository %s is not in the StreamCore allowlist (allowed: %s)",
			full, strings.Join(c.Repositories(), ", "))
	}

	c.mu.Lock()
	known, cached := c.installed[strings.ToLower(full)]
	c.mu.Unlock()
	if cached {
		if !known {
			return "", fmt.Errorf("the StreamCore GitHub App is not installed on %s", full)
		}
		return full, nil
	}

	// Minting a read token for the repository is itself the access check:
	// GitHub refuses to scope a token to a repository the installation cannot
	// reach, so a successful mint proves the grant.
	if _, err := c.tokens.InstallationToken(ctx, full, ReadPermissions()); err != nil {
		c.mu.Lock()
		c.installed[strings.ToLower(full)] = false
		c.mu.Unlock()
		return "", fmt.Errorf("the StreamCore GitHub App cannot access %s: %w", full, err)
	}
	c.mu.Lock()
	c.installed[strings.ToLower(full)] = true
	c.mu.Unlock()
	return full, nil
}

// ForgetInstallation drops the cached access decision for a repository so the
// next call re-checks. Called when GitHub returns 401/404 mid-flight.
func (c *Client) ForgetInstallation(repo string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.installed, strings.ToLower(repo))
}

type request struct {
	method string
	path   string
	query  url.Values
	body   any
	// accept overrides the default JSON media type.
	accept string
	// noRedirect returns the 302 Location instead of following it, for the
	// log-download endpoints that hand out a short-lived signed URL.
	noRedirect bool
	// raw reads the body as text rather than decoding JSON, for the diff and
	// patch media types.
	raw bool
	// rawLimit caps a raw body. Zero uses maxResponseBytes.
	rawLimit int64
}

// do performs one authenticated API call with a token scoped to repo and perms.
func (c *Client) do(ctx context.Context, repo string, perms Permissions, req request, out any) error {
	token, err := c.tokens.InstallationToken(ctx, repo, perms)
	if err != nil {
		return err
	}

	endpoint := c.baseURL + req.path
	if len(req.query) > 0 {
		endpoint += "?" + req.query.Encode()
	}

	var body io.Reader
	if req.body != nil {
		encoded, err := json.Marshal(req.body)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(encoded))
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.method, endpoint, body)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	accept := req.accept
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	httpReq.Header.Set("Accept", accept)
	httpReq.Header.Set("X-GitHub-Api-Version", apiVersion)
	if req.body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	client := c.http
	if req.noRedirect {
		noFollow := *c.http
		noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &noFollow
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("github %s %s: %w", req.method, req.path, err)
	}
	defer resp.Body.Close()

	if req.noRedirect && (resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusTemporaryRedirect) {
		if location, ok := out.(*string); ok {
			*location = resp.Header.Get("Location")
			return nil
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
			c.ForgetInstallation(repo)
		}
		return fmt.Errorf("github %s %s: %s", req.method, req.path, describeAPIFailure(resp, string(snippet)))
	}

	if out == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return nil
	}
	if req.raw {
		target, ok := out.(*string)
		if !ok {
			return fmt.Errorf("raw request needs a *string destination")
		}
		limit := req.rawLimit
		if limit <= 0 {
			limit = maxResponseBytes
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		if err != nil {
			return err
		}
		*target = string(data)
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out)
}

// fetchURL downloads a pre-signed URL handed back by a redirecting endpoint.
// It is deliberately unauthenticated: the signature is already in the URL, and
// attaching the installation token would leak it to storage hosts.
func (c *Client) fetchURL(ctx context.Context, rawURL string, limit int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("download log: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func describeAPIFailure(resp *http.Response, body string) string {
	var payload struct {
		Message string `json:"message"`
	}
	message := strings.TrimSpace(body)
	if json.Unmarshal([]byte(body), &payload) == nil && payload.Message != "" {
		message = payload.Message
	}
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return "403 rate limited — retry after the limit resets"
	}
	return fmt.Sprintf("%d: %s", resp.StatusCode, truncate(message, 200))
}
