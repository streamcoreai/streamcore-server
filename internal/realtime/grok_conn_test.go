package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/streamcoreai/server/internal/config"
)

// fakeGrok is a stand-in for the xAI Realtime endpoint. It records every
// client message and can push events back, so the connection lifecycle can be
// exercised without network access or an API key.
type fakeGrok struct {
	t        *testing.T
	server   *httptest.Server
	conn     *websocket.Conn
	received chan recordedMsg
	ready    chan struct{}
}

type recordedMsg struct {
	binary bool
	data   []byte
}

func newFakeGrok(t *testing.T) *fakeGrok {
	t.Helper()
	f := &fakeGrok{
		t:        t,
		received: make(chan recordedMsg, 64),
		ready:    make(chan struct{}),
	}

	upgrader := websocket.Upgrader{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.conn = conn
		close(f.ready)

		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			cp := make([]byte, len(data))
			copy(cp, data)
			select {
			case f.received <- recordedMsg{binary: msgType == websocket.BinaryMessage, data: cp}:
			default:
			}
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGrok) url() string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http")
}

// send pushes a server event to the client.
func (f *fakeGrok) send(t *testing.T, event map[string]any) {
	t.Helper()
	<-f.ready
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := f.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write event: %v", err)
	}
}

// nextJSON returns the next text message the client sent, decoded.
func (f *fakeGrok) nextJSON(t *testing.T) map[string]any {
	t.Helper()
	for {
		select {
		case msg := <-f.received:
			if msg.binary {
				continue
			}
			var out map[string]any
			if err := json.Unmarshal(msg.data, &out); err != nil {
				t.Fatalf("client sent invalid JSON: %v", err)
			}
			return out
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for a client message")
			return nil
		}
	}
}

// dialFake points the client at the fake server by overriding the endpoint
// for the duration of the test.
func dialFake(t *testing.T, f *fakeGrok, opts Options, h Handlers) *GrokClient {
	t.Helper()

	orig := grokDialURL
	grokDialURL = f.url()
	t.Cleanup(func() { grokDialURL = orig })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	client, err := NewGrokClient(ctx, baseConfig(), opts, h)
	if err != nil {
		t.Fatalf("dial fake grok: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestGrokSendsSessionUpdateOnConnect(t *testing.T) {
	f := newFakeGrok(t)
	dialFake(t, f, Options{}, Handlers{})

	msg := f.nextJSON(t)
	if msg["type"] != "session.update" {
		t.Fatalf("first message = %v, want session.update", msg["type"])
	}
}

// The greeting must be spoken, not answered. Seeding a user message would
// make the model reply to the greeting text instead of saying it.
func TestGrokSpeakDoesNotSeedUserMessage(t *testing.T) {
	f := newFakeGrok(t)
	client := dialFake(t, f, Options{}, Handlers{})

	f.nextJSON(t) // session.update

	if err := client.Speak("Hi, how can I help?"); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	msg := f.nextJSON(t)
	if msg["type"] != "response.create" {
		t.Fatalf("Speak sent %v, want response.create", msg["type"])
	}

	resp, ok := msg["response"].(map[string]any)
	if !ok {
		t.Fatalf("response is %T, want map", msg["response"])
	}
	instructions, _ := resp["instructions"].(string)
	if !strings.Contains(instructions, "Hi, how can I help?") {
		t.Errorf("greeting text missing from instructions: %q", instructions)
	}
}

func TestGrokSendsAudioAsBinaryFrames(t *testing.T) {
	f := newFakeGrok(t)
	client := dialFake(t, f, Options{}, Handlers{})

	pcm := []byte{0x01, 0x02, 0x03, 0x04}
	if err := client.SendAudio(pcm); err != nil {
		t.Fatalf("SendAudio: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-f.received:
			if !msg.binary {
				continue // session.update
			}
			if string(msg.data) != string(pcm) {
				t.Fatalf("audio payload = %v, want %v", msg.data, pcm)
			}
			return
		case <-deadline:
			t.Fatal("no binary audio frame received")
		}
	}
}

// Binary frames must reach OnAudio untouched — base64 decoding them, or
// routing them through the JSON path, would corrupt playback.
func TestGrokDeliversBinaryAudioToHandler(t *testing.T) {
	f := newFakeGrok(t)
	got := make(chan []byte, 1)
	dialFake(t, f, Options{}, Handlers{
		OnAudio: func(pcm []byte) {
			select {
			case got <- pcm:
			default:
			}
		},
	})

	<-f.ready
	want := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	if err := f.conn.WriteMessage(websocket.BinaryMessage, want); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	select {
	case pcm := <-got:
		if string(pcm) != string(want) {
			t.Errorf("OnAudio got %v, want %v", pcm, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnAudio never fired for a binary frame")
	}
}

func TestGrokRoutesTranscriptAndSpeechEvents(t *testing.T) {
	f := newFakeGrok(t)

	userFinal := make(chan string, 1)
	speechStarted := make(chan struct{}, 1)
	dialFake(t, f, Options{}, Handlers{
		OnUserTranscript: func(text string, final bool) {
			if final {
				select {
				case userFinal <- text:
				default:
				}
			}
		},
		OnSpeechStarted: func() {
			select {
			case speechStarted <- struct{}{}:
			default:
			}
		},
	})

	f.send(t, map[string]any{"type": "input_audio_buffer.speech_started"})
	select {
	case <-speechStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("OnSpeechStarted never fired — barge-in would not work")
	}

	f.send(t, map[string]any{
		"type":       "conversation.item.input_audio_transcription.completed",
		"transcript": "book a table",
	})
	select {
	case got := <-userFinal:
		if got != "book a table" {
			t.Errorf("final transcript = %q, want %q", got, "book a table")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("final user transcript never delivered")
	}
}

// A tool call must produce both the output item and a response.create, or the
// agent goes silent after running the tool.
func TestGrokToolCallReturnsOutputAndResumes(t *testing.T) {
	f := newFakeGrok(t)
	client := dialFake(t, f, Options{}, Handlers{
		OnToolCall: func(_ context.Context, name string, args json.RawMessage) (string, error) {
			return "sunny", nil
		},
	})
	_ = client

	f.nextJSON(t) // session.update

	f.send(t, map[string]any{
		"type":      "response.function_call_arguments.done",
		"name":      "weather.get",
		"call_id":   "call_123",
		"arguments": `{"city":"Auckland"}`,
	})

	msg := f.nextJSON(t)
	if msg["type"] != "conversation.item.create" {
		t.Fatalf("first post-tool message = %v, want conversation.item.create", msg["type"])
	}
	item, ok := msg["item"].(map[string]any)
	if !ok {
		t.Fatalf("item is %T, want map", msg["item"])
	}
	if item["type"] != "function_call_output" {
		t.Errorf("item.type = %v, want function_call_output", item["type"])
	}
	if item["call_id"] != "call_123" {
		t.Errorf("item.call_id = %v, want call_123", item["call_id"])
	}
	if item["output"] != "sunny" {
		t.Errorf("item.output = %v, want sunny", item["output"])
	}

	if msg := f.nextJSON(t); msg["type"] != "response.create" {
		t.Errorf("after tool output the client sent %v, want response.create", msg["type"])
	}
}

// A failing tool must still report back, otherwise the model waits forever on
// a call that will never return.
func TestGrokToolCallReportsErrors(t *testing.T) {
	f := newFakeGrok(t)
	dialFake(t, f, Options{}, Handlers{
		OnToolCall: func(_ context.Context, name string, args json.RawMessage) (string, error) {
			return "", context.DeadlineExceeded
		},
	})

	f.nextJSON(t) // session.update
	f.send(t, map[string]any{
		"type":    "response.function_call_arguments.done",
		"name":    "weather.get",
		"call_id": "call_err",
	})

	msg := f.nextJSON(t)
	item, ok := msg["item"].(map[string]any)
	if !ok {
		t.Fatalf("item is %T, want map", msg["item"])
	}
	output, _ := item["output"].(string)
	if !strings.Contains(strings.ToLower(output), "error") {
		t.Errorf("tool error not reported to the model: %q", output)
	}
}

func TestGrokMissingAPIKeyIsRejected(t *testing.T) {
	cfg := &config.Config{}
	cfg.Realtime.Provider = "grok"

	if _, err := NewClient(context.Background(), cfg, Options{}, Handlers{}); err == nil {
		t.Fatal("expected an error when [grok] api_key is unset")
	}
}

func TestUnknownRealtimeProviderIsRejected(t *testing.T) {
	cfg := &config.Config{}
	cfg.Realtime.Provider = "nope"

	if _, err := NewClient(context.Background(), cfg, Options{}, Handlers{}); err == nil {
		t.Fatal("expected an error for an unknown realtime provider")
	}
}
