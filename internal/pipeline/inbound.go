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

	sttCallback := func(result stt.TranscriptResult) {
		ev := TranscriptEvent{Text: result.Text, Final: result.IsFinal}
		if result.IsFinal {
			ev.TurnStart = time.Now()
			// Sampled here, not in runAgent: the turn buffer may hold this
			// final for up to turnMergeMax, by which point the agent has
			// finished and the talking-over question can no longer be asked.
			ev.OverAgentSpeech = p.agentSpeechRecent()
			hasPartialText.Store(false)
			latestPartial.Store("text", "")
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
		}
		select {
		case p.transcriptCh <- ev:
		case <-p.ctx.Done():
		}
	}

	go p.runTurnBuffer()

	sttClient, err := stt.NewClient(p.ctx, p.cfg, sttCallback)
	if err != nil {
		log.Printf("[inbound] STT start error: %v", err)
		return
	}
	defer sttClient.Close()

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
			// Feed all audio to STT continuously
			data := audio.PCMToLinear16BytesInto(sttBuf, frame.Samples)
			sttBuf = data[:0]
			if err := sttClient.SendAudio(data); err != nil {
				if p.ctx.Err() == nil {
					log.Printf("[inbound] STT send error: %v", err)
				}
				return
			}

			// Barge-in detection with backchannel suppression.
			// Uses the fast bargeInVAD (60ms onset) for responsiveness.
			if *p.cfg.Pipeline.BargeIn {
				p.bargeInVAD.Process(frame.Samples)

				if bargeInPending {
					if time.Since(bargeInStart) >= backchannelWindow {
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
				} else if p.bargeInVAD.IsSpeaking() && p.speaking.Load() && hasPartialText.Load() {
					// Conditions met — start backchannel suppression window.
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
