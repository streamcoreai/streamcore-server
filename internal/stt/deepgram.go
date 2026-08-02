package stt

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/streamcoreai/server/internal/config"

	clientinterfaces "github.com/deepgram/deepgram-go-sdk/v3/pkg/client/interfaces"
	websocketv1 "github.com/deepgram/deepgram-go-sdk/v3/pkg/client/listen/v1/websocket"

	msginterfaces "github.com/deepgram/deepgram-go-sdk/v3/pkg/api/listen/v1/websocket/interfaces"
)

// recentFinalSuppressWindow is how long an identical final transcript is
// suppressed after being emitted. Deepgram can re-deliver the same text via
// both speech_final and UtteranceEnd; emitting twice would make the agent
// answer the same sentence twice.
const recentFinalSuppressWindow = 5 * time.Second

// deepgramCallback implements the Deepgram SDK's LiveMessageCallback interface.
//
// Deepgram delivers an utterance as a series of chunks: interim results, then
// one or more is_final chunks, and finally (usually) one carrying
// speech_final. Emitting each is_final chunk separately splits a single
// sentence into several turns, and consecutive chunks routinely overlap by a
// few words. This callback accumulates the chunks, merges the overlaps, and
// emits exactly one final per utterance.
type deepgramCallback struct {
	onResult func(TranscriptResult)
	mu       sync.Mutex
	// finalized accumulates is_final chunks for the current utterance.
	finalized          string
	lastEmittedPartial string
	lastFinal          string
	lastFinalAt        time.Time
	speechActive       bool
	// lastChunkConfidence remembers the confidence from the most recent
	// message of this utterance, so the UtteranceEnd flush path — which has
	// no message of its own — can still report one.
	lastChunkConfidence float64
}

func (cb *deepgramCallback) Open(or *msginterfaces.OpenResponse) error {
	log.Println("[stt] Deepgram connection opened")
	return nil
}

func (cb *deepgramCallback) Message(mr *msginterfaces.MessageResponse) error {
	if len(mr.Channel.Alternatives) == 0 {
		return nil
	}

	alt := mr.Channel.Alternatives[0]
	text := strings.TrimSpace(alt.Transcript)
	if text == "" {
		return nil
	}
	confidence := alt.Confidence

	var emit *TranscriptResult

	cb.mu.Lock()
	cb.lastChunkConfidence = confidence
	if mr.IsFinal {
		cb.finalized = mergeTranscriptChunks(cb.finalized, text)
		if mr.SpeechFinal {
			combined := strings.TrimSpace(cb.finalized)
			if combined == "" {
				combined = text
			}
			if !cb.shouldSuppressRecentFinalLocked(combined) {
				emit = &TranscriptResult{Text: combined, IsFinal: true, Confidence: confidence}
			}
			cb.resetUtteranceLocked(combined)
		} else {
			// Emit progress so barge-in and the text UI still see current words.
			emit = cb.partialResultLocked(cb.finalized, confidence)
		}
	} else {
		combined := text
		if cb.finalized != "" {
			combined = strings.TrimSpace(cb.finalized + " " + text)
		}
		emit = cb.partialResultLocked(combined, confidence)
	}
	cb.mu.Unlock()

	if emit != nil {
		if emit.IsFinal {
			log.Printf("[stt] final: %q (conf=%.2f)", emit.Text, emit.Confidence)
		}
		cb.onResult(*emit)
	}
	return nil
}

// partialResultLocked builds an interim result, dropping repeats and anything
// that merely restates a final we just emitted.
func (cb *deepgramCallback) partialResultLocked(text string, confidence float64) *TranscriptResult {
	text = strings.TrimSpace(text)
	if text == "" ||
		sameTranscript(text, cb.lastEmittedPartial) ||
		cb.shouldSuppressRecentFinalLocked(text) {
		return nil
	}
	cb.lastEmittedPartial = text
	return &TranscriptResult{Text: text, IsFinal: false, Confidence: confidence}
}

// resetUtteranceLocked clears per-utterance accumulation and records the final
// text so an immediate repeat can be suppressed.
func (cb *deepgramCallback) resetUtteranceLocked(finalText string) {
	cb.finalized = ""
	cb.lastEmittedPartial = ""
	cb.speechActive = false
	cb.lastChunkConfidence = 0
	if strings.TrimSpace(finalText) != "" {
		cb.lastFinal = finalText
		cb.lastFinalAt = time.Now()
	}
}

func (cb *deepgramCallback) shouldSuppressRecentFinalLocked(text string) bool {
	return sameTranscript(text, cb.lastFinal) && time.Since(cb.lastFinalAt) < recentFinalSuppressWindow
}

func (cb *deepgramCallback) Metadata(md *msginterfaces.MetadataResponse) error {
	return nil
}

func (cb *deepgramCallback) SpeechStarted(ssr *msginterfaces.SpeechStartedResponse) error {
	var logStarted bool
	cb.mu.Lock()
	if !cb.speechActive {
		cb.speechActive = true
		logStarted = true
	}
	cb.mu.Unlock()
	if logStarted {
		log.Println("[stt] Deepgram speech started")
	}
	return nil
}

// UtteranceEnd is the fallback path: if Deepgram closes the utterance without
// ever sending a speech_final message, the buffered chunks would otherwise sit
// in `finalized` forever and the caller's turn would be silently dropped.
func (cb *deepgramCallback) UtteranceEnd(ur *msginterfaces.UtteranceEndResponse) error {
	cb.mu.Lock()
	combined := strings.TrimSpace(cb.finalized)
	emit := combined != "" && !cb.shouldSuppressRecentFinalLocked(combined)
	confidence := cb.lastChunkConfidence
	cb.resetUtteranceLocked(combined)
	cb.mu.Unlock()

	if emit {
		log.Printf("[stt] utterance end flush: %q (conf=%.2f)", combined, confidence)
		cb.onResult(TranscriptResult{Text: combined, IsFinal: true, Confidence: confidence})
	}
	return nil
}

func (cb *deepgramCallback) Close(cr *msginterfaces.CloseResponse) error {
	log.Println("[stt] Deepgram connection closed")
	return nil
}

func (cb *deepgramCallback) Error(er *msginterfaces.ErrorResponse) error {
	log.Printf("[stt] Deepgram error: %s", er.ErrMsg)
	return nil
}

func (cb *deepgramCallback) UnhandledEvent(byData []byte) error {
	return nil
}

// deepgramClient wraps the official Deepgram Go SDK websocket client.
type deepgramClient struct {
	ws     *websocketv1.WSCallback
	cancel context.CancelFunc
}

func NewDeepgramClient(ctx context.Context, cfg config.DeepgramConfig, onResult func(TranscriptResult)) (*deepgramClient, error) {
	sttCtx, cancel := context.WithCancel(ctx)

	tOptions := &clientinterfaces.LiveTranscriptionOptions{
		Model:          cfg.Model,
		Language:       nova3Language(cfg.Language),
		Encoding:       "linear16",
		SampleRate:     16000,
		Channels:       1,
		Punctuate:      true,
		InterimResults: true,
		Endpointing:    cfg.Endpointing,
		// UtteranceEndMs drives the UtteranceEnd event the callback uses to
		// flush buffered chunks when speech_final never arrives.
		UtteranceEndMs: cfg.UtteranceEndMs,
		VadEvents:      true,
		// Keyterms bias the decoder toward domain vocabulary that is
		// phonetically ambiguous against common English. Nova-3 only; the SDK
		// silently drops them on other models.
		Keyterm: cfg.Keyterms,
	}
	if len(cfg.Keyterms) > 0 {
		log.Printf("[stt] deepgram keyterms loaded: count=%d", len(cfg.Keyterms))
	}

	cb := &deepgramCallback{onResult: onResult}

	ws, err := websocketv1.NewUsingCallbackWithCancel(sttCtx, cancel, cfg.APIKey, &clientinterfaces.ClientOptions{}, tOptions, cb)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("deepgram client: %w", err)
	}

	if !ws.Connect() {
		cancel()
		return nil, fmt.Errorf("deepgram: failed to connect")
	}

	log.Println("[stt] connected to Deepgram")
	return &deepgramClient{ws: ws, cancel: cancel}, nil
}

// nova3Language maps a BCP-47 locale to the language code Nova-3 accepts.
// English and Spanish have dedicated models; everything else routes to the
// multilingual model rather than failing.
func nova3Language(locale string) string {
	base := strings.ToLower(strings.TrimSpace(locale))
	if idx := strings.Index(base, "-"); idx > 0 {
		base = base[:idx]
	}
	switch base {
	case "", "en":
		return "en"
	case "es":
		return "es"
	default:
		return "multi"
	}
}

// SendAudio sends raw linear16 PCM bytes to Deepgram.
func (c *deepgramClient) SendAudio(data []byte) error {
	_, err := c.ws.Write(data)
	return err
}

// Close gracefully shuts down the Deepgram connection.
func (c *deepgramClient) Close() {
	if c.ws != nil {
		c.ws.Finish()
	}
	c.cancel()
	log.Println("[stt] closed")
}
