package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// tokenServer stands in for GitHub's installation-token endpoint. It records
// what was asked for, which is the interesting half of scoping.
type tokenServer struct {
	*httptest.Server
	calls     atomic.Int32
	lastAuth  atomic.Value // string
	lastBody  atomic.Value // map[string]any
	expiresIn time.Duration
	status    atomic.Int32
}

func newTokenServer(t *testing.T) *tokenServer {
	t.Helper()
	fake := &tokenServer{expiresIn: time.Hour}
	fake.status.Store(int32(http.StatusCreated))

	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/access_tokens") {
			http.NotFound(w, r)
			return
		}
		fake.calls.Add(1)
		fake.lastAuth.Store(r.Header.Get("Authorization"))

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		fake.lastBody.Store(body)

		if status := int(fake.status.Load()); status != http.StatusCreated {
			w.WriteHeader(status)
			w.Write([]byte(`{"message":"nope"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_test_token",
			"expires_at": time.Now().Add(fake.expiresIn).Format(time.RFC3339),
		})
	}))
	t.Cleanup(fake.Close)
	return fake
}

func (f *tokenServer) body() map[string]any {
	body, _ := f.lastBody.Load().(map[string]any)
	return body
}

func writeKey(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "app.pem")
	encoded := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path, key
}

func newTestAuth(t *testing.T, server *tokenServer) (*AppAuth, *rsa.PrivateKey) {
	t.Helper()
	path, key := writeKey(t)
	auth, err := NewAppAuth("Iv1.testclientid", "12345", path, server.URL)
	if err != nil {
		t.Fatalf("new auth: %v", err)
	}
	return auth, key
}

func TestAppJWTClaims(t *testing.T) {
	server := newTokenServer(t)
	auth, key := newTestAuth(t, server)

	signed, err := auth.appJWT()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	token, err := jwt.Parse(signed, func(*jwt.Token) (any, error) { return &key.PublicKey, nil })
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if token.Method.Alg() != "RS256" {
		t.Fatalf("algorithm is %s, GitHub requires RS256", token.Method.Alg())
	}
	claims := token.Claims.(jwt.MapClaims)
	if claims["iss"] != "Iv1.testclientid" {
		t.Fatalf("iss is %v", claims["iss"])
	}

	issued := time.Unix(int64(claims["iat"].(float64)), 0)
	expires := time.Unix(int64(claims["exp"].(float64)), 0)
	if !issued.Before(time.Now()) {
		t.Fatal("iat is not backdated; GitHub recommends 60s in the past for clock drift")
	}
	if time.Until(expires) > 10*time.Minute {
		t.Fatalf("exp is %v away; GitHub's ceiling is 10 minutes", time.Until(expires))
	}
}

func TestInstallationTokenIsScoped(t *testing.T) {
	server := newTokenServer(t)
	auth, _ := newTestAuth(t, server)

	token, err := auth.InstallationToken(context.Background(), "streamcoreai/streamcore-server", ReadPermissions())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if token != "ghs_test_token" {
		t.Fatalf("token is %q", token)
	}
	if auth := server.lastAuth.Load().(string); !strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("installation token request did not present the app JWT: %q", auth)
	}

	body := server.body()
	repos, _ := body["repositories"].([]any)
	if len(repos) != 1 || repos[0] != "streamcore-server" {
		t.Fatalf("token was not scoped to one repository: %v", body["repositories"])
	}
	perms, _ := body["permissions"].(map[string]any)
	if perms["actions"] != "read" || perms["contents"] != "read" || perms["pull_requests"] != "read" {
		t.Fatalf("read token permissions are wrong: %v", perms)
	}
	for name, level := range perms {
		if level == "write" {
			t.Fatalf("the read token requested write on %q", name)
		}
	}
	for _, forbidden := range []string{"administration", "secrets", "members", "workflows"} {
		if _, present := perms[forbidden]; present {
			t.Fatalf("the read token requested %q", forbidden)
		}
	}
}

func TestWritePermissionsAreSeparate(t *testing.T) {
	server := newTokenServer(t)
	auth, _ := newTestAuth(t, server)
	ctx := context.Background()

	if _, err := auth.InstallationToken(ctx, "a/b", ReadPermissions()); err != nil {
		t.Fatalf("read mint: %v", err)
	}
	if _, err := auth.InstallationToken(ctx, "a/b", WritePermissions()); err != nil {
		t.Fatalf("write mint: %v", err)
	}
	if server.calls.Load() != 2 {
		t.Fatalf("read and write tokens shared a cache entry (%d mints)", server.calls.Load())
	}

	perms := server.body()["permissions"].(map[string]any)
	if perms["contents"] != "write" || perms["pull_requests"] != "write" {
		t.Fatalf("write token permissions are wrong: %v", perms)
	}
	if _, present := perms["actions"]; present {
		t.Fatalf("the write token asked for actions, which PR creation does not need")
	}
	if _, present := perms["workflows"]; present {
		t.Fatal("the write token asked for workflows; this phase does not modify workflow files")
	}
}

func TestInstallationTokenIsCachedThenRefreshed(t *testing.T) {
	server := newTokenServer(t)
	auth, _ := newTestAuth(t, server)
	ctx := context.Background()

	if _, err := auth.InstallationToken(ctx, "a/b", ReadPermissions()); err != nil {
		t.Fatalf("first mint: %v", err)
	}
	if _, err := auth.InstallationToken(ctx, "a/b", ReadPermissions()); err != nil {
		t.Fatalf("second mint: %v", err)
	}
	if server.calls.Load() != 1 {
		t.Fatalf("the cached token was not reused (%d mints)", server.calls.Load())
	}

	// Inside the refresh margin, the token must be replaced rather than
	// presented to GitHub moments before it expires.
	auth.now = func() time.Time { return time.Now().Add(55 * time.Minute) }
	if _, err := auth.InstallationToken(ctx, "a/b", ReadPermissions()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if server.calls.Load() != 2 {
		t.Fatalf("a token near expiry was reused (%d mints)", server.calls.Load())
	}
}

func TestMissingPrivateKey(t *testing.T) {
	_, err := NewAppAuth("Iv1.x", "1", filepath.Join(t.TempDir(), "absent.pem"), "")
	if err == nil {
		t.Fatal("a missing private key was accepted")
	}
	if !strings.Contains(err.Error(), "private key") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestFailedAuthIsExplained(t *testing.T) {
	server := newTokenServer(t)
	server.status.Store(http.StatusUnauthorized)
	auth, _ := newTestAuth(t, server)

	_, err := auth.InstallationToken(context.Background(), "a/b", ReadPermissions())
	if err == nil {
		t.Fatal("a 401 was treated as success")
	}
	if !strings.Contains(err.Error(), "app_id") {
		t.Fatalf("the 401 message does not say what to check: %v", err)
	}
}

// The private key must never reach a log line, a tool result, or an error the
// model can read back to the user.
func TestErrorsCarryNoKeyMaterial(t *testing.T) {
	server := newTokenServer(t)
	server.status.Store(http.StatusUnprocessableEntity)
	path, key := writeKey(t)
	auth, err := NewAppAuth("Iv1.x", "1", path, server.URL)
	if err != nil {
		t.Fatalf("new auth: %v", err)
	}

	_, err = auth.InstallationToken(context.Background(), "a/b", ReadPermissions())
	if err == nil {
		t.Fatal("expected an error")
	}
	pemBytes := string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
	if strings.Contains(err.Error(), "PRIVATE KEY") || strings.Contains(err.Error(), pemBytes[:64]) {
		t.Fatal("the private key appeared in an error message")
	}
	if strings.Contains(err.Error(), "ghs_") {
		t.Fatal("a token appeared in an error message")
	}
}

func TestBadRepositoryName(t *testing.T) {
	server := newTokenServer(t)
	auth, _ := newTestAuth(t, server)

	for _, repo := range []string{"", "nope", "a/b/c", "/b", "a/"} {
		if _, err := auth.InstallationToken(context.Background(), repo, ReadPermissions()); err == nil {
			t.Fatalf("%q was accepted as a repository", repo)
		}
	}
	if server.calls.Load() != 0 {
		t.Fatal("a malformed repository reached the network")
	}
}
