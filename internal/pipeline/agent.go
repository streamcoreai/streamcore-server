package pipeline

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/audio"
	"github.com/streamcoreai/streamcore-server/internal/llm"
	"github.com/streamcoreai/streamcore-server/internal/procstat"
	"github.com/streamcoreai/streamcore-server/internal/rag"
	"github.com/streamcoreai/streamcore-server/internal/tts"
)

// runAgent is the central orchestrator goroutine. It receives transcript
// events from STT, calls the LLM for responses, synthesizes TTS audio,
// and pushes PCM frames to the outbound sender. It handles barge-in
// interruption by cancelling in-progress responses.
func (p *Pipeline) runAgent() {
	for {
		select {
		case <-p.ctx.Done():
			return

		case ev := <-p.transcriptCh:
			// Forward all transcripts to client for display
			p.sendEvent(transcriptMsg{
				Type:  "transcript",
				Text:  ev.Text,
				Final: ev.Final,
			})

			if !ev.Final {
				continue
			}

			if p.isPassiveAcknowledgement(ev) {
				log.Printf("[agent] acknowledgement ignored (caller talking over agent): %q", ev.Text)
				continue
			}

			log.Printf("[agent] user: %s", ev.Text)
			gen := p.supersedeResponse()
			p.sendEvent(stateMsg{Type: "state", State: "thinking"})
			go func() { defer p.recoverPanic("respond"); p.respond(gen, ev.Text, ev.TurnStart) }()

		case <-p.interruptCh:
			// Capture what agent was saying for interruption context
			if txt, _ := p.lastAgentText.Load().(string); txt != "" {
				p.interruptedText.Store(txt)
			}
			log.Println("[agent] interrupted (barge-in)")
			p.supersedeResponse()
		}
	}
}

// isPassiveAcknowledgement reports whether a completed turn is just the caller
// signalling "I'm still with you" over the agent's voice, rather than a turn
// that wants an answer.
//
// These arrive as ordinary finals, and the backchannel suppression in
// runInbound only guards the VAD barge-in path — it never sees them. So
// without this check an "okay" dropped mid-explanation starts a full LLM turn
// whose audio lands on top of the sentences still in flight.
func (p *Pipeline) isPassiveAcknowledgement(ev TranscriptEvent) bool {
	// Said into silence, an acknowledgement is a real (if minimal) turn — the
	// caller is handing the floor back and deserves a reply. Only one spoken
	// over the agent is passive.
	if !ev.OverAgentSpeech || !isAcknowledgementOnly(ev.Text) {
		return false
	}

	// A confirmed barge-in already cut the agent off mid-sentence. Swallowing
	// the turn now would leave the caller in dead air, so answer it even
	// though the words themselves carry nothing.
	if interrupted, _ := p.interruptedText.Load().(string); interrupted != "" {
		return false
	}

	return true
}

// respond runs the LLM → TTS → outbound flow for a single user utterance.
// gen is the response generation issued by supersedeResponse; it scopes every
// mutation of shared speaking state to this response, so a barge-in or a newer
// turn cleanly takes ownership of the audio path.
func (p *Pipeline) respond(gen uint64, userText string, turnStart time.Time) {
	respCtx, cancel := context.WithCancel(p.ctx)

	// Superseded between the turn being dispatched and this goroutine getting
	// scheduled — a newer turn already owns the audio path, and synthesizing
	// now would put two responses on outPCMCh at once.
	if !p.registerResponse(gen, cancel) {
		cancel()
		return
	}

	defer func() {
		cancel()
		p.finishResponse(gen)
	}()

	// NOTE: speaking flag is set in synthesizeSentences when first TTS audio
	// is actually produced, not here. This prevents false barge-in triggers
	// during LLM thinking time.

	// Build LLM input with interruption context if the user barged in.
	// For short redirections ("no", "wait", "stop") we focus the LLM on the
	// user's intent without dumping the previous response — like a human would.
	// For longer interruptions we include brief prior context so the LLM can
	// pick up naturally.
	// turn.Text stays the caller's words throughout; turn.Prompt accumulates the
	// same context inline for providers that can only read one message.
	turn := llm.Turn{Text: userText, Prompt: userText}
	if interrupted, _ := p.interruptedText.Load().(string); interrupted != "" {
		p.interruptedText.Store("")
		trimmedUser := strings.TrimSpace(strings.ToLower(userText))
		words := strings.Fields(trimmedUser)
		if len(words) <= 2 {
			// Short interruption — user is redirecting, not adding new info.
			// Just let the LLM know it was cut off so it doesn't repeat itself.
			turn.Prompt = fmt.Sprintf("[You were interrupted mid-response. The user said: '%s'. Respond to what they said — do not continue or repeat your previous answer.]", userText)
		} else {
			// Longer interruption — include brief context of what agent was saying.
			if len(interrupted) > 150 {
				interrupted = "..." + interrupted[len(interrupted)-150:]
			}
			turn.Prompt = fmt.Sprintf("[You were interrupted while saying: '%s'. The user said: '%s'. Address what the user said.]", interrupted, userText)
		}
		turn.InterruptedText = interrupted
	}

	timing := &procstat.TurnTiming{}
	if !turnStart.IsZero() {
		timing.EndpointMs = time.Since(turnStart).Milliseconds()
	}

	// Retrieve relevant context via RAG before calling the LLM. Turns with no
	// content-bearing words ("okay, sure, thanks") can't anchor a vector
	// search, so skipping them removes a network round trip from the turn.
	if p.ragClient != nil {
		if skip, reason := shouldSkipRAG(p, userText); skip {
			timing.RAGSkipped, timing.RAGSkipReason = true, reason
			log.Printf("[agent] RAG skipped: %s", reason)
		} else {
			query := p.ragSearchQuery(userText)
			var chunks []string
			var err error

			// A prefetch started during the turn-merge window has already paid
			// the embedding + vector-search latency.
			if st := p.takeRAGPrefetch(query); st != nil {
				chunks, err = st.await(respCtx)
				timing.RAGPrefetched = true
				timing.EmbeddingMs, timing.VectorSearchMs = st.timing.EmbedMs, st.timing.QueryMs
			} else {
				rt := &rag.Timing{}
				chunks, err = p.ragClient.Search(rag.WithTiming(respCtx, rt), query, 0)
				timing.EmbeddingMs, timing.VectorSearchMs = rt.EmbedMs, rt.QueryMs
			}

			if err != nil {
				log.Printf("[agent] RAG search error: %v", err)
			} else if len(chunks) > 0 {
				timing.RAGChunks = len(chunks)
				turn.Context = chunks
				turn.Prompt = fmt.Sprintf("[Context:\n%s]\n\nUser: %s", strings.Join(chunks, "\n---\n"), turn.Prompt)
			}
		}
	}

	// Prepend the rolling summary so facts from early in a long call survive
	// after the LLM's own history window has dropped them.
	if summary := p.currentRollingSummary(); summary != "" {
		turn.Summary = summary
		if block := summaryContextBlock(summary); block != "" {
			turn.Prompt = block + turn.Prompt
		}
	}

	// When the caller's speech came through garbled, tell the model to ask
	// rather than guess. Escalates with consecutive low-confidence turns.
	if note := p.misunderstandingNote(userText); note != "" {
		turn.Note = note
		turn.Prompt += note
	}

	p.lastAgentText.Store("")
	if p.transcriptLog != nil {
		p.transcriptLog.Add("user", userText)
	}
	p.maybeRefreshRollingSummary()

	// Channel decouples LLM sentence production from TTS synthesis so the
	// LLM stream isn't blocked while waiting for audio.
	sentences := make(chan string, 8)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer p.recoverPanic("synthesizeSentences")
		p.synthesizeSentences(respCtx, sentences, turnStart)
	}()

	llmFirstToken := true
	_, err := p.llmClient.Chat(respCtx, turn,
		func(chunk string) {
			if llmFirstToken && p.cfg.Pipeline.Debug && !turnStart.IsZero() {
				llmFirstToken = false
				p.sendEvent(timingMsg{
					Type:  "timing",
					Stage: "llm_first_token",
					Ms:    time.Since(turnStart).Milliseconds(),
				})
			}
			// Accumulate response text for interruption context. Delivery tags
			// are stripped: this text is quoted back to the LLM verbatim when
			// the user barges in, and feeding it "[warm] ..." would teach the
			// model that tags are part of normal prose.
			acc := chunk
			if prev, _ := p.lastAgentText.Load().(string); prev != "" {
				acc = prev + chunk
			}
			p.lastAgentText.Store(tts.StripVoiceTags(acc))
			p.sendEvent(responseMsg{
				Type: "response",
				Text: chunk,
			})
		},
		func(sentence string) {
			select {
			case sentences <- sentence:
			case <-respCtx.Done():
			}
		},
	)
	close(sentences)

	if err != nil && respCtx.Err() == nil {
		log.Printf("[agent] LLM error: %v", err)
	}

	wg.Wait()

	if p.transcriptLog != nil {
		if txt, _ := p.lastAgentText.Load().(string); txt != "" {
			p.transcriptLog.Add("agent", txt)
		}
	}
	if p.cfg.Pipeline.Debug {
		timing.Log()
	}
}

// synthesizeSentences reads sentences from the channel, calls TTS for each,
// and pushes the resulting PCM frames into outPCMCh for the sender goroutine.
func (p *Pipeline) synthesizeSentences(ctx context.Context, sentences <-chan string, turnStart time.Time) {
	emittedTTSTiming := false
	emittedSpeaking := false

	// leftover holds samples that did not fill a complete frame. Streaming
	// chunks arrive on arbitrary byte boundaries, so the remainder carries
	// into the next chunk rather than being padded with silence mid-utterance,
	// which would insert an audible click every chunk.
	var leftover []int16
	talkspurtSent := false

	emitFrame := func(frame PCMFrame) bool {
		select {
		case p.outPCMCh <- frame:
			if !emittedTTSTiming && p.cfg.Pipeline.Debug && !turnStart.IsZero() {
				emittedTTSTiming = true
				p.sendEvent(timingMsg{
					Type:  "timing",
					Stage: "tts_first_byte",
					Ms:    time.Since(turnStart).Milliseconds(),
				})
			}
			// Send "speaking" state when the first audio frame is actually
			// produced, not when respond() begins. This keeps the "thinking"
			// state visible while the LLM and TTS are working.
			if !emittedSpeaking {
				emittedSpeaking = true
				p.setSpeaking(true)
				p.sendEvent(stateMsg{Type: "state", State: "speaking"})
			}
			return true
		case <-ctx.Done():
			return false
		}
	}

	for sentence := range sentences {
		if ctx.Err() != nil {
			return
		}

		// The LLM may prefix a sentence with a delivery tag ([warm], [calm], …)
		// per the voice-output rules. The tag is stripped here — the caller
		// never hears it — and mapped to provider voice controls when the
		// configured TTS supports them.
		controls, clean := tts.ParseVoiceTag(sentence)

		// Stream so audio starts playing as soon as the first PCM chunk
		// arrives, instead of waiting for the whole sentence to synthesize.
		var stream <-chan tts.StreamChunk
		var err error
		if cs, ok := p.ttsClient.(tts.ControllableStreamer); ok && !controls.IsZero() {
			stream, err = cs.SynthesizeStreamWithControls(ctx, clean, controls)
		} else {
			stream, err = p.ttsClient.SynthesizeStream(ctx, clean)
		}
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[agent] TTS stream error: %v", err)
			}
			return
		}

		for chunk := range stream {
			if chunk.Err != nil {
				if ctx.Err() == nil {
					log.Printf("[agent] TTS error: %v", chunk.Err)
				}
				return
			}
			if ctx.Err() != nil {
				return
			}

			samples := append(leftover, audio.Linear16BytesToPCM(chunk.PCM)...)
			leftover = nil

			// Emit complete frames only; the partial tail waits for the next
			// chunk, or is flushed once every sentence is done.
			i := 0
			for ; i+audio.FrameSize <= len(samples); i += audio.FrameSize {
				frame := PCMFrame{
					Samples:      make([]int16, audio.FrameSize),
					NewTalkspurt: !talkspurtSent,
				}
				copy(frame.Samples, samples[i:i+audio.FrameSize])
				talkspurtSent = true
				if !emitFrame(frame) {
					return
				}
			}
			if i < len(samples) {
				leftover = append([]int16{}, samples[i:]...)
			}
		}
	}

	// Flush the final partial frame, padded with silence. Only the very end of
	// the turn gets padding, so the click lands where the agent stops talking.
	if len(leftover) > 0 {
		frame := PCMFrame{
			Samples:      make([]int16, audio.FrameSize),
			NewTalkspurt: !talkspurtSent,
		}
		copy(frame.Samples, leftover)
		emitFrame(frame)
	}
}

// waitOutboundDrain waits until the outbound PCM channel is empty, meaning
// runSender has picked up all queued frames. It gives up as soon as
// generation gen is superseded — the frames it would be waiting on have been
// discarded, and the new response is already filling the channel behind them.
func (p *Pipeline) waitOutboundDrain(gen uint64) {
	p.waitOutboundEmpty(func() bool { return p.responseGen.Load() != gen })
}

// waitOutboundEmpty polls the outbound PCM channel until it is empty, meaning
// runSender has picked up all queued frames. It polls briefly with a timeout
// to avoid blocking forever, and returns early if superseded reports true.
func (p *Pipeline) waitOutboundEmpty(superseded func() bool) {
	deadline := time.After(5 * time.Second)
	for {
		if superseded() {
			return
		}
		if len(p.outPCMCh) == 0 {
			// Frames consumed by sender; add a small grace period for the
			// last few RTP packets to reach the client and be played.
			time.Sleep(300 * time.Millisecond)
			return
		}
		select {
		case <-deadline:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// supersedeResponse retires whatever the agent is currently saying so a new
// turn can start on a clear audio path, and returns the generation that the
// incoming response owns.
//
// All three steps are load-bearing:
//
//   - bumping the generation invalidates the outgoing response, so that when
//     it finally unwinds it leaves speaking / responseCancel / the client
//     state alone — those now belong to its successor
//   - cancelling stops the LLM and TTS from producing any MORE audio
//   - draining discards audio the old response ALREADY queued; cancellation
//     does nothing about the frames sitting in outPCMCh (up to outPCMChSize,
//     ~2s worth), and leaving them there is what makes the caller hear the
//     previous answer keep playing while the new one stacks up behind it
func (p *Pipeline) supersedeResponse() uint64 {
	gen := p.responseGen.Add(1)
	p.cancelResponse()
	p.drainOutbound()
	// The queued audio is gone, so the agent is no longer speaking. The next
	// response re-raises this when its first frame actually reaches the wire.
	p.setSpeaking(false)
	return gen
}

// backchannelGrace is how long after the agent's last frame an incoming final
// still counts as having been spoken over it. STT endpointing sits on a final
// for ~600ms after the caller stops, so an "okay" landing on the agent's
// closing words reaches the agent goroutine well after speaking cleared.
const backchannelGrace = 1500 * time.Millisecond

// setSpeaking updates the speaking flag, stamping the moment agent speech
// ended so agentSpeechRecent can cover the STT endpointing lag.
func (p *Pipeline) setSpeaking(speaking bool) {
	if prev := p.speaking.Swap(speaking); prev && !speaking {
		p.speechEndedAt.Store(time.Now().UnixNano())
	}
}

// agentSpeechRecent reports whether the agent is speaking, or stopped recently
// enough that a caller utterance arriving now was plausibly spoken over it.
func (p *Pipeline) agentSpeechRecent() bool {
	if p.speaking.Load() {
		return true
	}
	endedAt := p.speechEndedAt.Load()
	return endedAt != 0 && time.Since(time.Unix(0, endedAt)) < backchannelGrace
}

// registerResponse installs cancel as the cancel func for generation gen,
// reporting false if gen has already been superseded.
func (p *Pipeline) registerResponse(gen uint64, cancel context.CancelFunc) bool {
	p.responseMu.Lock()
	defer p.responseMu.Unlock()
	if p.responseGen.Load() != gen {
		return false
	}
	if p.responseCancel != nil {
		p.responseCancel()
	}
	p.responseCancel = cancel
	p.responseCancelGen = gen
	return true
}

// finishResponse releases the shared speaking state owned by generation gen.
//
// A superseded response returns without touching anything. This guard is the
// fix for responses talking over each other: a cancelled response unwinds
// asynchronously (its LLM stream has to return first), by which point its
// successor is already registered, and the unconditional cleanup this
// replaced would nil out the successor's cancel func — leaving it immune to
// the next barge-in and audible underneath the turn after it.
func (p *Pipeline) finishResponse(gen uint64) {
	p.responseMu.Lock()
	if p.responseCancelGen == gen {
		p.responseCancel = nil
	}
	stale := p.responseGen.Load() != gen
	p.responseMu.Unlock()
	if stale {
		return
	}

	p.setSpeaking(false)

	// Wait for the outbound PCM queue to drain before signalling "listening",
	// so the sender has RTP-sent every frame before the client is told the
	// agent finished speaking.
	p.waitOutboundDrain(gen)

	// The drain can block for seconds; a new turn may have started inside it,
	// in which case "listening" would contradict the response now speaking.
	if p.responseGen.Load() != gen {
		return
	}
	p.sendEvent(stateMsg{Type: "state", State: "listening"})
}

// cancelResponse cancels any in-progress LLM/TTS response.
func (p *Pipeline) cancelResponse() {
	p.responseMu.Lock()
	if p.responseCancel != nil {
		p.responseCancel()
		p.responseCancel = nil
	}
	p.responseMu.Unlock()
}

// drainOutbound discards any queued outbound PCM frames.
func (p *Pipeline) drainOutbound() {
	for {
		select {
		case <-p.outPCMCh:
		default:
			return
		}
	}
}

// greet synthesizes the greeting text via TTS and pushes PCM to outPCMCh.
// This runs once, right after the pipeline starts.
func (p *Pipeline) greet(text string) {
	log.Printf("[agent] greeting: %s", text)
	// NOTE: speaking flag and "speaking" state event are set inside
	// synthesizeSentences when TTS audio is actually produced.

	// The greeting goes through the same ownership handshake as a response so
	// that a caller who starts talking over it can actually cut it off. Run on
	// the pipeline context instead, and cancelResponse has nothing to cancel:
	// the greeting keeps synthesizing underneath the first real answer.
	gen := p.responseGen.Load()
	ctx, cancel := context.WithCancel(p.ctx)
	if !p.registerResponse(gen, cancel) {
		cancel()
		return
	}
	defer func() {
		cancel()
		p.finishResponse(gen)
	}()

	sentences := make(chan string, 1)
	sentences <- text
	close(sentences)

	p.synthesizeSentences(ctx, sentences, time.Time{})
}

// greetingText returns the appropriate greeting based on call direction.
// For outbound calls it prefers greeting_outgoing, falling back to greeting.
func (p *Pipeline) greetingText() string {
	if p.direction == "outbound" && p.cfg.Pipeline.GreetingOutgoing != "" {
		return p.cfg.Pipeline.GreetingOutgoing
	}
	return p.cfg.Pipeline.Greeting
}
