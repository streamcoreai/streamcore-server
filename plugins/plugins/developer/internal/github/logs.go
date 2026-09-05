package github

import (
	"fmt"
	"regexp"
	"strings"
)

// Byte and line ceilings. maxRawLogBytes is what may be downloaded; the rest
// bound what survives reduction and can reach the model. A job log is routinely
// several megabytes of dependency chatter, and the useful part is a few dozen
// lines near the end.
const (
	maxRawLogBytes    = 8 << 20
	MaxExcerptBytes   = 4000
	MaxExcerptLines   = 60
	MaxDiffBytes      = 24 << 10
	contextBefore     = 2
	contextAfter      = 6
	maxRepeatedLine   = 2
	maxAnnotations    = 6
	maxFailedJobs     = 4
	maxAnnotationText = 240
)

// GitHub prefixes every log line with an ISO-8601 timestamp. It is pure noise
// once the run is identified, and it is a third of the bytes.
var logTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z\s?`)

// ANSI colour sequences survive into the plain-text log.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// signalPatterns mark a line as worth keeping. Order does not matter; a line
// matching any of them anchors a context window.
var signalPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^\s*(FAIL|FAILED|ERROR|error:)\b`),
	regexp.MustCompile(`^\s*---\s+FAIL:`),
	regexp.MustCompile(`(?i)\bpanic:`),
	regexp.MustCompile(`(?i)\bfatal error\b`),
	regexp.MustCompile(`(?i)\bassertion (failed|error)\b`),
	regexp.MustCompile(`(?i)^\s*(expected|actual|want|got|wanted):`),
	regexp.MustCompile(`(?i)\bexpected .* (but )?(got|received)\b`),
	// path/to/file.go:42:7: message — compilers and vet.
	regexp.MustCompile(`^\s*[\w./\\+-]+\.\w+:\d+(:\d+)?:\s`),
	regexp.MustCompile(`(?i)^##\[error\]`),
	regexp.MustCompile(`(?i)\bprocess completed with exit code [1-9]`),
	regexp.MustCompile(`(?i)\b(exit status|exit code) [1-9]`),
	regexp.MustCompile(`(?i)\b(traceback|stack trace)\b`),
	regexp.MustCompile(`(?i)^\s*(cannot|undefined|unresolved|no such file)\b`),
	regexp.MustCompile(`(?i)\bthe command .* failed\b`),
	regexp.MustCompile(`(?i)^\s*(E|FAILURES)\s`),
}

// noisePatterns are dropped before anything else is considered. Every one of
// these is either progress output or a success message; none of them has ever
// explained a failure.
var noisePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^\s*(downloading|fetching|extracting|resolving|installing|unpacking|pulling)\b`),
	regexp.MustCompile(`(?i)^\s*go: (downloading|finding|extracting)\b`),
	regexp.MustCompile(`(?i)^\s*(npm|yarn|pnpm) (warn|notice|info)\b`),
	regexp.MustCompile(`(?i)^\s*(added|removed|changed|audited) \d+ packages?\b`),
	regexp.MustCompile(`^\s*(ok|PASS|---\s+PASS:|\s*✓|\s*√)\b`),
	regexp.MustCompile(`(?i)^\s*\[command\]`),
	regexp.MustCompile(`(?i)^##\[(group|endgroup|debug|section)\]`),
	regexp.MustCompile(`^\s*\d+(\.\d+)?%\s`),
	regexp.MustCompile(`(?i)^\s*(receiving|counting|compressing|remote:)\b`),
	regexp.MustCompile(`(?i)^\s*(cache (hit|miss|restored|saved))\b`),
	regexp.MustCompile(`^\s*$`),
}

// ReduceLog turns a raw job log into a bounded excerpt built only from the
// lines that explain a failure, plus a little context around each.
//
// The reduction is deterministic: the same log always yields the same excerpt,
// so a failing turn can be reproduced without re-running CI.
func ReduceLog(raw string) string {
	lines := strings.Split(raw, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(ansiEscape.ReplaceAllString(logTimestamp.ReplaceAllString(line, ""), ""), "\r \t")
		cleaned = append(cleaned, line)
	}

	// Mark the context window around every signal line. Noise is skipped as an
	// anchor but may still be pulled in as context, which is fine — it is
	// bounded by the window, not by the log.
	keep := make([]bool, len(cleaned))
	anchored := false
	for i, line := range cleaned {
		if isNoise(line) || !isSignal(line) {
			continue
		}
		anchored = true
		for j := max(0, i-contextBefore); j <= min(len(cleaned)-1, i+contextAfter); j++ {
			keep[j] = true
		}
	}

	// Nothing matched: the failure is phrased in words this reducer does not
	// know. The tail of the log is the best remaining guess.
	if !anchored {
		return tail(cleaned, MaxExcerptLines, MaxExcerptBytes)
	}

	selected := make([]string, 0, MaxExcerptLines)
	repeats := make(map[string]int)
	gap := false
	for i, line := range cleaned {
		if !keep[i] {
			gap = true
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Context windows can reach back over progress output. A noise line is
		// still noise when it happens to sit next to a failure.
		if isNoise(line) && !isSignal(line) {
			gap = true
			continue
		}
		key := strings.TrimSpace(line)
		repeats[key]++
		if repeats[key] > maxRepeatedLine {
			continue
		}
		if gap && len(selected) > 0 {
			selected = append(selected, "…")
		}
		gap = false
		selected = append(selected, line)
	}

	// When the selection still overflows, the end of the log wins: CI writes
	// the summary of what failed after it has finished failing.
	return tail(selected, MaxExcerptLines, MaxExcerptBytes)
}

func isSignal(line string) bool {
	for _, pattern := range signalPatterns {
		if pattern.MatchString(line) {
			return true
		}
	}
	return false
}

func isNoise(line string) bool {
	for _, pattern := range noisePatterns {
		if pattern.MatchString(line) {
			return true
		}
	}
	return false
}

// tail keeps the last maxLines lines, then trims from the front until the
// result fits maxBytes. Cutting from the front keeps the final failure summary.
func tail(lines []string, maxLines, maxBytes int) string {
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	for {
		joined := strings.Join(lines, "\n")
		if len(joined) <= maxBytes || len(lines) <= 1 {
			return strings.TrimSpace(joined)
		}
		lines = lines[1:]
	}
}

// failedStep names the first step of a job that did not succeed. That is
// usually the command worth quoting back to the user.
func failedStep(job workflowJob) string {
	for _, step := range job.Steps {
		switch step.Conclusion {
		case "failure", "timed_out", "cancelled":
			return step.Name
		}
	}
	return ""
}

// summarizeAnnotations flattens check-run annotations into short strings.
func summarizeAnnotations(annotations []checkAnnotation) []string {
	out := make([]string, 0, maxAnnotations)
	for _, a := range annotations {
		if len(out) >= maxAnnotations {
			break
		}
		if a.AnnotationLevel == "notice" {
			continue
		}
		text := strings.TrimSpace(a.Message)
		if a.Title != "" {
			text = strings.TrimSpace(a.Title) + ": " + text
		}
		if a.Path != "" {
			location := a.Path
			if a.StartLine > 0 {
				location = fmt.Sprintf("%s:%d", a.Path, a.StartLine)
			}
			text = location + " — " + text
		}
		out = append(out, truncate(collapseBlankLines(text), maxAnnotationText))
	}
	return out
}

func collapseBlankLines(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
