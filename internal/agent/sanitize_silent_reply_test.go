package agent

import "testing"

func TestIsSilentReply(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"exact token", "NO_REPLY", true},
		{"suffix punctuation", "NO_REPLY.", true},
		{"wrapped in parens", "(NO_REPLY)", true},
		{"wrapped in backticks", "`NO_REPLY`", true},
		{"nested wrappers", "((NO_REPLY))", true},
		{"quoted with spaces", ` "NO_REPLY" `, true},
		{"markdown emphasis", "*NO_REPLY*", true},
		{"empty", "", false},
		{"normal text", "hello", false},
		{"token inside sentence", "please NO_REPLY now", false},
		{"wrapped token inside sentence", "bot said (NO_REPLY) here", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsSilentReply(tt.in)
			if got != tt.want {
				t.Fatalf("IsSilentReply(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
