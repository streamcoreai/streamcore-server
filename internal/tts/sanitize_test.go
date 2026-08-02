package tts

import "testing"

func TestSanitizeForTTS(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "Hello there.", "Hello there."},
		{"bold", "The **2019 Nissan Leaf** is great.", "The 2019 Nissan Leaf is great."},
		{"bold_in_list", "1. **2019 Nissan Leaf 40G** - $20,880", "2019 Nissan Leaf 40G - $20,880"},
		{"dash_list", "- item one\n- item two", "item one\nitem two"},
		{"star_list", "* alpha\n* beta", "alpha\nbeta"},
		{"heading", "# Title\nbody", "Title\nbody"},
		{"link", "see [our site](https://example.com) for more", "see our site for more"},
		{"image", "![alt text](http://x/y.png)", "alt text"},
		{"inline_code", "use `git pull` now", "use git pull now"},
		{"code_fence", "```go\nfmt.Println()\n```", "fmt.Println()\n"},
		{"strike", "~~old~~ new", "old new"},
		{"snake_case_preserved", "user_name and file_path", "user_name and file_path"},
		{"hyphenated_preserved", "state-of-the-art", "state-of-the-art"},
		{"italic_star", "this is *very* nice", "this is very nice"},
		{"triple_star_bold", "***wow***", "wow"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeForTTS(tc.in)
			if got != tc.want {
				t.Errorf("SanitizeForTTS(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
