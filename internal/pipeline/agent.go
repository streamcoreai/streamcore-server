package pipeline

import (
	"context"
	"log"
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
			go p.respond(ev.Text, ev.TurnStart)

		case <-p.interruptCh:
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
	}()

	p.speaking.Store(true)

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
	_, err := p.llmClient.Chat(respCtx, userText,
		func(chunk string) {
			if llmFirstToken && p.cfg.Pipeline.Debug && !turnStart.IsZero() {
				llmFirstToken = false
				p.sendEvent(timingMsg{
					Type:  "timing",
					Stage: "llm_first_token",
					Ms:    time.Since(turnStart).Milliseconds(),
				})
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
			case <-ctx.Done():
				return
			}
		}
		firstSentence = false
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
	p.speaking.Store(true)
	defer p.speaking.Store(false)

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
