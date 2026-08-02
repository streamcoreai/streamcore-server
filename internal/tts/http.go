package tts

import (
	"net/http"
	"time"
)

// newPooledHTTPClient returns an http.Client with a warm connection pool so
// per-sentence synthesis calls on the live audio path reuse an established
// TLS+HTTP/2 connection instead of paying a fresh handshake (~100-200ms) on
// every sentence. Mirrors the transport used by the OpenAI LLM client.
//
// No overall client Timeout is set: synthesis bodies are read to completion
// and the pipeline cancels via ctx on barge-in/hangup; a total-request timeout
// would cut off long utterances mid-stream. Phase-level limits on the
// transport still bound a hung dial or unresponsive server.
func newPooledHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
}
