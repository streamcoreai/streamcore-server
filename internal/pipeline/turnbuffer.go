package pipeline

import (
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// Turn-buffer tuning. A caller who pauses mid-sentence ("I want to... um...
// book a table") produces two finals from the STT provider. Answering the
// first half is the single most common way a voice agent feels like it is
// interrupting, so finals are held briefly and merged.
const (
	// turnMergeMax bounds how long a turn can be extended by repeated
	// continuations, so a caller who never pauses cleanly still gets an answer.
	turnMergeMax = 2500 * time.Millisecond
	// turnMergeIncomplete is the longer wait used when the text ends on a
	// dangling word ("and", "my number is") or mid-dictation — the caller is
	// clearly not finished.
	turnMergeIncomplete = 900 * time.Millisecond
	// turnMergeSpeakingMax bounds how long a turn can be held because the
	// caller is talking again. Their next final normally lands well inside
	// it and merges; this only stops a line that never goes quiet from
	// holding a turn forever.
	turnMergeSpeakingMax = 6 * time.Second
	// callerRecheck is how often a turn held for a talking caller looks again.
	callerRecheck = 50 * time.Millisecond
	// callerVoiceFresh is how recent the VAD's last voiced frame must be to
	// belong to the utterance a final describes. Older, and the VAD missed
	// this utterance (a caller below its threshold), so the hold falls back
	// to counting from the final.
	callerVoiceFresh = 3 * time.Second
	// callerTalking is how recently a voiced frame must have been seen for the
	// caller to count as still talking. The VAD refreshes the stamp every
	// frame while it hears speech, so this only needs to span a frame or two.
	callerTalking = 100 * time.Millisecond
)

// holdFrom turns a merge wait into how long to hold from now. A
// complete-looking turn's wait is caller silence, counted from their last
// voiced frame: provider endpointing has usually spent more than the whole
// wait before the final arrives, so the turn goes straight through. An
// incomplete-looking turn keeps the full wait from the final, as it always had.
func (p *Pipeline) holdFrom(wait, base time.Duration, now time.Time) time.Duration {
	if wait != base {
		return wait
	}
	at := p.callerVoiceAt.Load()
	if at == 0 {
		return wait + p.vadMissHold
	}
	since := now.Sub(time.Unix(0, at))
	if since > callerVoiceFresh {
		return wait + p.vadMissHold
	}
	return max(wait-since, 0)
}

// legacyEndpointing is the Deepgram endpointing the turn buffer's numbers were
// first tuned against.
const legacyEndpointing = 300 * time.Millisecond

// vadMissHoldFor is the extra hold for a turn the VAD did not hear (a caller
// below its threshold). Everything that keeps such a caller from being cut off
// mid-sentence is then down to provider endpointing plus the hold, so with
// endpointing shorter than it used to be the hold makes up the difference.
func vadMissHoldFor(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.STT.Provider != "deepgram" {
		return 0
	}
	ms, err := strconv.Atoi(strings.TrimSpace(cfg.Deepgram.Endpointing))
	if err != nil || ms < 0 {
		return 0
	}
	return max(legacyEndpointing-time.Duration(ms)*time.Millisecond, 0)
}

// callerStillTalking reports whether the VAD heard the caller in the last
// frame or two.
func (p *Pipeline) callerStillTalking(now time.Time) bool {
	at := p.callerVoiceAt.Load()
	return at != 0 && now.Sub(time.Unix(0, at)) < callerTalking
}

// mergeWait returns how long to hold a pending turn before answering it.
//
// Terminal punctuation wins over a dangling-looking tail word. The tail list
// exists to hold open utterances with no closer ("I already fixed it" — the
// caller is still going), but it also matches pronouns, so a sentence the
// provider punctuated as finished ("I already fixed it.") was taking the long
// incomplete window too. When the STT has already judged the thought complete,
// believe it; only an unpunctuated dangling tail extends the wait.
func mergeWait(text string, base time.Duration) time.Duration {
	if endsTerminal(text) {
		return base
	}
	if endsIncomplete(text) || endsDictation(text) {
		return turnMergeIncomplete
	}
	return base
}

// runTurnBuffer debounces final transcripts into whole turns. It waits a
// short window after each final; a further final inside that window is merged
// and the window restarts. The wait adapts to how the text ends:
//
//   - terminal punctuation ("...book a table.") — the caller sounds finished,
//     so the configured (short) window applies
//   - a dangling connective or dictation tail — extend, they are mid-thought
//
// While the window runs, retrieval is prefetched speculatively so the wait
// costs nothing on turns that do need context.
func (p *Pipeline) runTurnBuffer() {
	base := time.Duration(p.cfg.Pipeline.TurnMergeMs) * time.Millisecond
	if base <= 0 {
		base = 350 * time.Millisecond
	}

	var (
		pending    *TranscriptEvent
		timer      *time.Timer
		timerC     <-chan time.Time
		turnBegan  time.Time
		prefetched bool
	)
	stop := func() {
		if timer != nil {
			timer.Stop()
			timer, timerC = nil, nil
		}
	}
	arm := func(d time.Duration) {
		stop()
		timer = time.NewTimer(d)
		timerC = timer.C
	}
	flush := func() {
		if pending == nil {
			return
		}
		ev := *pending
		pending, prefetched = nil, false
		stop()
		if !turnBegan.IsZero() {
			ev.MergeWaitMs = time.Since(turnBegan).Milliseconds()
		}
		select {
		case p.transcriptCh <- ev:
		case <-p.ctx.Done():
		}
	}

	for {
		select {
		case <-p.ctx.Done():
			stop()
			return

		case ev := <-p.finalCh:
			text := strings.TrimSpace(ev.Text)
			if text == "" {
				continue
			}
			if pending == nil {
				merged := ev
				merged.Text = text
				pending = &merged
				turnBegan = time.Now()
			} else {
				pending.Text = strings.TrimSpace(pending.Text + " " + text)
				// Keep the earliest turn start: latency is measured from when
				// the caller first stopped speaking, not from the last chunk.
				//
				// The talking-over flag is sticky across the merge: a turn that
				// began over the agent's voice was an interruption regardless of
				// whether its later halves landed after the agent stopped.
				pending.OverAgentSpeech = pending.OverAgentSpeech || ev.OverAgentSpeech
			}

			// A caller still mid-thought gets a longer grace period.
			wait := p.holdFrom(mergeWait(pending.Text, base), base, time.Now())
			if elapsed := time.Since(turnBegan); elapsed+wait > turnMergeMax {
				if remaining := turnMergeMax - elapsed; remaining > 0 {
					wait = remaining
				} else if p.callerStillTalking(time.Now()) && elapsed < turnMergeSpeakingMax {
					// A long sentence arrives as several finals when
					// endpointing is short. Past the cap, keep merging
					// while the caller is still talking; the timer path
					// commits once they stop.
					wait = callerRecheck
				} else {
					flush()
					continue
				}
			}

			// Overlap retrieval with the wait rather than paying for it after.
			if !prefetched && p.cfg.Pipeline.RAGPrefetch {
				prefetched = true
				p.startRAGPrefetch(pending.Text)
			}
			arm(wait)

		case <-timerC:
			// A hesitation is a final followed by more speech. Answering
			// while the caller is talking again would answer half a
			// sentence; their next final merges into this turn instead.
			if pending != nil && p.callerStillTalking(time.Now()) && time.Since(turnBegan) < turnMergeSpeakingMax {
				arm(callerRecheck)
				continue
			}
			if pending != nil {
				log.Printf("[turn-buffer] turn complete after %dms: %q",
					time.Since(turnBegan).Milliseconds(), pending.Text)
			}
			flush()
		}
	}
}
