package rag

import (
	"context"
	"testing"
)

func TestWithTimingRoundTrip(t *testing.T) {
	var tm Timing
	ctx := WithTiming(context.Background(), &tm)

	got := timingFrom(ctx)
	if got != &tm {
		t.Fatalf("timingFrom returned %p, want %p", got, &tm)
	}

	got.EmbedMs = 42
	got.QueryMs = 7
	if tm.EmbedMs != 42 || tm.QueryMs != 7 {
		t.Fatalf("writes through collector not visible: %+v", tm)
	}
}

func TestTimingFromAbsent(t *testing.T) {
	if got := timingFrom(context.Background()); got != nil {
		t.Fatalf("timingFrom on bare context = %v, want nil", got)
	}
}
