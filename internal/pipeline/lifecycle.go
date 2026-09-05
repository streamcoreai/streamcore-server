package pipeline

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/plugin"
)

// lifecycleEventTimeout bounds all handlers for one event. Individual plugins
// may use tighter deadlines; this process-wide ceiling keeps a misbehaving
// native handler from retaining a goroutine indefinitely.
const lifecycleEventTimeout = 5 * time.Second

// emitAssistantResponseCompleted dispatches the final result of one logical
// assistant turn. It is called after response synthesis has settled, before the
// caller waits for outbound audio to drain, and performs no LLM/TTS work on the
// response goroutine.
func (p *Pipeline) emitAssistantResponseCompleted(gen uint64, transcript, response string) {
	if p.pluginMgr == nil || gen == 0 || !p.claimLifecycleEvent(gen) {
		return
	}
	if p.responseGen.Load() != gen || strings.TrimSpace(response) == "" {
		return
	}
	if p.conv == nil {
		return
	}

	seq, turnID := p.conv.NextTurn()
	p.latestLifecycleTurn.Store(seq)
	event := plugin.PluginEvent{
		Type:      plugin.AssistantResponseCompletedEvent,
		SessionID: p.conv.SessionID,
		TurnID:    turnID,
		TurnSeq:   seq,
		Data: plugin.AssistantResponseCompleted{
			Transcript:  transcript,
			Response:    response,
			Interrupted: false,
		},
	}

	go func() {
		defer p.recoverKeepAlive("dispatchPluginEvent")
		p.dispatchPluginEvent(gen, event, seq)
	}()
}

// claimLifecycleEvent records the highest response generation for which a
// completion event has been emitted. Superseded generations cannot claim, and a
// repeated call for the same generation cannot emit twice.
func (p *Pipeline) claimLifecycleEvent(gen uint64) bool {
	for {
		previous := p.lifecycleEmittedGen.Load()
		if previous >= gen {
			return false
		}
		if p.lifecycleEmittedGen.CompareAndSwap(previous, gen) {
			return true
		}
	}
}

// dispatchPluginEvent is a best-effort observer path. Errors, panics, and
// cancellations are logged and never propagate to the voice pipeline.
func (p *Pipeline) dispatchPluginEvent(gen uint64, event plugin.PluginEvent, seq uint64) {
	ctx, cancel := context.WithTimeout(p.ctx, lifecycleEventTimeout)
	defer cancel()

	// A barge-in or newer turn may have superseded this generation between the
	// completion snapshot and this goroutine being scheduled.
	if p.responseGen.Load() != gen || ctx.Err() != nil {
		return
	}

	outputs, err := p.pluginMgr.DispatchEvent(ctx, event)
	if err != nil {
		log.Printf("[lifecycle] %s handler error for %s: %v", event.Type, event.TurnID, err)
	}

	for _, output := range outputs {
		if output.TurnSeq == 0 {
			output.TurnSeq = seq
		}
		if output.TurnID == "" {
			output.TurnID = event.TurnID
		}
		if output.Type == "" || output.Payload == nil {
			continue
		}
		if output.TurnSeq < p.latestLifecycleTurn.Load() || p.responseGen.Load() != gen {
			continue
		}
		if err := p.sendEvent(output.Payload); err != nil {
			log.Printf("[lifecycle] send %s for %s: %v", output.Type, event.TurnID, err)
		}
	}
}
