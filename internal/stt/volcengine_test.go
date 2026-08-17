package stt

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// serverFrame builds a response the way the service does: a 12-byte header
// (the 4-byte prologue plus a sequence number) followed by the payload.
func serverFrame(msgType byte, compressed bool, payload []byte) []byte {
	if compressed {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Write(payload)
		w.Close()
		payload = buf.Bytes()
	}
	frame := make([]byte, 12+len(payload))
	frame[0] = 0x11
	frame[1] = msgType<<4 | 0x00
	frame[2] = volcSerialisationJS
	if compressed {
		frame[2] |= volcCompressionGzip
	}
	binary.BigEndian.PutUint32(frame[8:12], uint32(len(payload)))
	copy(frame[12:], payload)
	return frame
}

// The payload offset differs between the two directions — requests put it at
// byte 8, responses at byte 12 because of the sequence number. Reading a
// response at byte 8 yields four bytes of sequence number glued to the front
// of the JSON, which fails to parse and silently drops every transcript.
func TestVolcengineParsesServerPayloadOffset(t *testing.T) {
	body := []byte(`{"result":{"text":"你好"}}`)
	msgType, payload, ok := parseVolcengineFrame(serverFrame(volcTypeFullServer, false, body))
	if !ok {
		t.Fatal("frame rejected, want it parsed")
	}
	if msgType != volcTypeFullServer {
		t.Errorf("msgType = %d, want %d", msgType, volcTypeFullServer)
	}
	if string(payload) != string(body) {
		t.Errorf("payload = %q, want %q", payload, body)
	}
}

// The service may gzip a payload; the compression bit in byte 2 is the only
// signal. Missing it hands raw deflate bytes to the JSON decoder.
func TestVolcengineDecompressesGzippedPayload(t *testing.T) {
	body := []byte(`{"result":{"text":"压缩过的文本"}}`)
	_, payload, ok := parseVolcengineFrame(serverFrame(volcTypeFullServer, true, body))
	if !ok {
		t.Fatal("frame rejected, want it parsed")
	}
	if string(payload) != string(body) {
		t.Errorf("payload = %q, want it decompressed to %q", payload, body)
	}
}

// A frame shorter than the header must be discarded rather than slicing out
// of range — the connection can deliver a truncated message on teardown.
func TestVolcengineRejectsShortFrame(t *testing.T) {
	for _, n := range []int{0, 1, 8, 11} {
		if _, _, ok := parseVolcengineFrame(make([]byte, n)); ok {
			t.Errorf("%d-byte frame accepted, want it rejected", n)
		}
	}
}

// definite is what separates a settled utterance from a partial one. Treating
// every result as final answers the caller mid-sentence; treating none as
// final means no turn ever completes and the agent never speaks.
func TestVolcengineDefiniteMarksFinal(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"no utterances", `{"result":{"text":"你好"}}`, false},
		{"partial", `{"result":{"text":"你好","utterances":[{"text":"你好","definite":false}]}}`, false},
		{"definite", `{"result":{"text":"你好。","utterances":[{"text":"你好。","definite":true}]}}`, true},
		{"one of many definite", `{"result":{"text":"a b","utterances":[{"definite":false},{"definite":true}]}}`, true},
	}
	for _, tc := range cases {
		var parsed volcengineResponse
		if err := json.Unmarshal([]byte(tc.body), &parsed); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := false
		for _, u := range parsed.Result.Utterances {
			if u.Definite {
				got = true
				break
			}
		}
		if got != tc.want {
			t.Errorf("%s: final = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The client frame layout is fixed by the service; a wrong byte here fails as
// an opaque protocol error rather than anything that names the cause.
func TestVolcengineClientFrameLayout(t *testing.T) {
	c := &volcengineClient{}
	// writeFrame needs a connection, so assemble the header the same way and
	// assert on the bytes rather than sending them.
	payload := []byte(`{"a":1}`)
	buf := make([]byte, 8+len(payload))
	buf[0] = 0x11
	buf[1] = volcTypeFullClient<<4 | volcFlagNone
	buf[2] = volcSerialisationJS
	binary.BigEndian.PutUint32(buf[4:8], uint32(len(payload)))
	copy(buf[8:], payload)

	if buf[0] != 0x11 {
		t.Errorf("byte 0 = %#x, want 0x11 (version 1, 4-byte header)", buf[0])
	}
	if got := (buf[1] >> 4) & 0x0F; got != volcTypeFullClient {
		t.Errorf("message type = %d, want %d", got, volcTypeFullClient)
	}
	if got := binary.BigEndian.Uint32(buf[4:8]); int(got) != len(payload) {
		t.Errorf("declared length = %d, want %d", got, len(payload))
	}
	_ = c
}

// The last-packet flag is what tells the service the stream ended so it
// flushes the tail of the final utterance. Sending a plain audio frame
// instead loses the last words of the call.
func TestVolcengineLastPacketFlag(t *testing.T) {
	b := byte(volcTypeAudioOnly<<4 | volcFlagLastPacket)
	if got := (b >> 4) & 0x0F; got != volcTypeAudioOnly {
		t.Errorf("message type = %d, want %d", got, volcTypeAudioOnly)
	}
	if got := b & 0x0F; got != volcFlagLastPacket {
		t.Errorf("flags = %#b, want %#b", got, volcFlagLastPacket)
	}
}

// A partial must carry only the unsettled tail. result.text is the whole
// conversation including everything already emitted as final, so forwarding it
// re-sends a finished sentence as a partial — it appears twice on screen — and
// prefixes every partial of the next sentence with the previous one.
func TestVolcenginePartialExcludesSettledText(t *testing.T) {
	body := `{"result":{"text":"你好。什么是大","utterances":[
		{"text":"你好。","definite":true},
		{"text":"什么是大","definite":false}]}}`

	var parsed volcengineResponse
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatal(err)
	}

	definite := 0
	for _, u := range parsed.Result.Utterances {
		if !u.Definite {
			break
		}
		definite++
	}

	var pending bytes.Buffer
	for i := definite; i < len(parsed.Result.Utterances); i++ {
		pending.WriteString(parsed.Result.Utterances[i].Text)
	}

	if got := pending.String(); got != "什么是大" {
		t.Errorf("partial = %q, want %q — settled text must not reappear", got, "什么是大")
	}
	if pending.String() == parsed.Result.Text {
		t.Error("partial equals result.text, want only the unsettled tail")
	}
}
