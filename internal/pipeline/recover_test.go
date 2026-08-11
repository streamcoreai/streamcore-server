package pipeline

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

// silenceLogs keeps the recovered panic's stack trace out of the test output
// and returns it for assertions.
func silenceLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// A panic in a machinery goroutine must end this pipeline — and only this
// pipeline. Before recoverPanic existed, this test would crash the whole
// test binary, which is exactly the production failure it prevents.
func TestRecoverPanicCancelsOnlyItsOwnPipeline(t *testing.T) {
	logs := silenceLogs(t)

	ctx, cancel := context.WithCancel(context.Background())
	p := &Pipeline{ctx: ctx, cancel: cancel}

	otherCtx, otherCancel := context.WithCancel(context.Background())
	defer otherCancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer p.recoverPanic("testGoroutine")
		panic("boom")
	}()
	<-done

	if ctx.Err() == nil {
		t.Fatal("panicking pipeline was not cancelled")
	}
	if otherCtx.Err() != nil {
		t.Fatal("a panic in one pipeline reached another")
	}
	if !strings.Contains(logs.String(), "testGoroutine") {
		t.Fatalf("panic was not logged with its goroutine name: %q", logs.String())
	}
}

// Best-effort goroutines recover without ending the call.
func TestRecoverKeepAliveLeavesThePipelineRunning(t *testing.T) {
	silenceLogs(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Pipeline{ctx: ctx, cancel: cancel}

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer p.recoverKeepAlive("backgroundWork")
		panic("boom")
	}()
	<-done

	if ctx.Err() != nil {
		t.Fatal("a best-effort panic cancelled the call")
	}
}
