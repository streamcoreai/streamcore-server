package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/streamcoreai/server/internal/audio"
	"github.com/streamcoreai/server/internal/llm"
	"github.com/streamcoreai/server/internal/realtime"
)

// ragToolName is the function the model calls to search the knowledge base.
// In the classic pipeline RAG context is injected into the LLM prompt before
// every turn; a speech-to-speech model has no prompt to inject into, so
// retrieval becomes a tool the model invokes when it decides it needs facts.
const ragToolName = "knowledge_search"

// ragToolParams is the JSON Schema for ragToolName's arguments.
const ragToolParams = `{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "The search query. Use the caller's own words where possible."
    }
  },
  "required": ["query"]
}`

// runRealtime is the realtime-mode counterpart to runInbound + runAgent. It
// pumps caller PCM to the speech-to-speech provider and pushes returned audio
// onto the outbound queue.
//
// Almost everything the classic path does between those two points — STT
// finals, the turn buffer, LLM streaming, sentence splitting, TTS — happens
// inside the provider instead, including turn detection and barge-in. What
// stays local is discarding already-buffered audio when the caller cuts in,
// since the provider cannot un-send frames sitting in outPCMCh.
func (p *Pipeline) runRealtime() {
	opts := realtime.Options{
		Tools:              p.realtimeTools(),
		InstructionsSuffix: p.realtimeInstructionsSuffix(),
		Debug:              p.cfg.Pipeline.Debug,
	}

	client, err := realtime.NewClient(p.ctx, p.cfg, opts, p.realtimeHandlers())
	if err != nil {
		log.Printf("[realtime] start error: %v", err)
		p.cancel()
		return
	}
	defer client.Close()

	defer p.realtimeAudio.close()
	go p.runRealtimeOutbound()

	p.realtimeMu.Lock()
	p.realtimeClient = client
	p.realtimeMu.Unlock()
	close(p.realtimeReady)

	p.sendEvent(stateMsg{Type: "state", State: "listening"})

	// Reusable conversion buffer, one per call rather than one per 20ms
	// frame. Safe because SendAudio writes the bytes out before returning.
	sendBuf := make([]byte, 0, audio.FrameSize*2)

	for {
		select {
		case <-p.ctx.Done():
			return
		case frame := <-p.inPCMCh:
			data := audio.PCMToLinear16BytesInto(sendBuf, frame.Samples)
			sendBuf = data[:0]
			if err := client.SendAudio(data); err != nil {
				if p.ctx.Err() == nil {
					log.Printf("[realtime] send error: %v", err)
				}
				return
			}
		}
	}
}

// realtimeTools collects everything the model may call: plugin tools, the
// car controls, and knowledge_search when RAG is configured.
func (p *Pipeline) realtimeTools() []realtime.ToolDefinition {
	var defs []realtime.ToolDefinition

	if p.pluginMgr != nil {
		for _, t := range p.pluginMgr.Tools() {
			defs = append(defs, realtime.ToolDefinition{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			})
		}
	}

	if p.ragClient != nil {
		defs = append(defs, realtime.ToolDefinition{
			Name:        ragToolName,
			Description: "Search the knowledge base for information needed to answer the caller. Call this whenever the answer depends on specific facts, documents, or details you were not given.",
			Parameters:  json.RawMessage(ragToolParams),
		})
	}

	if len(defs) > 0 {
		log.Printf("[realtime] registered %d tools", len(defs))
	}
	return defs
}

// realtimeInstructionsSuffix returns text appended to the provider's system
// prompt. Skills are markdown instructions loaded from the plugin directory;
// the classic path feeds them to the LLM via AppendSystemPrompt, and without
// this they would silently do nothing in realtime mode.
func (p *Pipeline) realtimeInstructionsSuffix() string {
	if p.pluginMgr == nil {
		return ""
	}
	skills := p.pluginMgr.SkillsPrompt()
	if skills == "" {
		return ""
	}
	log.Printf("[realtime] injected %d skills into system prompt", len(p.pluginMgr.Skills()))
	return skills
}

// realtimeHandlers maps provider events onto the pipeline's existing state,
// DataChannel events, and tool dispatch.
func (p *Pipeline) realtimeHandlers() realtime.Handlers {
	return realtime.Handlers{
		OnAudio: func(pcm []byte) {
			p.realtimeAudio.push(audio.Linear16BytesToPCM(pcm))
		},

		// Both transcription events feed the turn buffer and go out as
		// partials. The client SDK treats a final transcript as a committed
		// message, so emitting one per provider fragment would render a
		// single sentence as several "You" bubbles. The turn is committed
		// once, in OnResponseStarted.
		OnUserTranscript: func(text string, final bool) {
			var merged string
			if final {
				merged = p.realtimeTurn.complete(text)
			} else {
				merged = p.realtimeTurn.update(text)
			}
			p.sendEvent(transcriptMsg{Type: "transcript", Text: merged, Final: false})
		},

		// The provider has decided to answer, so the caller's turn is over
		// however many fragments it arrived in.
		OnResponseStarted: func() {
			text := p.realtimeTurn.close()
			if text == "" {
				return
			}
			p.sendEvent(transcriptMsg{Type: "transcript", Text: text, Final: true})
			log.Printf("[realtime] user: %s", text)
			if p.transcriptLog != nil {
				p.transcriptLog.Add("user", text)
			}
		},

		OnAgentTranscript: func(delta string) {
			p.sendEvent(responseMsg{Type: "response", Text: delta})
			prev, _ := p.lastAgentText.Load().(string)
			p.lastAgentText.Store(prev + delta)
		},

		OnSpeechStarted: func() {
			// The provider has already stopped generating, but audio it sent
			// beforehand is still buffered here and in the outbound queue.
			// Both have to go or the agent keeps talking over the caller.
			//
			// This runs unconditionally rather than only when p.speaking is
			// set: audio the model has produced may still be sitting in the
			// queue before the pump has dequeued its first frame, and gating
			// on "already audible" would let exactly that audio survive the
			// interruption and play afterwards. Discarding an empty queue
			// costs nothing.
			wasSpeaking := p.speaking.Swap(false)
			p.realtimeAudio.discard()
			p.drainOutbound()
			if wasSpeaking {
				log.Println("[realtime] barge-in — dropping queued audio")
			}
			p.sendEvent(stateMsg{Type: "state", State: "listening"})
		},

		OnSpeechStopped: func() {
			p.sendEvent(stateMsg{Type: "state", State: "thinking"})
		},

		OnResponseDone: func() {
			if txt, _ := p.lastAgentText.Load().(string); txt != "" {
				log.Printf("[realtime] agent: %s", txt)
				if p.transcriptLog != nil {
					p.transcriptLog.Add("assistant", txt)
				}
				p.lastAgentText.Store("")
			}
			// The model has finished generating, but playback is still
			// catching up. The outbound pump reports "listening" once the
			// buffered audio has actually been sent.
			p.realtimeAudio.endResponse()
		},

		OnToolCall: p.handleRealtimeToolCall,
	}
}

// runRealtimeOutbound paces buffered agent audio onto the outbound queue.
// It runs separately from the provider read goroutine so that blocking on a
// full outPCMCh never delays reading control events — see
// realtimeAudioQueue for why that matters.
func (p *Pipeline) runRealtimeOutbound() {
	for {
		chunk, ok := p.realtimeAudio.next()
		if !ok {
			return
		}

		if chunk.Frame != nil {
			// The "speaking" state is reported when audio actually reaches
			// the wire, not when the response started, so the client keeps
			// showing "thinking" while the model reasons.
			newTalkspurt := !p.speaking.Load()
			if newTalkspurt {
				p.speaking.Store(true)
				p.sendEvent(stateMsg{Type: "state", State: "speaking"})
			}

			select {
			case p.outPCMCh <- PCMFrame{Samples: chunk.Frame, NewTalkspurt: newTalkspurt}:
			case <-p.ctx.Done():
				return
			}
		}

		if chunk.EndOfResponse {
			p.speaking.Store(false)
			// Realtime mode has no response generations to supersede.
			p.waitOutboundEmpty(func() bool { return false })
			p.sendEvent(stateMsg{Type: "state", State: "listening"})
		}
	}
}

// handleRealtimeToolCall dispatches a model function call. It mirrors the
// classic path's handler so vision, car control, and plugins behave
// identically in both modes.
func (p *Pipeline) handleRealtimeToolCall(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}

	switch {
	case name == ragToolName:
		return p.realtimeRAGSearch(ctx, args)
	case name == visionToolName:
		return p.handleVisionToolCall(llm.ToolCall{Name: name, Arguments: args})
	case strings.HasPrefix(name, "car."):
		return p.handleCarToolCall(llm.ToolCall{Name: name, Arguments: args})
	}

	if p.pluginMgr == nil {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	tool, ok := p.pluginMgr.GetTool(name)
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	// The thinking sound is deliberately not played here. It writes straight
	// to outPCMCh, bypassing realtimeAudioQueue, so its tone frames would
	// interleave with model audio still draining through the outbound pump —
	// the model usually speaks before calling a tool, and that speech is
	// still in flight when the call lands. Pacing it through the queue
	// instead would need its own rate limiting, since push() never blocks.
	// A speech-to-speech model covers the same gap conversationally.
	if tool.ThinkingSound() {
		log.Printf("[realtime] tool %q requests a thinking sound; not supported in realtime mode", name)
	}

	return tool.Execute(args)
}

// realtimeRAGSearch runs a knowledge-base lookup on the model's behalf and
// returns the chunks as tool output.
func (p *Pipeline) realtimeRAGSearch(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parse %s arguments: %w", ragToolName, err)
	}
	if strings.TrimSpace(params.Query) == "" {
		return "No query supplied.", nil
	}

	chunks, err := p.ragClient.Search(ctx, params.Query, 0)
	if err != nil {
		log.Printf("[realtime] RAG search error: %v", err)
		return "The knowledge base is unavailable right now.", nil
	}
	if len(chunks) == 0 {
		return "No matching information found.", nil
	}
	return strings.Join(chunks, "\n---\n"), nil
}

// greetRealtimeWhenReady asks the model to open the conversation once the
// provider connection is up. Start() calls this before the dial completes, so
// it waits rather than racing it.
func (p *Pipeline) greetRealtimeWhenReady(text string) {
	select {
	case <-p.realtimeReady:
	case <-p.ctx.Done():
		return
	}

	p.realtimeMu.Lock()
	client := p.realtimeClient
	p.realtimeMu.Unlock()
	if client == nil {
		return
	}

	log.Printf("[realtime] greeting: %s", text)
	if err := client.Speak(text); err != nil {
		log.Printf("[realtime] greeting failed: %v", err)
	}
}
