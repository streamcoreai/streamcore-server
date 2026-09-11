package plugin

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A plugin can leave a child behind that inherited its stdout and stderr.
// exec.Cmd.Wait does not only reap the process — stderr here is a logWriter
// rather than an *os.File, so exec gives it a pipe and a copy goroutine, and
// Wait blocks until that goroutine sees EOF. The grandchild holds the write end
// open, so Wait never returns.
//
// Shutdown used to wait on that unbounded, after the kill, which is the one
// point where there is nothing further to try. A hung Stop takes the whole
// shutdown with it, and in the test suite that reads as a ten-minute hang
// rather than a failure.
func TestStopGivesUpOnAPluginThatLeftAChildHoldingItsPipes(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}

	// Spawn a grandchild that inherits stderr and outlives the shell, then
	// ignore every signal so only the kill ends the shell itself.
	p := NewExternalPlugin(Manifest{
		Name: "leaves-a-child",
		Exec: []string{"/bin/sh", "-c", `sleep 300 & trap '' TERM INT; while :; do sleep 1; done`},
	}, t.TempDir(), "")
	// The child never answers initialize, and waiting the default thirty
	// seconds to find that out would be the whole runtime of this test.
	p.initTimeout = 500 * time.Millisecond

	if err := p.Start(context.Background()); err != nil {
		// The child never speaks JSON-RPC, so a handshake timeout is expected
		// and irrelevant: the process is running, which is all this needs.
		t.Logf("start returned %v (expected: the child does not speak the protocol)", err)
	}

	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()

	// stopGrace at each of three stages, plus room for a slow machine.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop never returned: it is waiting on a pipe a grandchild still holds")
	}

	// Returning is the floor. WaitDelay should also have let Wait finish, so
	// the process is reaped and no goroutine is left holding the pipe.
	if p.cmd.ProcessState == nil {
		t.Error("Stop returned but Wait did not: the pipe drain is still blocked, so a goroutine leaked")
	}
}

// Shutdown from outside can land while restart is honouring the same shutdown
// from inside, so two goroutines reach Stop for one process. Each used to
// spawn its own cmd.Wait, which exec forbids on a single Cmd and the race
// detector reports. Run under -race, which is how CI runs it.
func TestStopIsSafeToCallConcurrently(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a process")
	}

	// Answers initialize, then holds stdin open until it is closed.
	p := NewExternalPlugin(Manifest{
		Name: "shared",
		Exec: []string{"/bin/sh", "-c",
			`read -r line; printf '{"jsonrpc":"2.0","id":1,"result":"ok"}\n'; cat >/dev/null`},
	}, t.TempDir(), "")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	const callers = 4
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			p.Stop()
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent Stop calls did not all return")
	}

	// Every caller returned to the same dead process.
	if p.cmd.ProcessState == nil {
		t.Error("Stop returned but the process was never reaped")
	}
	if p.running.Load() {
		t.Error("plugin still marked running after Stop")
	}
}
