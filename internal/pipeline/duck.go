package pipeline

import "log"

// Ducking attenuates the agent's outbound audio while the caller talks over
// it, instead of cutting straight to silence. A hard cut on every candidate
// barge-in makes backchannels ("mm-hm", "yeah") clip the agent mid-word; a
// duck is recoverable, so a false positive costs a brief dip in volume rather
// than a lost sentence.

// maybeStartDuck attenuates agent audio if it is not already ducked.
func (p *Pipeline) maybeStartDuck() {
	if p == nil || !p.speaking.Load() {
		return
	}
	if p.audioMuted.CompareAndSwap(false, true) {
		p.notifyDuckChange(true)
		log.Println("[duck] agent audio attenuated (caller speaking)")
	}
}

// maybeRestoreDuck returns agent audio to full volume.
func (p *Pipeline) maybeRestoreDuck() {
	if p == nil {
		return
	}
	if p.audioMuted.CompareAndSwap(true, false) {
		p.notifyDuckChange(false)
		log.Println("[duck] agent audio restored")
	}
}

// notifyDuckChange hands the new state to the sender goroutine. Non-blocking:
// the audio path must never stall on a duck notification, and the sender also
// reads audioMuted directly, so a dropped signal self-corrects on the next
// frame.
func (p *Pipeline) notifyDuckChange(ducked bool) {
	if p.duckChangeCh == nil {
		return
	}
	select {
	case p.duckChangeCh <- ducked:
	default:
	}
}
