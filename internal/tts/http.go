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

// pcmAligner keeps a stream of PCM byte chunks int16-aligned across chunk
// boundaries. Providers split audio wherever it suits them — a socket read,
// an SSE event — and nothing guarantees the split lands on a sample edge.
// Emitting an odd-length chunk loses its trailing byte downstream in
// audio.Linear16BytesToPCM, so the odd byte is held here and re-attached to
// the front of the next chunk instead.
type pcmAligner struct {
	carry    byte
	hasCarry bool
}

// take returns the int16-aligned part of chunk: any byte carried over from
// the previous call, then chunk, minus a trailing odd byte that is kept for
// the next call. The result is a fresh slice, so callers may hand it to a
// consumer and reuse their own buffer.
func (a *pcmAligner) take(chunk []byte) []byte {
	if len(chunk) == 0 && !a.hasCarry {
		return nil
	}
	out := make([]byte, 0, len(chunk)+1)
	if a.hasCarry {
		out = append(out, a.carry)
		a.hasCarry = false
	}
	out = append(out, chunk...)
	if len(out)%2 == 1 {
		a.carry = out[len(out)-1]
		a.hasCarry = true
		out = out[:len(out)-1]
	}
	return out
}

// drainLimit bounds how much of an abandoned response body is read back for
// connection reuse. Anything left after a provider's terminal event is a
// trailing newline or a [DONE] marker, so the cap only exists to stop a
// misbehaving server turning a cleanup step into an unbounded read.
const drainLimit = 64 << 10

// drainForReuse reads the small remainder of a response body that was
// abandoned before EOF, so the connection returns to the idle pool instead of
// being torn down — the whole point of newPooledHTTPClient. It is skipped once
// ctx is cancelled: on barge-in the provider is usually still sending, and
// draining then would mean waiting on audio nobody will hear.
func drainForReuse(ctx context.Context, body io.Reader) {
	if ctx.Err() != nil {
		return
	}
	io.Copy(io.Discard, io.LimitReader(body, drainLimit))
}

// pumpPCMStream incrementally reads raw linear16 PCM from body and emits it
// on ch as it arrives, so playback can start before the provider finishes
// synthesizing. Emitted chunks are always even-length (int16-aligned); a
// trailing odd byte is carried into the next read. Returns on EOF, read
// error (reported on ch), or ctx cancellation. The caller owns closing ch
// and body.
func pumpPCMStream(ctx context.Context, body io.Reader, ch chan<- StreamChunk) {
	var align pcmAligner
	buf := make([]byte, 8192)
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := body.Read(buf)
		if n > 0 {
			if chunk := align.take(buf[:n]); len(chunk) > 0 {
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
