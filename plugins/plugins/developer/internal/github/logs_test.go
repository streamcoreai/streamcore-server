package github

import (
	"fmt"
	"strings"
	"testing"
)

func TestReduceLogKeepsTheFailure(t *testing.T) {
	var raw strings.Builder
	for i := range 4000 {
		fmt.Fprintf(&raw, "2026-01-01T00:00:%02d.1234567Z go: downloading github.com/pkg/thing v1.%d.0\n", i%60, i)
	}
	raw.WriteString("2026-01-01T00:01:00.1234567Z --- FAIL: TestDisplayCardVersion (0.00s)\n")
	raw.WriteString("2026-01-01T00:01:00.1234567Z     display_card_test.go:42: expected version 1, received version 2\n")
	raw.WriteString("2026-01-01T00:01:00.1234567Z FAIL\tgithub.com/streamcoreai/streamcore-server/internal/displayprojector\t0.4s\n")
	raw.WriteString("2026-01-01T00:01:01.1234567Z ##[error]Process completed with exit code 1.\n")

	excerpt := ReduceLog(raw.String())

	for _, want := range []string{"TestDisplayCardVersion", "display_card_test.go:42", "expected version 1", "exit code 1"} {
		if !strings.Contains(excerpt, want) {
			t.Fatalf("the excerpt lost %q:\n%s", want, excerpt)
		}
	}
	if strings.Contains(excerpt, "downloading") {
		t.Fatalf("dependency download noise survived:\n%s", excerpt)
	}
	if strings.Contains(excerpt, "2026-01-01T00:01:00") {
		t.Fatalf("log timestamps survived:\n%s", excerpt)
	}
}

func TestReduceLogRespectsLimits(t *testing.T) {
	var raw strings.Builder
	for i := range 5000 {
		fmt.Fprintf(&raw, "panic: goroutine %d exploded in a way nobody predicted or planned for\n", i)
	}
	excerpt := ReduceLog(raw.String())

	if len(excerpt) > MaxExcerptBytes {
		t.Fatalf("excerpt is %d bytes, over the %d ceiling", len(excerpt), MaxExcerptBytes)
	}
	if lines := strings.Count(excerpt, "\n") + 1; lines > MaxExcerptLines {
		t.Fatalf("excerpt is %d lines, over the %d ceiling", lines, MaxExcerptLines)
	}
}

// The same log must always reduce to the same excerpt, or a failing turn cannot
// be reproduced without re-running CI.
func TestReduceLogIsDeterministic(t *testing.T) {
	raw := "ok  \tpkg/a\t0.1s\nFAIL\tpkg/b\t0.2s\nerror: undefined: Frobnicate\n"
	first := ReduceLog(raw)
	for range 5 {
		if ReduceLog(raw) != first {
			t.Fatal("reduction is not deterministic")
		}
	}
}

func TestReduceLogCollapsesRepeats(t *testing.T) {
	var raw strings.Builder
	for range 200 {
		raw.WriteString("error: the same warning over and over\n")
	}
	excerpt := ReduceLog(raw.String())
	if count := strings.Count(excerpt, "the same warning"); count > maxRepeatedLine {
		t.Fatalf("a repeated line appeared %d times", count)
	}
}

// A log whose failure is phrased in words the reducer does not know still has
// to produce something; the tail is the honest fallback.
func TestReduceLogFallsBackToTail(t *testing.T) {
	var raw strings.Builder
	for i := range 500 {
		fmt.Fprintf(&raw, "step %d finished quietly\n", i)
	}
	raw.WriteString("the last thing that happened\n")

	excerpt := ReduceLog(raw.String())
	if !strings.Contains(excerpt, "the last thing that happened") {
		t.Fatalf("the tail was not kept:\n%s", excerpt)
	}
	if len(excerpt) > MaxExcerptBytes {
		t.Fatalf("the fallback ignored the byte ceiling (%d)", len(excerpt))
	}
}

func TestSummarizeAnnotations(t *testing.T) {
	annotations := []checkAnnotation{
		{Path: "internal/a.go", StartLine: 12, AnnotationLevel: "failure", Title: "vet", Message: "unused variable\n\nmore"},
		{Path: "internal/b.go", AnnotationLevel: "notice", Message: "just a note"},
	}
	out := summarizeAnnotations(annotations)
	if len(out) != 1 {
		t.Fatalf("expected notices to be dropped, got %v", out)
	}
	if !strings.Contains(out[0], "internal/a.go:12") || !strings.Contains(out[0], "unused variable") {
		t.Fatalf("annotation lost detail: %q", out[0])
	}
	if strings.Contains(out[0], "\n") {
		t.Fatalf("annotation kept newlines: %q", out[0])
	}
}

func TestFailedStep(t *testing.T) {
	job := workflowJob{}
	job.Steps = append(job.Steps,
		struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			Number     int    `json:"number"`
		}{Name: "Set up Go", Conclusion: "success"},
		struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			Number     int    `json:"number"`
		}{Name: "go test ./...", Conclusion: "failure"},
	)
	if got := failedStep(job); got != "go test ./..." {
		t.Fatalf("failed step is %q", got)
	}
}
