package tts

import (
	"context"
	"fmt"
	"io"
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

// pumpPCMStream incrementally reads raw linear16 PCM from body and emits it
// on ch as it arrives, so playback can start before the provider finishes
// synthesizing. Emitted chunks are always even-length (int16-aligned); a
// trailing odd byte is carried into the next read. Returns on EOF, read
// error (reported on ch), or ctx cancellation. The caller owns closing ch
// and body.
func pumpPCMStream(ctx context.Context, body io.Reader, ch chan<- StreamChunk) {
	var carry byte
	hasCarry := false
	buf := make([]byte, 8192)
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := body.Read(buf)
		if n > 0 {
			chunk := make([]byte, 0, n+1)
			if hasCarry {
				chunk = append(chunk, carry)
				hasCarry = false
			}
			chunk = append(chunk, buf[:n]...)
			if len(chunk)%2 == 1 {
				carry = chunk[len(chunk)-1]
				hasCarry = true
				chunk = chunk[:len(chunk)-1]
			}
			if len(chunk) > 0 {
				select {
				case ch <- StreamChunk{PCM: chunk}:
				case <-ctx.Done():
					return
				}
			}
		}
		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				select {
				case ch <- StreamChunk{Err: fmt.Errorf("tts stream read: %w", err)}:
				case <-ctx.Done():
				}
			}
			return
		}
	}
}

// streamHTTPResponse turns an in-flight HTTP response carrying raw linear16
// PCM into a StreamChunk channel. It takes ownership of resp.Body.
func streamHTTPResponse(ctx context.Context, resp *http.Response) <-chan StreamChunk {
	ch := make(chan StreamChunk, 8)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		pumpPCMStream(ctx, resp.Body, ch)
	}()
	return ch
}

// streamWhole adapts a one-shot Synthesize into the streaming interface for
// providers that return audio in a single envelope (a JSON body, say) and
// therefore cannot emit partial audio.
func streamWhole(ctx context.Context, synth func(context.Context, string) ([]byte, error), text string) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 1)
	go func() {
		defer close(ch)
		pcm, err := synth(ctx, text)
		if err != nil {
			ch <- StreamChunk{Err: err}
			return
		}
		ch <- StreamChunk{PCM: pcm}
	}()
	return ch, nil
}
