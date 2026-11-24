package pipeline

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/streamcoreai/streamcore-server/internal/audio"
	"github.com/streamcoreai/streamcore-server/internal/stt"
	"github.com/streamcoreai/streamcore-server/internal/vad"
)

// runReader reads RTP packets from the remote WebRTC track, decodes Opus
// to PCM, and pushes frames into inPCMCh.
func (p *Pipeline) runReader() {
	buf := make([]byte, 1500)
	var frameCount uint64
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		n, _, err := p.remoteTrack.Read(buf)
		if err != nil {
			if p.ctx.Err() == nil {
				log.Printf("[reader] track read error: %v", err)
			}
			return
		}

		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}

		pcm, err := p.decoder.Decode(pkt.Payload)
		if err != nil {
			log.Printf("[reader] opus decode error (payload %d bytes): %v", len(pkt.Payload), err)
			continue
		}

		// Diagnostic: log PCM levels for first frames and periodically
		frameCount++
		if frameCount <= 5 || frameCount%100 == 0 {
			var maxAbs int16
			for _, s := range pcm {
				if s < 0 && -s > maxAbs {
					maxAbs = -s
				} else if s > maxAbs {
					maxAbs = s
				}
			}
			log.Printf("[reader] frame=%d payload=%d pcm_samples=%d max_abs=%d ssrc=%d seq=%d",
				frameCount, len(pkt.Payload), len(pcm), maxAbs, pkt.SSRC, pkt.SequenceNumber)
		}

		select {
		case p.inPCMCh <- PCMFrame{Samples: pcm}:
		case <-p.ctx.Done():
			return
		}
	}
}

// runInbound consumes decoded PCM frames from inPCMCh, feeds them to the
// STT provider, and runs barge-in detection via the energy-based VAD.
func (p *Pipeline) runInbound() {
	// hasPartialText tracks whether STT has recognized actual words during
	// the current speech segment. Barge-in only fires when both VAD detects
	// sustained energy AND STT confirms real speech — preventing false
	// interrupts from noise, coughs, or keyboard clicks.
	var hasPartialText atomic.Bool

	// latestPartial stores the most recent STT partial text (lowercased,
	// trimmed) for backchannel detection.
	var latestPartial sync.Map // key: "text", value: string

	// lastFinal is the most recent final's text (lowercased) and arrival time.
	// With short endpointing a "stop" spoken over the agent is often
	// finalised before any partial reaches the barge-in check, and a final
	// clears latestPartial; barge-in reads this so the words still count.
	var lastFinal atomic.Value // bargeInFinal

	sttCallback := func(result stt.TranscriptResult) {
		ev := TranscriptEvent{Text: result.Text, Final: result.IsFinal}
		if result.IsFinal {
			if span, caller, bg := p.speaker.background(result, time.Now()); bg {
				log.Printf("[inbound] background speech ignored (level %.0f, caller %.0f): %q", span, caller, result.Text)
				return
			}
			ev.TurnStart = time.Now()
			if at := p.callerVoiceAt.Load(); at != 0 {
				ev.SpeechEndAt = time.Unix(0, at)
			}
			// Sampled here, not in runAgent: the turn buffer may hold this
			// final for up to turnMergeMax, by which point the agent has
			// finished and the talking-over question can no longer be asked.
			ev.OverAgentSpeech = p.agentSpeechRecent()
			hasPartialText.Store(false)
			latestPartial.Store("text", "")
			lastFinal.Store(bargeInFinal{text: strings.ToLower(strings.TrimSpace(result.Text)), at: time.Now()})
			// The utterance is over, so a tool that fired on it may fire again
			// on the next thing said.
			p.reflex.endUtterance()
			p.storeLastUserConfidence(result.Confidence)
			// Finals go to the turn buffer, which merges a caller's
			// mid-sentence pauses into one turn before the agent responds.
			select {
			case p.finalCh <- ev:
			case <-p.ctx.Done():
			}
			return
		}
		{
			trimmed := strings.ToLower(strings.TrimSpace(result.Text))
			latestPartial.Store("text", trimmed)
			if len(trimmed) >= 2 {
				// Require at least 2 non-whitespace characters to count as real
				// speech. Single-char noise artifacts ("uh", "m") are ignored.
				hasPartialText.Store(true)
			}
			// Before the turn buffer, before the model. A manifest that
			// declared on_partial gets its packet out now; everything else
			// carries on unchanged.
			p.reflexOnPartial(trimmed)
		}
		select {
		case p.transcriptCh <- ev:
		case <-p.ctx.Done():
		}
	}

	go func() { defer p.recoverPanic("runTurnBuffer"); p.runTurnBuffer() }()

	sttClient, err := stt.NewClient(p.ctx, p.cfg, sttCallback)
	if err != nil {
		log.Printf("[inbound] STT start error: %v", err)
		return
	}
	// A closure, so whichever client is current when the call ends is closed.
	defer func() { sttClient.Close() }()

	// Finals-only providers never confirm speech with partial text
	// (issue #75). Asserted once here: a provider that does not implement
	// stt.PartialsEmitter keeps emitsPartials true and the partials-driven
	// path below exactly as it was.
	emitsPartials := true
	if ep, ok := sttClient.(stt.PartialsEmitter); ok {
		emitsPartials = ep.EmitsPartials()
	}

	// callerPeakAt is the last speech frame loud enough to be the caller.
	var callerPeakAt time.Time

	// The provider is asked to finalise once per stretch of caller speech,
	// after the caller has been quiet a moment; finalizedFor is the
	// callerVoiceAt that request was for.
	finalizer, _ := sttClient.(stt.Finalizer)
	var finalizedFor int64

	// Backchannel suppression state machine
	var bargeInPending bool
	var bargeInStart time.Time
	const backchannelWindow = 600 * time.Millisecond

	// Reusable conversion buffer — one per call instead of one per 20ms
	// frame. Safe: every SendAudio implementation writes the bytes out (or
	// copies them) before returning.
	sttBuf := make([]byte, 0, audio.FrameSize*2)

	for {
		select {
		case <-p.ctx.Done():
			return
		case frame := <-p.inPCMCh:
			// Caller speech for turn timing and the quiet gate, tracked
			// whether or not barge-in is on. On ended the hangover is backed
			// out, so the stamp is the last voiced frame rather than the
			// moment the detector gave up waiting for more.
			// Only the caller's speech stamps; the room talking is not the
			// caller still talking (see foreground.go). Loudness is judged per
			// stretch of speech, not per frame: soft syllables sit well below
			// the caller's level, so a per-frame test would see the caller
			// stop between words.
			now := time.Now()
			rms := vad.RMSEnergy(frame.Samples)
			p.speaker.frame(rms)
			_, ended := p.vad.Process(frame.Samples)
			if p.vad.IsSpeaking() && p.speaker.near(rms, now) {
				callerPeakAt = now
			}
			nearCaller := !callerPeakAt.IsZero() && now.Sub(callerPeakAt) < callerSegmentWindow
			if nearCaller && ended {
				p.callerVoiceAt.Store(now.Add(-p.vad.Hangover()).UnixNano())
			} else if nearCaller && p.vad.IsSpeaking() {
				p.callerVoiceAt.Store(now.UnixNano())
			}

			// The caller has stopped but the provider has not finalised:
			// in a noisy room it never hears the silence it waits for.
			if at := p.callerVoiceAt.Load(); finalizer != nil && at != 0 && at != finalizedFor &&
				hasPartialText.Load() && now.Sub(time.Unix(0, at)) >= callerFinalizeAfter {
				finalizedFor = at
				if err := finalizer.Finalize(); err != nil && p.ctx.Err() == nil {
					log.Printf("[inbound] STT finalize: %v", err)
				}
			}

			// Feed all audio to STT continuously
			data := audio.PCMToLinear16BytesInto(sttBuf, frame.Samples)
			sttBuf = data[:0]
			if err := sttClient.SendAudio(data); err != nil {
				if p.ctx.Err() != nil {
					return
				}
				// A provider socket that drops mid-call used to end
				// recognition for the rest of the call: the caller kept
				// talking to an agent that could no longer hear them.
				log.Printf("[inbound] STT send error: %v; reconnecting", err)
				next := p.reconnectSTT(sttCallback)
				if next == nil {
					return
				}
				sttClient.Close()
				sttClient = next
				finalizer, _ = sttClient.(stt.Finalizer)
				p.speaker.newConnection()
				continue
			}

			// Barge-in detection with backchannel suppression.
			// Uses the fast bargeInVAD (60ms onset) for responsiveness.
			if *p.cfg.Pipeline.BargeIn {
				p.bargeInVAD.Process(frame.Samples)

				if bargeInPending {
					// A partial that already reads as a real interruption ends
					// in a barge-in whichever branch below catches it: past the
					// window unconditionally, or on speech end via the
					// meaningful-transcript check. Waiting out the window only
					// keeps the agent talking over the caller for the rest of
					// it. The window still does its job for backchannels, which
					// never read as meaningful. The readback guard keeps its
					// say: mid-readback only a strong command fires early.
					partialNow, _ := latestPartial.Load("text")
					partialNowStr, _ := partialNow.(string)
					if f, ok := lastFinal.Load().(bargeInFinal); ok && f.within(bargeInStart, time.Now()) {
						partialNowStr = strings.TrimSpace(f.text + " " + partialNowStr)
					}
					guarded := p.readbackBargeInGuardEnabled() && p.readbackInProgress()
					if isStrongBargeInCommand(partialNowStr) || (!guarded && isMeaningfulBargeInTranscript(partialNowStr)) {
						log.Printf("[inbound] barge-in on transcript: %q", partialNowStr)
						bargeInPending = false
						p.maybeRestoreDuck()
						p.bargeInVAD.Reset()
						select {
						case p.interruptCh <- struct{}{}:
						default:
						}
					} else if time.Since(bargeInStart) >= backchannelWindow {
						// Speech continued past the suppression window — real interruption.
						log.Println("[inbound] barge-in confirmed (speech > 600ms)")
						bargeInPending = false
						p.maybeRestoreDuck()
						p.bargeInVAD.Reset()
						select {
						case p.interruptCh <- struct{}{}:
						default:
						}
					} else if !p.bargeInVAD.IsSpeaking() {
						// Speech ended within the window — check for backchannel.
						// A finals-only provider has no partial text here, so
						// every burst that ends inside the window classifies as
						// backchannel: with no text, there is no basis to cut
						// the agent off mid-word.
						partial, _ := latestPartial.Load("text")
						partialStr, _ := partial.(string)
						if !isMeaningfulBargeInTranscript(partialStr) {
							log.Printf("[inbound] backchannel suppressed: %q", partialStr)
							bargeInPending = false
							hasPartialText.Store(false)
							p.maybeRestoreDuck()
						} else if p.readbackBargeInGuardEnabled() && p.readbackInProgress() &&
							!isStrongBargeInCommand(partialStr) {
							// Mid-readback, a weak correction is usually the
							// caller agreeing along. Only an explicit command
							// (stop, cancel, hang up) cuts the readback off.
							log.Printf("[inbound] readback guard held barge-in: %q", partialStr)
							bargeInPending = false
							hasPartialText.Store(false)
							p.maybeRestoreDuck()
						} else {
							// Short but not a backchannel — fire immediately.
							log.Printf("[inbound] barge-in detected (short utterance: %q)", partialStr)
							bargeInPending = false
							p.bargeInVAD.Reset()
							select {
							case p.interruptCh <- struct{}{}:
							default:
							}
						}
					}
					// else: still speaking within window, keep waiting
				} else if nearCaller && p.bargeInVAD.IsSpeaking() && p.speaking.Load() && (!emitsPartials || hasPartialText.Load() || recentFinal(&lastFinal, time.Now())) {
					// Conditions met — start backchannel suppression window.
					// A finals-only provider opens the window on VAD alone,
					// since partial text never arrives to confirm the speech;
					// the interrupt still cannot fire until the window has
					// fully elapsed.
					bargeInPending = true
					bargeInStart = time.Now()
					hasPartialText.Store(false)
					// Duck rather than cut: the caller hears the agent lower
					// its voice immediately, and it recovers if this turns out
					// to be a backchannel.
					p.maybeStartDuck()
					log.Println("[inbound] barge-in candidate, checking for backchannel...")
				}
			}
		}
	}
}

// callerFinalizeAfter is how long the caller must have been quiet before the
// STT is asked to finalise what it has. In a quiet room Deepgram usually
// finalises first on its own; this matters when background speech means it
// never hears a pause.
const callerFinalizeAfter = 300 * time.Millisecond

// callerSegmentWindow is how recently a stretch of speech must have had a
// caller-level frame to count as the caller's. Syllable peaks come every
// 100-250ms; this also covers the VAD's 300ms hangover after the last one.
const callerSegmentWindow = 500 * time.Millisecond

// sttReconnectBackoff is the wait before each attempt to replace a dropped
// STT connection. About eight seconds in all: past that the provider is down,
// not blipping, and the call ends as it always did.
var sttReconnectBackoff = []time.Duration{
	250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second,
}

// reconnectSTT opens a fresh STT connection after the current one failed,
// or returns nil when the call ends or every attempt fails. Audio queues in
// inPCMCh meanwhile and goes to the new connection.
func (p *Pipeline) reconnectSTT(onResult func(stt.TranscriptResult)) stt.Client {
	for i, wait := range sttReconnectBackoff {
		select {
		case <-p.ctx.Done():
			return nil
		case <-time.After(wait):
		}
		c, err := stt.NewClient(p.ctx, p.cfg, onResult)
		if err == nil {
			log.Printf("[inbound] STT reconnected (attempt %d)", i+1)
			return c
		}
		log.Printf("[inbound] STT reconnect attempt %d failed: %v", i+1, err)
	}
	log.Printf("[inbound] STT gone after %d reconnect attempts", len(sttReconnectBackoff))
	return nil
}

// bargeInFinal is a final transcript as the barge-in check sees it.
type bargeInFinal struct {
	text string
	at   time.Time
}

// bargeInFinalSlack is how long before a barge-in window opened a final may
// have landed and still belong to it: the caller's first word can be
// finalised before the VAD has seen enough speech to open the window.
const bargeInFinalSlack = time.Second

func (f bargeInFinal) within(windowStart, now time.Time) bool {
	return f.text != "" && f.at.After(windowStart.Add(-bargeInFinalSlack)) && !f.at.After(now)
}

// recentFinal reports whether a final landed in the last bargeInFinalSlack.
// It stands in for partial text when opening a barge-in window.
func recentFinal(v *atomic.Value, now time.Time) bool {
	f, ok := v.Load().(bargeInFinal)
	return ok && f.text != "" && now.Sub(f.at) < bargeInFinalSlack
}
