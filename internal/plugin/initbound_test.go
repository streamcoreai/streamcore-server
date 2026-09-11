package plugin

import (
	"context"
	"strings"
	"testing"
	"time"
)

// timeout_ms is how long one tool call gets. It used to bound the initialize
// handshake as well, so a plugin that set it honestly low for a fast tool was
// given the same budget to exec, bring up its runtime and read its first line
// of stdin — and on a loaded machine that was not enough, and the plugin was
// skipped at startup as if it were broken.
//
// This plugin answers initialize after a delay that is enormous next to its
// timeout_ms and trivial next to the startup bound. It can only start if the
// two are separate.
func TestStartIsNotBoundedByThePerCallTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a process")
	}

	p := NewExternalPlugin(Manifest{
		Name: "slow-to-wake",
		// Read the initialize request, take our time, answer it (the first
		// request id is 1), then hold stdin open until the server closes it.
		Exec: []string{"/bin/sh", "-c",
			`read -r line; sleep 0.2; printf '{"jsonrpc":"2.0","id":1,"result":"ok"}\n'; cat >/dev/null`},
		TimeoutMs: 1,
	}, t.TempDir(), "")
	t.Cleanup(p.Stop)

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v\n"+
			"a 1 ms per-call timeout must not be the handshake's budget; "+
			"initialize should be bounded by initTimeout instead", err)
	}
	if !p.running.Load() {
		t.Fatal("Start returned nil but the plugin is not marked running")
	}

	// Sanity: the per-call budget is still the manifest's, so a tool call
	// against this plugin would time out at 1 ms. That is the point — the
	// two bounds are independent.
	if p.timeout != time.Millisecond {
		t.Errorf("per-call timeout = %v, want 1ms from the manifest", p.timeout)
	}
	if p.initTimeout != defaultInitTimeout {
		t.Errorf("initTimeout = %v, want defaultInitTimeout %v", p.initTimeout, defaultInitTimeout)
	}
}

// The timeout error used to print p.timeout whatever context had expired, so
// the handshake failing at its own bound reported the per-call one: "timed out
// after 30s" on a test that took half a second. Whoever reads that adjusts the
// wrong setting.
func TestHandshakeTimeoutNamesItsOwnBound(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a process")
	}

	p := NewExternalPlugin(Manifest{
		Name: "mute",
		Exec: []string{"/bin/sh", "-c", `cat >/dev/null`},
	}, t.TempDir(), "")
	p.initTimeout = 300 * time.Millisecond
	t.Cleanup(p.Stop)

	err := p.Start(context.Background())
	if err == nil {
		t.Fatal("a plugin that never answers initialize should not start")
	}
	if !strings.Contains(err.Error(), "timed out after 300ms") {
		t.Errorf("initialize error should name the handshake bound it hit, got: %v", err)
	}
}

func TestCallTimeoutNamesTheContextBound(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a process")
	}

	// Answers initialize (the first request id is 1), then swallows
	// everything, so a call runs to whatever bound its context carries.
	p := NewExternalPlugin(Manifest{
		Name: "deaf-after-hello",
		Exec: []string{"/bin/sh", "-c",
			`read -r line; printf '{"jsonrpc":"2.0","id":1,"result":"ok"}\n'; cat >/dev/null`},
	}, t.TempDir(), "")
	t.Cleanup(p.Stop)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.call(ctx, JSONRPCRequest{Method: "execute"})
	if err == nil || !strings.Contains(err.Error(), "timed out after 50ms") {
		t.Errorf("call error should name the 50ms bound it hit, not the manifest default, got: %v", err)
	}

	// Cancellation is not a timeout and must not be reported as one.
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = p.call(ctx, JSONRPCRequest{Method: "execute"})
	if err == nil || strings.Contains(err.Error(), "timed out") {
		t.Errorf("a cancelled call must not claim to have timed out, got: %v", err)
	}
}
