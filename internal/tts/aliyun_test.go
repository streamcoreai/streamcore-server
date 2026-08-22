package tts

import (
	"encoding/json"
	"strings"
	"testing"
)

// run-task is a fixed shape, and the service rejects the session outright when
// any of task_group/task/function is missing or wrong — as an opaque protocol
// error that names none of them.
func TestAliyunTTSRunTaskShape(t *testing.T) {
	c := NewAliyunClient("k", "", "", "").(*aliyunClient)

	body, err := json.Marshal(aliyunMessage{
		Header: aliyunHeader{Action: "run-task", TaskID: "0123456789abcdef0123456789abcdef", Streaming: "duplex"},
		Payload: aliyunRunPayload{
			TaskGroup: "audio", Task: "tts", Function: "SpeechSynthesizer",
			Model: c.model,
			Parameters: aliyunTTSParameters{
				TextType: "PlainText", Voice: c.voice, Format: "pcm", SampleRate: aliyunSampleRate,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}

	header := decoded["header"].(map[string]any)
	if header["action"] != "run-task" {
		t.Errorf("action = %v, want run-task", header["action"])
	}
	if header["streaming"] != "duplex" {
		t.Errorf("streaming = %v, want duplex", header["streaming"])
	}
	// A 32-character hex id: a UUID with its dashes removed. The dashed form
	// is rejected.
	if id, _ := header["task_id"].(string); len(id) != 32 || strings.Contains(id, "-") {
		t.Errorf("task_id = %q, want 32 hex characters with no dashes", id)
	}

	payload := decoded["payload"].(map[string]any)
	// task is "tts" and function is "SpeechSynthesizer" — the ASR path on this
	// same endpoint uses "asr"/"recognition", and swapping one in leaves the
	// other looking plausible.
	for k, want := range map[string]string{
		"task_group": "audio", "task": "tts", "function": "SpeechSynthesizer",
	} {
		if payload[k] != want {
			t.Errorf("%s = %v, want %v", k, payload[k], want)
		}
	}

	params := payload["parameters"].(map[string]any)
	if params["format"] != "pcm" {
		t.Errorf("format = %v, want pcm", params["format"])
	}
	// The pipeline plays 16kHz linear16. This provider honours the request, so
	// a wrong rate here is not resampled anywhere — it plays back at the wrong
	// speed and pitch.
	if params["sample_rate"].(float64) != 16000 {
		t.Errorf("sample_rate = %v, want 16000", params["sample_rate"])
	}
	if params["text_type"] != "PlainText" {
		t.Errorf("text_type = %v, want PlainText", params["text_type"])
	}
}

// Text rides on continue-task under input.text. Putting it anywhere else is
// accepted by the service and synthesizes silence.
func TestAliyunTTSTextPayloadShape(t *testing.T) {
	var p aliyunTextPayload
	p.Input.Text = "你好"

	body, err := json.Marshal(aliyunMessage{
		Header:  aliyunHeader{Action: "continue-task", TaskID: "t", Streaming: "duplex"},
		Payload: p,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"payload":{"input":{"text":"你好"}}`) {
		t.Errorf("payload = %s, want text nested under input", body)
	}
}

// finish-task carries no text, and the empty input object must still be
// present — the field is not optional even when it holds nothing.
func TestAliyunTTSFinishTaskKeepsEmptyInput(t *testing.T) {
	body, err := json.Marshal(aliyunMessage{
		Header:  aliyunHeader{Action: "finish-task", TaskID: "t", Streaming: "duplex"},
		Payload: aliyunTextPayload{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"payload":{"input":{}}`) {
		t.Errorf("payload = %s, want an empty input object", body)
	}
	if strings.Contains(string(body), `"text"`) {
		t.Errorf("payload = %s, want no text field on finish-task", body)
	}
}

// A failure arrives as its own event carrying a code and message. Dropping it
// leaves the caller waiting on a channel that only ever closes empty, with
// nothing said about why.
func TestAliyunTTSSurfacesTaskFailed(t *testing.T) {
	body := `{"header":{"event":"task-failed","task_id":"abc","error_code":"InvalidParameter","error_message":"[cosyvoice:]Engine return error code: 418"}}`

	var res aliyunMessage
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	if res.Header.Event != "task-failed" {
		t.Errorf("event = %q, want task-failed", res.Header.Event)
	}
	if res.Header.ErrorCode != "InvalidParameter" {
		t.Errorf("error_code = %q, want it carried through", res.Header.ErrorCode)
	}
	if !strings.Contains(res.Header.ErrorMessage, "418") {
		t.Errorf("error_message = %q, want the provider text", res.Header.ErrorMessage)
	}
}

// Empty voice, model and url fall back rather than being sent blank, which the
// service rejects as an unknown model or voice.
func TestAliyunTTSDefaults(t *testing.T) {
	c := NewAliyunClient("k", "", "", "").(*aliyunClient)
	if c.model != defaultAliyunTTSModel {
		t.Errorf("model = %q, want %q", c.model, defaultAliyunTTSModel)
	}
	if c.voice != defaultAliyunVoice {
		t.Errorf("voice = %q, want %q", c.voice, defaultAliyunVoice)
	}
	if c.url != aliyunTTSURL {
		t.Errorf("url = %q, want %q", c.url, aliyunTTSURL)
	}

	custom := NewAliyunClient("k", "longwan_v2", "cosyvoice-v1", "wss://example/x").(*aliyunClient)
	if custom.voice != "longwan_v2" || custom.model != "cosyvoice-v1" || custom.url != "wss://example/x" {
		t.Errorf("explicit values overwritten: %+v", custom)
	}
}

// The session must not send after Close: gorilla allows a single writer, and a
// barge-in closes the connection while the send path may still be running.
func TestAliyunTTSSessionRejectsWriteAfterClose(t *testing.T) {
	s := &aliyunSession{closed: true}
	if err := s.writeJSON(aliyunMessage{Header: aliyunHeader{Action: "continue-task"}}); err == nil {
		t.Error("write on a closed session succeeded, want an error instead of touching a nil conn")
	}
}
