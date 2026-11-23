package tts

import "testing"

func TestParseVoiceTag(t *testing.T) {
	cases := []struct {
		in        string
		wantTone  string
		wantClean string
	}{
		{"[warm] Thanks so much for calling.", "warm", "Thanks so much for calling."},
		{"[empathetic] I'm really sorry to hear that.", "empathetic", "I'm really sorry to hear that."},
		{"[Excited] That's wonderful news!", "excited", "That's wonderful news!"},
		{"Plain sentence with no tag.", "", "Plain sentence with no tag."},
		// Unknown bracket content must pass through untouched.
		{"[Context: something] stays.", "", "[Context: something] stays."},
		{"[12:30] is your booking time.", "", "[12:30] is your booking time."},
		{"", "", ""},
	}
	for _, c := range cases {
		vc, clean := ParseVoiceTag(c.in)
		if vc.Tone != c.wantTone || clean != c.wantClean {
			t.Errorf("ParseVoiceTag(%q) = (%q, %q), want (%q, %q)", c.in, vc.Tone, clean, c.wantTone, c.wantClean)
		}
		if c.wantTone != "" && vc.Speed == 0 {
			t.Errorf("ParseVoiceTag(%q) returned zero speed for known tag", c.in)
		}
	}
}

func TestStripVoiceTags(t *testing.T) {
	in := "[warm] Thanks for calling. [excited] We got you booked!"
	want := "Thanks for calling. We got you booked!"
	if got := StripVoiceTags(in); got != want {
		t.Errorf("StripVoiceTags = %q, want %q", got, want)
	}
	// Unknown brackets survive.
	keep := "Your appointment [tentative] is at three."
	if got := StripVoiceTags(keep); got != keep {
		t.Errorf("StripVoiceTags removed unknown bracket: %q", got)
	}
}

// All wrappers must expose the controls capability so a pipeline
// type-assertion reaches the provider through sanitizer + limiter layers.
func TestWrappersForwardControlsCapability(t *testing.T) {
	var c Client = &sanitizingClient{inner: &deepgramClient{}}
	if _, ok := c.(ControllableStreamer); !ok {
		t.Error("sanitizingClient must implement ControllableStreamer")
	}
	var l Client = &limitedClient{inner: &deepgramClient{}, limiter: newConcurrencyLimiter(1)}
	if _, ok := l.(ControllableStreamer); !ok {
		t.Error("limitedClient must implement ControllableStreamer")
	}
}

func TestVoiceTagFilterAcrossChunks(t *testing.T) {
	cases := []struct {
		chunks []string
		want   string
	}{
		{[]string{"[empathetic] I need", " your confirmation."}, "I need your confirmation."},
		{[]string{"[emp", "athetic]", " I need", " that."}, "I need that."},
		{[]string{"Sure. [", "warm] Thanks", "!"}, "Sure. Thanks!"},
		{[]string{"[Excited] Great", "!"}, "Great!"},
		// Brackets that are not tags pass through, held or not.
		{[]string{"Booked for [12:30", "] today."}, "Booked for [12:30] today."},
		{[]string{"Your slot [w", "eekday] is open."}, "Your slot [weekday] is open."},
		{[]string{"A trailing [w"}, "A trailing [w"},
	}
	for _, c := range cases {
		var f VoiceTagFilter
		got := ""
		for _, ch := range c.chunks {
			got += f.Feed(ch)
		}
		got += f.Flush()
		if got != c.want {
			t.Errorf("chunks %q = %q, want %q", c.chunks, got, c.want)
		}
	}
}
