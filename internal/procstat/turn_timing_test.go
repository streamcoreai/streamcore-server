package procstat

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestTurnTimingLogHit(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	(&TurnTiming{
		EndpointMs:     600,
		MergeWaitMs:    400,
		EmbeddingMs:    300,
		VectorSearchMs: 20,
		RAGChunks:      3,
		QuietWaitMs:    100,
		LLMTTFTMs:      800,
		TTSFirstByteMs: 950,
	}).Log()

	out := buf.String()
	// total = endpoint (600) + turn-boundary→first-audio (950); the merge
	// wait is already inside TTSFirstByteMs, which counts from the first final.
	for _, want := range []string{"rag=hit", "endpoint_ms=600", "merge_wait_ms=400", "embed_ms=300", "vsearch_ms=20", "llm_ttft_ms=800", "total_user_to_speech_ms=1550"} {
		if !strings.Contains(out, want) {
			t.Errorf("log line missing %q: %s", want, out)
		}
	}
}

func TestTurnTimingLogSkip(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	(&TurnTiming{RAGSkipped: true, RAGSkipReason: "form-active"}).Log()

	if out := buf.String(); !strings.Contains(out, "rag=skip:form-active") {
		t.Errorf("expected skip reason in log, got: %s", out)
	}
}

func TestTurnTimingLogNilSafe(t *testing.T) {
	var tt *TurnTiming
	tt.Log() // must not panic
}
