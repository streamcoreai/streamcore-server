package procstat

import "log"

// TurnTiming is the per-turn latency breakdown for one user→agent exchange.
// All durations are milliseconds measured from the turn boundary (the first
// final transcript). It is the baseline measuring stick for latency work: it
// separates the embedding network hop from the vector query and from LLM/TTS
// time so regressions and wins are attributable to a single stage.
//
// A zero field means "not measured this turn" (e.g. RAG was skipped, or the
// turn was cancelled before that stage ran).
type TurnTiming struct {
	RAGSkipped     bool
	RAGSkipReason  string
	RAGPrefetched  bool  // retrieval overlapped the turn-merge window
	EndpointMs     int64 // caller's last VAD speech → first STT final (provider endpointing)
	MergeWaitMs    int64 // first STT final → turn fired (turn-buffer debounce)
	QuietWaitMs    int64 // pre-LLM quiet grace
	EmbeddingMs    int64 // embedding provider round-trip (public-internet hop)
	VectorSearchMs int64 // vector store query
	RAGChunks      int
	MaxSimilarity  float64 // best chunk similarity this turn (0 if skipped/miss)
	LLMTTFTMs      int64   // turn boundary → first LLM token
	TTSFirstByteMs int64   // turn boundary → first audio frame (total user-to-speech)
}

// Log emits a single structured line per turn. Cheap and allocation-light so it
// is safe to call on every turn from the agent goroutine.
func (t *TurnTiming) Log() {
	if t == nil {
		return
	}
	rag := "hit"
	if t.RAGSkipped {
		rag = "skip:" + t.RAGSkipReason
	} else if t.RAGPrefetched {
		rag = "prefetch"
	}
	// TTSFirstByteMs is measured from the turn boundary (first STT final),
	// which already includes the merge-window hold, so the caller-perceived
	// silence is endpointing + turn-boundary→first-audio.
	log.Printf("[turn-timing] rag=%s endpoint_ms=%d merge_wait_ms=%d quiet_wait_ms=%d embed_ms=%d vsearch_ms=%d rag_chunks=%d max_sim=%.2f llm_ttft_ms=%d tts_ttfb_ms=%d total_user_to_speech_ms=%d",
		rag, t.EndpointMs, t.MergeWaitMs, t.QuietWaitMs, t.EmbeddingMs, t.VectorSearchMs, t.RAGChunks, t.MaxSimilarity, t.LLMTTFTMs, t.TTSFirstByteMs, t.EndpointMs+t.TTSFirstByteMs)
}
