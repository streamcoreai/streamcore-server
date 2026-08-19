package pipeline

import (
	"log"
	"strings"
	"time"
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
)

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
			wait := mergeWait(pending.Text, base)
			if elapsed := time.Since(turnBegan); elapsed+wait > turnMergeMax {
				if remaining := turnMergeMax - elapsed; remaining > 0 {
					wait = remaining
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
			if pending != nil {
				log.Printf("[turn-buffer] turn complete after %dms: %q",
					time.Since(turnBegan).Milliseconds(), pending.Text)
			}
			flush()
		}
	}
}
