package plugin

import (
	"testing"
	"time"
)

func TestNormalizePartial(t *testing.T) {
	cases := map[string]string{
		"  No, STOP! ":     "no stop",
		"Hold on...":       "hold on",
		"don't stop":       "don't stop",
		"the red mug, no—": "the red mug no",
		"":                 "",
		"!!!":              "",
	}
	for in, want := range cases {
		if got := NormalizePartial(in); got != want {
			t.Errorf("NormalizePartial(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPartialSpecValidate(t *testing.T) {
	dispatch := &DispatchSpec{Topic: "arm.command"}

	if err := (&PartialSpec{Match: []string{"stop"}}).validate("t", nil); err == nil {
		t.Error("on_partial without dispatch must be rejected: a half-spoken sentence revises, and re-running a subprocess on each revision is not what the author meant")
	}
	if err := (&PartialSpec{}).validate("t", dispatch); err == nil {
		t.Error("on_partial with no phrases must be rejected")
	}
	if err := (&PartialSpec{Match: []string{"  "}}).validate("t", dispatch); err == nil {
		t.Error("an empty phrase must be rejected; it would match everything")
	}
	if err := (&PartialSpec{Match: []string{"stop"}}).validate("t", dispatch); err != nil {
		t.Errorf("valid spec rejected: %v", err)
	}
	if err := (*PartialSpec)(nil).validate("t", nil); err != nil {
		t.Errorf("a tool without on_partial must stay valid: %v", err)
	}
}

func TestManifestRejectsPartialWithoutDispatch(t *testing.T) {
	m := Manifest{
		Name: "broken",
		Exec: []string{"true"},
		Tools: []ToolSpec{{
			Name:      "broken.tool",
			OnPartial: &PartialSpec{Match: []string{"go"}},
		}},
	}
	if err := m.Validate(); err == nil {
		t.Fatal("a manifest whose partial-triggered tool runs a process must not load")
	}
}

func TestPartialSpecCaptureValidation(t *testing.T) {
	dispatch := &DispatchSpec{Topic: "arm.command"}

	spec := &PartialSpec{Capture: &CaptureSpec{After: []string{"grab the"}}}
	if err := spec.validate("t", dispatch); err == nil {
		t.Error("a capture with no arg must be rejected")
	}

	spec = &PartialSpec{Capture: &CaptureSpec{Arg: "label"}}
	if err := spec.validate("t", dispatch); err == nil {
		t.Error("a capture with no anchors must be rejected: nothing marks where the argument starts")
	}

	spec = &PartialSpec{Capture: &CaptureSpec{Arg: "label", After: []string{"  "}}}
	if err := spec.validate("t", dispatch); err == nil {
		t.Error("an empty anchor must be rejected")
	}

	spec = &PartialSpec{Match: []string{"stop"}, SettleMs: 250}
	if err := spec.validate("t", dispatch); err == nil {
		t.Error("settle_ms without capture must be rejected: there is nothing to wait for")
	}

	spec = &PartialSpec{Capture: &CaptureSpec{Arg: "label", After: []string{"no the"}}, SettleMs: 200}
	if err := spec.validate("t", dispatch); err != nil {
		t.Errorf("valid capture spec rejected: %v", err)
	}
}

func TestSettleDefaults(t *testing.T) {
	if got := (&PartialSpec{}).Settle(); got != DefaultSettleMs*time.Millisecond {
		t.Errorf("Settle() = %v, want the default", got)
	}
	if got := (&PartialSpec{SettleMs: 400}).Settle(); got != 400*time.Millisecond {
		t.Errorf("Settle() = %v, want 400ms", got)
	}
	if got := (*PartialSpec)(nil).Settle(); got != DefaultSettleMs*time.Millisecond {
		t.Errorf("a nil spec must still answer: %v", got)
	}
}
