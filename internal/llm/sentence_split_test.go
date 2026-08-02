package llm

import "testing"

func TestFindSentenceEnd(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		// No boundary yet.
		{"", -1},
		{"Hello there", -1},
		// Trailing dot is deferred (ambiguous mid-stream).
		{"Hello there.", -1},
		// Dot inside an email/domain is NOT a boundary — this is the bug fix:
		// the splitter must not cut "gmail.com" at the internal dot, which
		// stripped the spoken "dot" from the email readback.
		{"Email: jasonnzyc@gmail.com", -1},
		{"Email: jasonnzyc@gmail.com.", -1}, // trailing sentence dot deferred; email intact
		// Decimal numbers are not boundaries either.
		{"That's 39.99 dollars", -1},
		// A real boundary (dot followed by space) is found, email left intact.
		{"Email: jasonnzyc@gmail.com. Anything", len("Email: jasonnzyc@gmail.com")},
		// ? and ! always terminate.
		{"financing or paying in full?", len("financing or paying in full?") - 1},
		{"Great!", len("Great!") - 1},
		// Latest boundary wins; earlier sentence split, later token kept.
		{"First. Email me at a@b.io", len("First")},
	}
	for _, tc := range cases {
		if got := findSentenceEnd(tc.in); got != tc.want {
			t.Errorf("findSentenceEnd(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
