package tts

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// The audio frames carry base64 linear16 PCM. A routing mistake here either
// plays the base64 text itself as noise or drops the chunk entirely.
func TestTelnyxRoutesAudioFrame(t *testing.T) {
	pcm := []byte{0x01, 0x02, 0x03, 0x04}
	frame := `{"audio":"` + base64.StdEncoding.EncodeToString(pcm) + `","cached":true,"isFinal":false,"text":null}`

	got, final, err := routeTelnyxFrame([]byte(frame))
	if err != nil {
		t.Fatalf("audio frame rejected: %v", err)
	}
	if final {
		t.Error("audio frame reported final, want it to be a chunk")
	}
	if string(got) != string(pcm) {
		t.Errorf("pcm = %v, want %v", got, pcm)
	}
}

// isFinal arrives only after the teardown and marks the end of the whole
// connection; treating any other frame as final would cut audio short.
func TestTelnyxRoutesFinalFrame(t *testing.T) {
	pcm, final, err := routeTelnyxFrame([]byte(`{"audio":null,"isFinal":true,"text":""}`))
	if err != nil {
		t.Fatalf("final frame rejected: %v", err)
	}
	if !final {
		t.Error("final frame not reported as final")
	}
	if pcm != nil {
		t.Errorf("final frame carried audio %v, want none", pcm)
	}
}

// The server interleaves cache-status notifications with audio: no audio
// payload, isFinal false. They must come back all zero so the caller can
// skip them: emitting one would surface an empty chunk mid-utterance.
func TestTelnyxSkipsCacheStatusFrame(t *testing.T) {
	pcm, final, err := routeTelnyxFrame([]byte(`{"audio":null,"cached":false,"isFinal":false,"text":"StreamCore is the layer that handles the media path."}`))
	if err != nil {
		t.Fatalf("cache-status frame rejected: %v", err)
	}
	if final {
		t.Error("cache-status frame reported final, want it skipped")
	}
	if pcm != nil {
		t.Errorf("cache-status frame produced audio %v, want none", pcm)
	}
}

// Error frames carry the failure in a plain error field. Swallowing them
// turns a failed synthesis into an utterance of silence, which reads
// downstream as the agent simply having nothing to say.
func TestTelnyxSurfacesErrorFrame(t *testing.T) {
	_, _, err := routeTelnyxFrame([]byte(`{"error":"synthesis failed for the requested voice"}`))
	if err == nil {
		t.Fatal("error frame not surfaced")
	}
	if !strings.Contains(err.Error(), "synthesis failed") {
		t.Errorf("error = %v, want it to carry the server message", err)
	}
}

// Undecodable audio must fail loudly rather than emit garbage bytes: a bad
// base64 chunk would play as noise on the live path.
func TestTelnyxRejectsBadBase64(t *testing.T) {
	if _, _, err := routeTelnyxFrame([]byte(`{"audio":"!!not base64!!","isFinal":false}`)); err == nil {
		t.Fatal("invalid base64 accepted, want a decode error")
	}
}

// A frame that is not JSON means the connection is misbehaving; continuing
// would silently drop whatever audio followed.
func TestTelnyxRejectsMalformedJSON(t *testing.T) {
	if _, _, err := routeTelnyxFrame([]byte(`{not json`)); err == nil {
		t.Fatal("malformed frame accepted, want a decode error")
	}
}

// The client frame layout is fixed by the service: the init frame is the
// required single-space opener that carries voice_speed, and the teardown
// must serialize an empty text field: the empty text is what makes the
// server flush and emit its final frame.
func TestTelnyxClientFrameWireFormat(t *testing.T) {
	init, err := json.Marshal(telnyxInitFrame{Text: " ", VoiceSettings: telnyxVoiceSettings{VoiceSpeed: 1.5}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(init), `{"text":" ","voice_settings":{"voice_speed":1.5}}`; got != want {
		t.Errorf("init frame = %s, want %s", got, want)
	}

	text, err := json.Marshal(telnyxTextFrame{Text: "hello", Flush: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(text), `{"text":"hello","flush":true}`; got != want {
		t.Errorf("text frame = %s, want %s", got, want)
	}

	teardown, err := json.Marshal(telnyxTeardownFrame{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(teardown), `{"text":""}`; got != want {
		t.Errorf("teardown frame = %s, want %s", got, want)
	}

	if got, want := string(telnyxForceJSON), `{"force":true}`; got != want {
		t.Errorf("force frame = %s, want %s", got, want)
	}
}

// Speed follows the Cartesia band: a delivery tag outside 0.8–1.2 must never
// reach the wire, where it would produce comedy-speed audio on a live call.
// Unset controls fall back to the configured baseline.
func TestTelnyxSpeedClamp(t *testing.T) {
	c := NewTelnyxClient("k", "", 0).(*TelnyxClient)

	cases := []struct {
		name string
		vc   VoiceControls
		want float64
	}{
		{"unset controls use the configured default", VoiceControls{}, 1.0},
		{"in-band control passes through", VoiceControls{Speed: 1.1}, 1.1},
		{"too slow clamps up", VoiceControls{Speed: 0.5}, 0.8},
		{"too fast clamps down", VoiceControls{Speed: 1.5}, 1.2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.resolveSpeed(tc.vc); got != tc.want {
				t.Errorf("resolveSpeed(%v) = %v, want %v", tc.vc, got, tc.want)
			}
		})
	}
}

// The configured baseline is clamped too: it feeds resolveSpeed's fallback,
// so an out-of-band config value would otherwise reach every utterance.
func TestTelnyxConfiguredSpeedIsClamped(t *testing.T) {
	c := NewTelnyxClient("k", "", 2.0).(*TelnyxClient)
	if got := c.resolveSpeed(VoiceControls{}); got != 1.2 {
		t.Errorf("resolveSpeed with baseline 2.0 = %v, want 1.2", got)
	}
}

// Empty voice and speed fall back rather than being sent blank: an empty
// voice fails the WebSocket handshake with an opaque 403.
func TestTelnyxDefaults(t *testing.T) {
	c := NewTelnyxClient("k", "", 0).(*TelnyxClient)
	if c.voice != defaultTelnyxVoice {
		t.Errorf("voice = %q, want %q", c.voice, defaultTelnyxVoice)
	}
	if c.voiceSpeed != 1.0 {
		t.Errorf("voiceSpeed = %v, want 1.0", c.voiceSpeed)
	}

	custom := NewTelnyxClient("k", "Telnyx.Najdi.Fahad", 1.1).(*TelnyxClient)
	if custom.voice != "Telnyx.Najdi.Fahad" || custom.voiceSpeed != 1.1 {
		t.Errorf("explicit values overwritten: %+v", custom)
	}
}
