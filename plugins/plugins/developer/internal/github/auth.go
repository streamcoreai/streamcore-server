// Package github is StreamCore's GitHub App integration: short-lived
// credentials, a small read-only investigation tool set, and a
// confirmation-gated pull request path.
//
// Nothing here is specific to a device or a display. The tools return ordinary
// structured results; the agent turns them into speech and the existing
// display-projector turns that into a card.
package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwtLifetime is well inside GitHub's 10-minute ceiling. The JWT is only ever
// used to mint an installation token, so a short life costs nothing.
const jwtLifetime = 9 * time.Minute

// jwtBackdate protects against clock drift between this host and GitHub, as
// the API docs recommend.
const jwtBackdate = 60 * time.Second

// Installation tokens last an hour. Retiring them early means a token is never
// presented to GitHub within a minute of expiry, which is where clock skew
// turns a valid request into a confusing 401.
const tokenRefreshMargin = 10 * time.Minute

// Permissions is the permission set requested when minting an installation
// token. Keys are GitHub's fine-grained permission names, values are "read" or
// "write".
type Permissions map[string]string

// ReadPermissions is what CI investigation actually needs, verified against the
// endpoints this package calls:
//
//	actions:read        list workflow runs, list jobs, download job logs
//	contents:read       read a file, read a commit, compare refs
//	pull_requests:read  read a pull request
//	checks:read         list check-run annotations
//	metadata:read       mandatory for every fine-grained token
//
// Deliberately absent: workflows, administration, secrets, members.
func ReadPermissions() Permissions {
	return Permissions{
		"actions":       "read",
		"contents":      "read",
		"pull_requests": "read",
		"checks":        "read",
		"metadata":      "read",
	}
}

// WritePermissions is the strictly larger set needed to push a branch and open
// a pull request, and nothing else. It is minted only for that operation and is
// never handed to Codex.
func WritePermissions() Permissions {
	return Permissions{
		"contents":      "write",
		"pull_requests": "write",
		"metadata":      "read",
	}
}

func (p Permissions) key() string {
	names := make([]string, 0, len(p))
	for name := range p {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+p[name])
	}
	return strings.Join(parts, ",")
}

// TokenProvider hands out installation access tokens scoped to one repository
// and one permission set. The GitHub tools depend on this interface rather than
// the concrete signer, so tests never need a private key.
type TokenProvider interface {
	InstallationToken(ctx context.Context, repo string, perms Permissions) (string, error)
}

// AppAuth turns a GitHub App private key into short-lived, repository-scoped
// installation access tokens.
//
// The private key is read once at startup and never leaves this struct. Nothing
// it produces is logged, returned in tool output, or passed to Codex.
type AppAuth struct {
	issuer         string // app ID or client ID, used as the JWT iss claim
	installationID string
	key            *rsa.PrivateKey
	baseURL        string
	http           *http.Client

	mu    sync.Mutex
	cache map[string]cachedToken

	now func() time.Time // test seam
}

type cachedToken struct {
	token   string
	expires time.Time
}

// NewAppAuth loads the App private key from disk. The file is read into memory
// and parsed; the path is retained only for error messages.
func NewAppAuth(issuer, installationID, privateKeyPath, baseURL string) (*AppAuth, error) {
	if strings.TrimSpace(issuer) == "" {
		return nil, fmt.Errorf("github.app_id is required")
	}
	if strings.TrimSpace(installationID) == "" {
		return nil, fmt.Errorf("github.installation_id is required")
	}
	pem, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read github app private key: %w", err)
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pem)
	if err != nil {
		return nil, fmt.Errorf("parse github app private key %s: %w", privateKeyPath, err)
	}
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &AppAuth{
		issuer:         issuer,
		installationID: installationID,
		key:            key,
		baseURL:        strings.TrimRight(baseURL, "/"),
		http:           &http.Client{Timeout: 20 * time.Second},
		cache:          make(map[string]cachedToken),
		now:            time.Now,
	}, nil
}

// appJWT mints a fresh App JWT. These are cheap and short-lived, so they are
// never cached: one is signed per installation-token exchange and discarded.
func (a *AppAuth) appJWT() (string, error) {
	now := a.now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now.Add(-jwtBackdate)),
		ExpiresAt: jwt.NewNumericDate(now.Add(jwtLifetime)),
		Issuer:    a.issuer,
	})
	signed, err := token.SignedString(a.key)
	if err != nil {
		return "", fmt.Errorf("sign github app jwt: %w", err)
	}
	return signed, nil
}

// InstallationToken returns a token scoped to one repository and one permission
// set, minting a new one when the cached token is missing or close to expiry.
//
// Cache entries are keyed by repository and permission set, so the read path
// can never accidentally be served the write token.
func (a *AppAuth) InstallationToken(ctx context.Context, repo string, perms Permissions) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	cacheKey := owner + "/" + name + "|" + perms.key()

	a.mu.Lock()
	if entry, ok := a.cache[cacheKey]; ok && a.now().Before(entry.expires.Add(-tokenRefreshMargin)) {
		a.mu.Unlock()
		return entry.token, nil
	}
	a.mu.Unlock()

	token, expires, err := a.mintInstallationToken(ctx, name, perms)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	a.cache[cacheKey] = cachedToken{token: token, expires: expires}
	a.mu.Unlock()
	return token, nil
}

// Forget drops every cached token. Called when GitHub rejects a token so the
// next call re-mints rather than replaying a credential GitHub has revoked.
func (a *AppAuth) Forget() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache = make(map[string]cachedToken)
}

func (a *AppAuth) mintInstallationToken(ctx context.Context, repoName string, perms Permissions) (string, time.Time, error) {
	appToken, err := a.appJWT()
	if err != nil {
		return "", time.Time{}, err
	}

	body, err := json.Marshal(struct {
		Repositories []string    `json:"repositories"`
		Permissions  Permissions `json:"permissions"`
	}{
		Repositories: []string{repoName},
		Permissions:  perms,
	})
	if err != nil {
		return "", time.Time{}, err
	}

	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", a.baseURL, a.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+appToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("github installation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", time.Time{}, fmt.Errorf("github installation token: %s", describeAuthFailure(resp.StatusCode, string(snippet)))
	}

	var payload struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", time.Time{}, fmt.Errorf("github installation token: decode response: %w", err)
	}
	if payload.Token == "" {
		return "", time.Time{}, fmt.Errorf("github installation token: response carried no token")
	}
	if payload.ExpiresAt.IsZero() {
		payload.ExpiresAt = a.now().Add(time.Hour)
	}
	return payload.Token, payload.ExpiresAt, nil
}

// describeAuthFailure keeps GitHub's status and message but never echoes a
// request that might carry credentials.
func describeAuthFailure(status int, body string) string {
	var payload struct {
		Message string `json:"message"`
	}
	message := strings.TrimSpace(body)
	if json.Unmarshal([]byte(body), &payload) == nil && payload.Message != "" {
		message = payload.Message
	}
	switch status {
	case http.StatusUnauthorized:
		return "401 unauthorized — check github.app_id and the private key at github.private_key_path"
	case http.StatusNotFound:
		return "404 not found — check github.installation_id, and that the app is still installed"
	case http.StatusUnprocessableEntity:
		return "422 — the app is not installed on that repository, or lacks a requested permission: " + truncate(message, 160)
	}
	return fmt.Sprintf("%d: %s", status, truncate(message, 160))
}

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

func splitRepo(repo string) (string, string, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("repository must be owner/name, got %q", repo)
	}
	return owner, name, nil
}
