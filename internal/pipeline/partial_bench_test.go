package pipeline

import (
	"testing"

	"github.com/streamcoreai/streamcore-server/internal/plugin"
)

// The reflex runs on every partial transcript, several times a second for the
// whole call, so its cost when nothing matches is the number that matters.
func BenchmarkReflexDisabled(b *testing.B) {
	p := &Pipeline{} // no manifest asked for it, so reflex is nil
	for i := 0; i < b.N; i++ {
		p.reflexOnPartial("i was thinking we could go on saturday instead")
	}
}

func BenchmarkReflexNoMatch(b *testing.B) {
	tool, err := plugin.NewDispatchTool(plugin.ToolSpec{
		Name:     "arm.pick",
		Dispatch: &plugin.DispatchSpec{Topic: "arm.command", Payload: map[string]any{"action": "pick"}},
		OnPartial: &plugin.PartialSpec{
			Capture: &plugin.CaptureSpec{Arg: "label", After: []string{
				"grab the", "grab", "pick up the", "pick up", "no the",
				"no not that the", "actually the", "i meant the"}},
			SettleMs: 250,
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	r := &partialReflex{fired: map[string]bool{}, firedAnchors: map[string]int{}, pending: map[string]*pendingCapture{}}
	match, startsWith, after := tool.Partial().Phrases()
	r.entries = append(r.entries, reflexEntry{tool: tool, match: match, startsWith: startsWith, after: after, arg: tool.Partial().Arg(), settle: tool.Partial().Settle()})
	p := &Pipeline{reflex: r}
	for i := 0; i < b.N; i++ {
		p.reflexOnPartial("i was thinking we could go on saturday instead")
	}
}
