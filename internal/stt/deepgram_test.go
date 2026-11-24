package stt

import (
	"testing"

	msginterfaces "github.com/deepgram/deepgram-go-sdk/v3/pkg/api/listen/v1/websocket/interfaces"
)

func TestNova3LanguageStripsLocale(t *testing.T) {
	cases := map[string]string{
		"":      "en",
		"en":    "en",
		"en-US": "en",
		"en-IN": "en",
		"en-NZ": "en",
		"en-GB": "en",
		"en-ZA": "en",
		"es":    "es",
		"es-ES": "es",
		// Nova-3 transcribes Mandarin directly; routing it to "multi" returns
		// garbage rather than a worse-but-usable transcript.
		"zh":    "zh",
		"zh-CN": "zh",
		"zh-TW": "zh",
		"ja-JP": "multi",
		"ko-KR": "multi",
		"hi-IN": "multi",
		"mi-NZ": "multi",
	}
	for in, want := range cases {
		if got := nova3Language(in); got != want {
			t.Errorf("nova3Language(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeepgramCallbackDedupesRepeatedPartials(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{
		onResult: func(result TranscriptResult) {
			got = append(got, result)
		},
	}
	defer cb.Close(nil)

	if err := cb.Message(deepgramMessage("hello there", false, false)); err != nil {
		t.Fatal(err)
	}
	if err := cb.Message(deepgramMessage("hello there", false, false)); err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got[0].Text != "hello there" || got[0].IsFinal {
		t.Fatalf("got %+v, want one non-final hello there", got[0])
	}
}

func TestDeepgramCallbackUtteranceEndDoesNotPromotePartial(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{
		onResult: func(result TranscriptResult) {
			got = append(got, result)
		},
	}
	defer cb.Close(nil)

	if err := cb.Message(deepgramMessage("change the email address", false, false)); err != nil {
		t.Fatal(err)
	}
	if err := cb.UtteranceEnd(&msginterfaces.UtteranceEndResponse{}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d results, want only the partial", len(got))
	}
	if got[0].Text != "change the email address" || got[0].IsFinal {
		t.Fatalf("got %+v, want non-final transcript", got[0])
	}
}

func TestDeepgramCallbackSpeechStartedDoesNotClearFinalizedChunk(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{
		onResult: func(result TranscriptResult) {
			got = append(got, result)
		},
	}
	defer cb.Close(nil)

	if err := cb.Message(deepgramMessage("first finalized chunk", true, false)); err != nil {
		t.Fatal(err)
	}
	if err := cb.SpeechStarted(&msginterfaces.SpeechStartedResponse{}); err != nil {
		t.Fatal(err)
	}
	if err := cb.UtteranceEnd(&msginterfaces.UtteranceEndResponse{}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d results, want partial and final", len(got))
	}
	if got[1].Text != "first finalized chunk" || !got[1].IsFinal {
		t.Fatalf("got final %+v, want preserved finalized chunk", got[1])
	}
}

func TestMergeTranscriptChunksDedupesOrdinaryOverlap(t *testing.T) {
	got := mergeTranscriptChunks("I would like a booking", "booking for tomorrow")
	want := "I would like a booking for tomorrow"
	if got != want {
		t.Fatalf("mergeTranscriptChunks ordinary overlap = %q, want %q", got, want)
	}
}

func TestMergeTranscriptChunksPreservesRepeatedSpokenPhoneDigit(t *testing.T) {
	got := mergeTranscriptChunks("o two one", "one two three four five six seven")
	want := "o two one one two three four five six seven"
	if got != want {
		t.Fatalf("mergeTranscriptChunks repeated spoken digit = %q, want %q", got, want)
	}
}

func deepgramMessage(text string, isFinal, speechFinal bool) *msginterfaces.MessageResponse {
	return deepgramMessageWithConfidence(text, isFinal, speechFinal, 0)
}

func deepgramMessageWithConfidence(text string, isFinal, speechFinal bool, confidence float64) *msginterfaces.MessageResponse {
	return &msginterfaces.MessageResponse{
		Channel: msginterfaces.Channel{
			Alternatives: []msginterfaces.Alternative{
				{Transcript: text, Confidence: confidence},
			},
		},
		IsFinal:     isFinal,
		SpeechFinal: speechFinal,
	}
}

func TestDeepgramCallbackPropagatesFinalConfidence(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{
		onResult: func(r TranscriptResult) { got = append(got, r) },
	}
	defer cb.Close(nil)

	if err := cb.Message(deepgramMessageWithConfidence("hello world", true, true, 0.93)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].IsFinal {
		t.Fatalf("got %+v, want one final", got)
	}
	if got[0].Confidence != 0.93 {
		t.Fatalf("final confidence = %v, want 0.93", got[0].Confidence)
	}
}

func TestDeepgramCallbackPropagatesPartialConfidence(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{
		onResult: func(r TranscriptResult) { got = append(got, r) },
	}
	defer cb.Close(nil)

	if err := cb.Message(deepgramMessageWithConfidence("hello there", false, false, 0.71)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IsFinal {
		t.Fatalf("got %+v, want one partial", got)
	}
	if got[0].Confidence != 0.71 {
		t.Fatalf("partial confidence = %v, want 0.71", got[0].Confidence)
	}
}

func TestDeepgramCallbackMissingConfidenceFallsBackToZero(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{
		onResult: func(r TranscriptResult) { got = append(got, r) },
	}
	defer cb.Close(nil)

	if err := cb.Message(deepgramMessage("hello world", true, true)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].IsFinal {
		t.Fatalf("got %+v, want one final", got)
	}
	if got[0].Confidence != 0 {
		t.Fatalf("missing-confidence final = %v, want 0", got[0].Confidence)
	}
	if got[0].Text != "hello world" {
		t.Fatalf("text = %q, want preserved transcript", got[0].Text)
	}
}

// A final says which audio it came from: the span of every is_final segment
// merged into it, for both the speech_final and the UtteranceEnd paths.
func TestDeepgramCallbackFinalCarriesAudioSpan(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{onResult: func(r TranscriptResult) { got = append(got, r) }}
	defer cb.Close(nil)

	first := deepgramMessage("my number is", true, false)
	first.Start, first.Duration = 2.0, 1.2
	second := deepgramMessage("four one five", true, true)
	second.Start, second.Duration = 3.2, 0.9
	cb.Message(first)
	cb.Message(second)
	var final *TranscriptResult
	for i := range got {
		if got[i].IsFinal {
			final = &got[i]
		}
	}
	near := func(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }
	if final == nil || !near(final.AudioStart, 2.0) || !near(final.AudioEnd, 4.1) {
		t.Fatalf("final span = %+v, want 2.0-4.1", final)
	}

	got = nil
	third := deepgramMessage("and then", true, false)
	third.Start, third.Duration = 6.0, 0.5
	cb.Message(third)
	cb.UtteranceEnd(&msginterfaces.UtteranceEndResponse{})
	if n := len(got); n == 0 || !got[n-1].IsFinal || !near(got[n-1].AudioStart, 6.0) || !near(got[n-1].AudioEnd, 6.5) {
		t.Fatalf("utterance-end final = %+v, want span 6.0-6.5", got)
	}
}

// A finalize the pipeline asked for ends the utterance, whether Deepgram
// answers with the last words or with nothing new.
func TestDeepgramCallbackFinalizeEndsUtterance(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{onResult: func(r TranscriptResult) { got = append(got, r) }}
	defer cb.Close(nil)

	seg := deepgramMessage("what's the capital", true, false)
	seg.Start, seg.Duration = 1, 1
	cb.Message(seg)
	tail := deepgramMessage("of France", true, false)
	tail.FromFinalize, tail.Start, tail.Duration = true, 2, 0.6
	cb.Message(tail)
	if n := len(got); n == 0 || !got[n-1].IsFinal || got[n-1].Text != "what's the capital of France" {
		t.Fatalf("finalize with text: %+v", got)
	}

	got = nil
	cb.Message(deepgramMessage("tell me a joke", true, false))
	empty := deepgramMessage("", true, false)
	empty.FromFinalize = true
	cb.Message(empty)
	if n := len(got); n == 0 || !got[n-1].IsFinal || got[n-1].Text != "tell me a joke" {
		t.Fatalf("empty finalize: %+v", got)
	}
}

// The span is the words, not the chunk: a short "stop" in a long segment must
// not be measured mostly as the silence before it.
func TestDeepgramCallbackSpanUsesWordTimes(t *testing.T) {
	var got []TranscriptResult
	cb := &deepgramCallback{onResult: func(r TranscriptResult) { got = append(got, r) }}
	defer cb.Close(nil)

	m := deepgramMessage("Stop.", true, true)
	m.Start, m.Duration = 10, 3
	m.Channel.Alternatives[0].Words = []msginterfaces.Word{{Word: "stop", Start: 12.4, End: 12.8}}
	cb.Message(m)
	if len(got) != 1 || got[0].AudioStart != 12.4 || got[0].AudioEnd != 12.8 {
		t.Fatalf("span = %+v, want the word's 12.4-12.8", got)
	}
}
