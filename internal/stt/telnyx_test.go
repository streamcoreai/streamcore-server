package stt

import (
	"strings"
	"testing"
)

// The in-house engine's final frame carries confidence null. A nil pointer
// must map to 0, which TranscriptResult defines as "unknown" — a naive
// dereference would panic, and a misread null would look like a failed
// recognition.
func TestTelnyxSTTFinalFrameMapsNullConfidence(t *testing.T) {
	frame := `{"transcript":" The quick brown fox jumps over the lazy dog, testing one, two, three.","confidence":null,"is_final":true}`

	result, emit, err := routeTelnyxSTTFrame([]byte(frame))
	if err != nil {
		t.Fatalf("final frame rejected: %v", err)
	}
	if !emit {
		t.Fatal("final frame not emitted")
	}
	if !result.IsFinal {
		t.Error("final frame reported partial, want final")
	}
	if result.Text != "The quick brown fox jumps over the lazy dog, testing one, two, three." {
		t.Errorf("text = %q, want the leading space trimmed", result.Text)
	}
	if result.Confidence != 0 {
		t.Errorf("confidence = %v, want 0 (null maps to unknown)", result.Confidence)
	}
}

// Hosted engines stream partials with a real confidence. Dropping the partial
// path would kill barge-in, which gates interruption on partial text.
func TestTelnyxSTTRoutesPartialFrame(t *testing.T) {
	frame := `{"transcript":"The quick brown","confidence":0.8442383,"is_final":false,"speech_final":false}`

	result, emit, err := routeTelnyxSTTFrame([]byte(frame))
	if err != nil {
		t.Fatalf("partial frame rejected: %v", err)
	}
	if !emit {
		t.Fatal("partial frame not emitted")
	}
	if result.IsFinal {
		t.Error("partial frame reported final, want partial")
	}
	if result.Text != "The quick brown" {
		t.Errorf("text = %q, want %q", result.Text, "The quick brown")
	}
	if result.Confidence != 0.8442383 {
		t.Errorf("confidence = %v, want 0.8442383", result.Confidence)
	}
}

// End-of-utterance markers arrive with an empty transcript. Emitting one
// would surface an empty final turn and re-answer the sentence before it.
func TestTelnyxSTTSkipsEmptyTranscript(t *testing.T) {
	_, emit, err := routeTelnyxSTTFrame([]byte(`{"transcript":"","is_final":true,"utterance_end":true}`))
	if err != nil {
		t.Fatalf("utterance-end frame rejected: %v", err)
	}
	if emit {
		t.Error("utterance-end frame emitted, want it skipped")
	}
}

// An unsupported engine is reported in a structured errors array, and the
// detail names the actual problem including the supported list. Surfacing
// only the code or title would leave an operator with "Invalid Parameter"
// and no idea what to fix.
func TestTelnyxSTTSurfacesErrorDetail(t *testing.T) {
	frame := `{"errors":[{"code":"40007","title":"Invalid Parameter","source":{"parameter":"transcription_engine"},"detail":"Unsupported transcription_engine 'telnyx'. Supported engines: AssemblyAI, Azure, Cohere, Deepgram, Google, Humain, Parakeet, Reson8, Soniox, Speechmatics, Telnyx, xAI"}]}`

	_, _, err := routeTelnyxSTTFrame([]byte(frame))
	if err == nil {
		t.Fatal("error frame not surfaced")
	}
	if !strings.Contains(err.Error(), "Unsupported transcription_engine 'telnyx'") {
		t.Errorf("error = %v, want it to carry the server detail", err)
	}
	if !strings.Contains(err.Error(), "Telnyx") {
		t.Errorf("error = %v, want the supported-engine list to survive", err)
	}
}

// A frame that is not JSON means the connection is misbehaving; continuing
// would silently drop whatever transcript followed.
func TestTelnyxSTTRejectsMalformedJSON(t *testing.T) {
	if _, _, err := routeTelnyxSTTFrame([]byte(`{not json`)); err == nil {
		t.Fatal("malformed frame accepted, want a decode error")
	}
}

// The engine rides the dial URL as a query parameter, so it must be encoded;
// and interim_results decides whether barge-in works at all. The in-house
// engine ignores the parameter, so it is not sent there; every hosted engine
// streams partials only when it is present.
func TestTelnyxSTTEndpointBuildsQuery(t *testing.T) {
	cases := []struct {
		name   string
		engine string
		want   string
	}{
		{
			"in-house engine, no interims",
			"Telnyx",
			telnyxSTTURL + "?transcription_engine=Telnyx&input_format=linear16&sample_rate=16000",
		},
		{
			"hosted engine gets interims",
			"Deepgram",
			telnyxSTTURL + "?transcription_engine=Deepgram&input_format=linear16&sample_rate=16000&interim_results=true",
		},
		{
			"engine value is URL-encoded",
			"Two Words",
			telnyxSTTURL + "?transcription_engine=Two+Words&input_format=linear16&sample_rate=16000&interim_results=true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := telnyxSTTEndpoint(tc.engine); got != tc.want {
				t.Errorf("endpoint = %q, want %q", got, tc.want)
			}
		})
	}
}

// After the in-house engine's single final, SendAudio must refuse rather
// than pretend to accept audio, and the message must point at the fix —
// "connection closed" would read as a network failure when the connection
// is behaving exactly as documented.
func TestTelnyxSTTSendAudioReportsSpentSession(t *testing.T) {
	c := &TelnyxClient{closed: true, closeReason: telnyxFinalSpentReason}

	err := c.SendAudio([]byte{0x00, 0x01})
	if err == nil {
		t.Fatal("send after close accepted, want the spent-session reason")
	}
	if !strings.Contains(err.Error(), "stt_engine") {
		t.Errorf("error = %v, want it to point at the stt_engine fix", err)
	}
}
