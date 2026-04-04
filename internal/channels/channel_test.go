package channels

import "testing"

func TestSenderMatchesList(t *testing.T) {
	tests := []struct {
		name     string
		senderID string
		entries  []string
		want     bool
	}{
		{
			name:     "match by exact user id",
			senderID: "12345",
			entries:  []string{"12345"},
			want:     true,
		},
		{
			name:     "match compound sender by id rule",
			senderID: "12345|kryptonite2304",
			entries:  []string{"12345"},
			want:     true,
		},
		{
			name:     "match compound sender by username rule",
			senderID: "12345|kryptonite2304",
			entries:  []string{"@kryptonite2304"},
			want:     true,
		},
		{
			name:     "match compound sender by compound rule",
			senderID: "12345|kryptonite2304",
			entries:  []string{"12345|kryptonite2304"},
			want:     true,
		},
		{
			name:     "no match",
			senderID: "12345|kryptonite2304",
			entries:  []string{"@someone_else"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SenderMatchesList(tt.senderID, tt.entries); got != tt.want {
				t.Fatalf("SenderMatchesList(%q, %v) = %v, want %v", tt.senderID, tt.entries, got, tt.want)
			}
		})
	}
}
