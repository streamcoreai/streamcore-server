package llm

import (
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

// TestToolCallDeltaIndex guards the streaming tool-call index against a nil
// Index pointer, which OpenAI-compatible backends/proxies can send and which
// would otherwise panic the call's goroutine mid-turn.
func TestToolCallDeltaIndex(t *testing.T) {
	if got := toolCallDeltaIndex(openai.ToolCall{}); got != 0 {
		t.Errorf("nil Index = %d, want 0", got)
	}
	for _, want := range []int{0, 1, 5} {
		idx := want
		if got := toolCallDeltaIndex(openai.ToolCall{Index: &idx}); got != want {
			t.Errorf("Index=%d => %d, want %d", want, got, want)
		}
	}
}

func assistantWithCalls(ids ...string) openai.ChatCompletionMessage {
	msg := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant}
	for _, id := range ids {
		msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{ID: id})
	}
	return msg
}

func toolResult(id string) openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{
		Role:       openai.ChatMessageRoleTool,
		Content:    "ok",
		ToolCallID: id,
	}
}

// checkWellFormed asserts the invariant OpenAI enforces: every assistant
// tool_call id is answered by a tool message that appears after it.
func checkWellFormed(t *testing.T, h []openai.ChatCompletionMessage) {
	t.Helper()
	answeredAt := make(map[string]int)
	for i, msg := range h {
		if msg.Role == openai.ChatMessageRoleTool {
			answeredAt[msg.ToolCallID] = i
		}
	}
	for i, msg := range h {
		if msg.Role != openai.ChatMessageRoleAssistant {
			continue
		}
		for _, tc := range msg.ToolCalls {
			at, ok := answeredAt[tc.ID]
			if !ok {
				t.Errorf("tool_call %q has no result", tc.ID)
			} else if at <= i {
				t.Errorf("tool_call %q answered at %d, before its assistant message at %d", tc.ID, at, i)
			}
		}
	}
}

// A turn cancelled mid-tool-execution leaves an assistant tool_calls message
// with no matching result. OpenAI rejects that outright, so every later turn
// in the call would fail until the history is patched.
func TestReconcileHistoryPatchesDanglingCalls(t *testing.T) {
	cases := []struct {
		name    string
		history []openai.ChatCompletionMessage
		want    int // expected length after reconciliation
	}{
		{
			name: "already complete — untouched",
			history: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem},
				assistantWithCalls("a"),
				toolResult("a"),
			},
			want: 3,
		},
		{
			name: "single dangling call",
			history: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem},
				assistantWithCalls("a"),
			},
			want: 3,
		},
		{
			name: "partially answered multi-call",
			history: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem},
				assistantWithCalls("a", "b"),
				toolResult("a"),
			},
			want: 4,
		},
		{
			name: "dangling call in an earlier round, later rounds intact",
			history: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem},
				assistantWithCalls("a"),
				{Role: openai.ChatMessageRoleUser, Content: "next turn"},
				assistantWithCalls("b"),
				toolResult("b"),
			},
			want: 6,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &openaiClient{history: tc.history}
			c.reconcileHistory()

			if len(c.history) != tc.want {
				t.Fatalf("history length = %d, want %d", len(c.history), tc.want)
			}
			checkWellFormed(t, c.history)

			// Reconciliation must be idempotent — a second pass adds nothing.
			c.reconcileHistory()
			if len(c.history) != tc.want {
				t.Errorf("second pass changed length to %d, want %d", len(c.history), tc.want)
			}
		})
	}
}
