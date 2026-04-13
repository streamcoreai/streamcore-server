package pipeline

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/streamcoreai/server/internal/audio"
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

			log.Printf("[agent] user: %s", ev.Text)
			p.cancelResponse()
			p.sendEvent(stateMsg{Type: "state", State: "thinking"})
			go p.respond(ev.Text, ev.TurnStart)

		case <-p.interruptCh:
			// Capture what agent was saying for interruption context
			if txt, _ := p.lastAgentText.Load().(string); txt != "" {
				p.interruptedText.Store(txt)
			}
			log.Println("[agent] interrupted (barge-in)")
			p.cancelResponse()
			p.drainOutbound()
		}
	}
}

// respond runs the LLM → TTS → outbound flow for a single user utterance.
// It is cancellable via responseCancel (barge-in or new utterance).
func (p *Pipeline) respond(userText string, turnStart time.Time) {
	respCtx, cancel := context.WithCancel(p.ctx)

	p.responseMu.Lock()
	if p.responseCancel != nil {
		p.responseCancel()
	}
	p.responseCancel = cancel
	p.responseMu.Unlock()

	defer func() {
		cancel()
		p.speaking.Store(false)
		p.responseMu.Lock()
		p.responseCancel = nil
		p.responseMu.Unlock()

		// Wait for outbound PCM queue to drain before signalling "listening".
		// This ensures the sender goroutine has consumed and RTP-sent all
		// frames before we tell the client the agent finished speaking.
		p.waitOutboundDrain()

		p.sendEvent(stateMsg{Type: "state", State: "listening"})
	}()

	// NOTE: speaking flag is set in synthesizeSentences when first TTS audio
	// is actually produced, not here. This prevents false barge-in triggers
	// during LLM thinking time.

	// Build LLM input with interruption context if the user barged in.
	// For short redirections ("no", "wait", "stop") we focus the LLM on the
	// user's intent without dumping the previous response — like a human would.
	// For longer interruptions we include brief prior context so the LLM can
	// pick up naturally.
	llmInput := userText
	if interrupted, _ := p.interruptedText.Load().(string); interrupted != "" {
		p.interruptedText.Store("")
		trimmedUser := strings.TrimSpace(strings.ToLower(userText))
		words := strings.Fields(trimmedUser)
		if len(words) <= 2 {
			// Short interruption — user is redirecting, not adding new info.
			// Just let the LLM know it was cut off so it doesn't repeat itself.
			llmInput = fmt.Sprintf("[You were interrupted mid-response. The user said: '%s'. Respond to what they said — do not continue or repeat your previous answer.]", userText)
		} else {
			// Longer interruption — include brief context of what agent was saying.
			if len(interrupted) > 150 {
				interrupted = "..." + interrupted[len(interrupted)-150:]
			}
			llmInput = fmt.Sprintf("[You were interrupted while saying: '%s'. The user said: '%s'. Address what the user said.]", interrupted, userText)
		}
	}

	// Retrieve relevant context via RAG before calling the LLM.
	if p.ragClient != nil {
		chunks, err := p.ragClient.Search(respCtx, userText, 0)
		if err != nil {
			log.Printf("[agent] RAG search error: %v", err)
		} else if len(chunks) > 0 {
			llmInput = fmt.Sprintf("[Context:\n%s]\n\nUser: %s", strings.Join(chunks, "\n---\n"), llmInput)
		}
	}

	p.lastAgentText.Store("")

	// Channel decouples LLM sentence production from TTS synthesis so the
	// LLM stream isn't blocked while waiting for audio.
	sentences := make(chan string, 8)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.synthesizeSentences(respCtx, sentences, turnStart)
	}()

	llmFirstToken := true
	_, err := p.llmClient.Chat(respCtx, llmInput,
		func(chunk string) {
			if llmFirstToken && p.cfg.Pipeline.Debug && !turnStart.IsZero() {
				llmFirstToken = false
				p.sendEvent(timingMsg{
					Type:  "timing",
					Stage: "llm_first_token",
					Ms:    time.Since(turnStart).Milliseconds(),
				})
			}
			// Accumulate response text for interruption context
			if prev, _ := p.lastAgentText.Load().(string); prev != "" {
				p.lastAgentText.Store(prev + chunk)
			} else {
				p.lastAgentText.Store(chunk)
			}
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
}

// synthesizeSentences reads sentences from the channel, calls TTS for each,
// and pushes the resulting PCM frames into outPCMCh for the sender goroutine.
func (p *Pipeline) synthesizeSentences(ctx context.Context, sentences <-chan string, turnStart time.Time) {
	firstSentence := true
	emittedTTSTiming := false
	emittedSpeaking := false

	for sentence := range sentences {
		if ctx.Err() != nil {
			return
		}

		pcmBytes, err := p.ttsClient.Synthesize(ctx, sentence)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[agent] TTS error: %v", err)
			}
			return
		}

		pcm := audio.Linear16BytesToPCM(pcmBytes)

		for i := 0; i < len(pcm); i += audio.FrameSize {
			end := i + audio.FrameSize
			var samples []int16
			if end > len(pcm) {
				// Pad last frame with silence
				samples = make([]int16, audio.FrameSize)
				copy(samples, pcm[i:])
			} else {
				samples = pcm[i:end]
			}

			frame := PCMFrame{
				Samples:      samples,
				NewTalkspurt: (i == 0 && firstSentence),
			}

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
				// Send "speaking" state when the first audio frame is
				// actually produced, not when respond() begins. This
				// keeps the "thinking" state visible while the LLM and
				// TTS are working.
				if !emittedSpeaking {
					emittedSpeaking = true
					p.speaking.Store(true)
					p.sendEvent(stateMsg{Type: "state", State: "speaking"})
				}
			case <-ctx.Done():
				return
			}
		}
		firstSentence = false
	}
}

// waitOutboundDrain waits until the outbound PCM channel is empty, meaning
// runSender has picked up all queued frames. It polls briefly with a timeout
// to avoid blocking forever.
func (p *Pipeline) waitOutboundDrain() {
	deadline := time.After(5 * time.Second)
	for {
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

	defer func() {
		p.speaking.Store(false)
		p.waitOutboundDrain()
		p.sendEvent(stateMsg{Type: "state", State: "listening"})
	}()

	sentences := make(chan string, 1)
	sentences <- text
	close(sentences)

	p.synthesizeSentences(p.ctx, sentences, time.Time{})
}

// greetingText returns the appropriate greeting based on call direction.
// For outbound calls it prefers greeting_outgoing, falling back to greeting.
func (p *Pipeline) greetingText() string {
	if p.direction == "outbound" && p.cfg.Pipeline.GreetingOutgoing != "" {
		return p.cfg.Pipeline.GreetingOutgoing
	}
	return p.cfg.Pipeline.Greeting
}
