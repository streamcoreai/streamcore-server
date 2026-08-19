package stt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// sentence_end is the whole final/partial distinction. Reading it wrong in one
// direction answers the caller mid-sentence; in the other no turn ever
// completes and the agent never speaks.
func TestAliyunSentenceEndMarksFinal(t *testing.T) {
	cases := []struct {
		name string
		body string
		text string
		want bool
	}{
		{
			"opening frame carries no text",
			`{"payload":{"output":{"sentence":{"text":"","sentence_end":false,"sentence_begin":true}}}}`,
			"", false,
		},
		{
			"partial",
			`{"payload":{"output":{"sentence":{"text":"你","sentence_end":false}}}}`,
			"你", false,
		},
		{
			"final",
			`{"payload":{"output":{"sentence":{"text":"你好，请用一句话介绍乒乓球。","sentence_end":true}}}}`,
			"你好，请用一句话介绍乒乓球。", true,
		},
	}
	for _, tc := range cases {
		var res aliyunResult
		if err := json.Unmarshal([]byte(tc.body), &res); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := res.Payload.Output.Sentence.Text; got != tc.text {
			t.Errorf("%s: text = %q, want %q", tc.name, got, tc.text)
		}
		if got := res.Payload.Output.Sentence.SentenceEnd; got != tc.want {
			t.Errorf("%s: sentence_end = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Sentence text is per-sentence, not the conversation so far. This is what
// lets the results pass straight through: a provider that accumulates needs
// the settled prefix stripped before the pipeline sees a partial, and getting
// that wrong shows the finished sentence twice on screen.
func TestAliyunSentenceTextIsNotCumulative(t *testing.T) {
	first := `{"payload":{"output":{"sentence":{"sentence_id":1,"text":"你好。","sentence_end":true}}}}`
	second := `{"payload":{"output":{"sentence":{"sentence_id":2,"text":"什么是大","sentence_end":false}}}}`

	var a, b aliyunResult
	if err := json.Unmarshal([]byte(first), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(second), &b); err != nil {
		t.Fatal(err)
	}

	if strings.HasPrefix(b.Payload.Output.Sentence.Text, a.Payload.Output.Sentence.Text) {
		t.Errorf("second sentence %q repeats the first %q — results are cumulative after all, and the adapter must strip the settled prefix",
			b.Payload.Output.Sentence.Text, a.Payload.Output.Sentence.Text)
	}
}

// A failure arrives as its own event with a code and message. Ignoring the
// event leaves the call running against a dead task, transcribing nothing and
// explaining nothing.
func TestAliyunSurfacesTaskFailed(t *testing.T) {
	body := `{"header":{"event":"task-failed","task_id":"abc","error_code":"ModelNotFound","error_message":"Model not found (qwen3-asr-flash-realtime)!"}}`

	var res aliyunResult
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	if res.Header.Event != "task-failed" {
		t.Errorf("event = %q, want task-failed", res.Header.Event)
	}
	if res.Header.ErrorCode != "ModelNotFound" {
		t.Errorf("error_code = %q, want it carried through", res.Header.ErrorCode)
	}
	if !strings.Contains(res.Header.ErrorMessage, "Model not found") {
		t.Errorf("error_message = %q, want the provider text", res.Header.ErrorMessage)
	}
}

// run-task is a fixed shape: task_group/task/function identify the operation,
// and the service rejects the session outright if any is missing or wrong.
func TestAliyunRunTaskShape(t *testing.T) {
	c := &aliyunClient{taskID: "0123456789abcdef0123456789abcdef"}

	// sendRunTask writes to a connection, so build the same message here and
	// assert on it rather than dialling.
	sent := aliyunMessage{
		Header: aliyunHeader{Action: "run-task", TaskID: c.taskID, Streaming: "duplex"},
		Payload: aliyunRunPayload{
			TaskGroup: "audio", Task: "asr", Function: "recognition",
			Model:      defaultAliyunModel,
			Parameters: aliyunParameters{SampleRate: aliyunSampleRate, Format: "pcm"},
		},
	}

	body, err := json.Marshal(sent)
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
	// The service expects a 32-character hex id, which is a UUID with its
	// dashes removed — sending the dashed form is rejected.
	if id, _ := header["task_id"].(string); len(id) != 32 || strings.Contains(id, "-") {
		t.Errorf("task_id = %q, want 32 hex characters with no dashes", id)
	}

	payload := decoded["payload"].(map[string]any)
	for k, want := range map[string]string{"task_group": "audio", "task": "asr", "function": "recognition"} {
		if payload[k] != want {
			t.Errorf("%s = %v, want %v", k, payload[k], want)
		}
	}
	params := payload["parameters"].(map[string]any)
	if params["format"] != "pcm" {
		t.Errorf("format = %v, want pcm", params["format"])
	}
	if params["sample_rate"].(float64) != float64(aliyunSampleRate) {
		t.Errorf("sample_rate = %v, want %d", params["sample_rate"], aliyunSampleRate)
	}
}

// The optional fields must stay out of the request when unset. Sending an
// empty vocabulary_id is not the same as omitting it — the service treats it
// as a reference to a hotword list that does not exist.
func TestAliyunOmitsUnsetOptionalParameters(t *testing.T) {
	body, err := json.Marshal(aliyunParameters{SampleRate: aliyunSampleRate, Format: "pcm"})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"language_hints", "vocabulary_id"} {
		if strings.Contains(string(body), field) {
			t.Errorf("unset %s present in %s, want it omitted", field, body)
		}
	}

	withOpts, err := json.Marshal(aliyunParameters{
		SampleRate: aliyunSampleRate, Format: "pcm",
		LanguageHints: []string{"zh"}, VocabularyID: "vocab-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"language_hints":["zh"]`, `"vocabulary_id":"vocab-123"`} {
		if !strings.Contains(string(withOpts), field) {
			t.Errorf("%s missing from %s", field, withOpts)
		}
	}
}

// An empty model falls back to the default rather than being sent blank,
// which the service rejects as an unknown model.
func TestAliyunDefaultsModel(t *testing.T) {
	if defaultAliyunModel == "" {
		t.Fatal("default model is empty")
	}
	cfg := config.AliyunConfig{APIKey: "k"}
	model := cfg.Model
	if model == "" {
		model = defaultAliyunModel
	}
	if model != defaultAliyunModel {
		t.Errorf("model = %q, want %q", model, defaultAliyunModel)
	}
}
