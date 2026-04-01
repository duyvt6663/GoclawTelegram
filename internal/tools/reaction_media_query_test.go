package tools

import "testing"

func TestNormalizeReactionMediaQuery(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "pepe cute pleading", want: "cute pleading"},
		{in: "cat judging side eye", want: "side eye judging"},
		{in: "chill smoking capybara smug", want: "chill smug"},
		{in: "caught in 4k dog", want: "caught in 4k"},
		{in: "deployment meme", want: "deployment meme"},
	}

	for _, tt := range tests {
		if got := normalizeReactionMediaQuery(tt.in); got != tt.want {
			t.Fatalf("normalizeReactionMediaQuery(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
