package telegram

import (
	"testing"
	"time"

	"github.com/mymmrac/telego"
)

func TestIsStaleTelegramMessage(t *testing.T) {
	now := time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC)
	cutoff := telegramUpdateCutoff(now)

	tests := []struct {
		name    string
		message *telego.Message
		want    bool
	}{
		{
			name: "older than one minute",
			message: &telego.Message{
				Date: now.Add(-61 * time.Second).Unix(),
			},
			want: true,
		},
		{
			name: "exactly at cutoff",
			message: &telego.Message{
				Date: cutoff.Unix(),
			},
			want: false,
		},
		{
			name: "recent message",
			message: &telego.Message{
				Date: now.Add(-20 * time.Second).Unix(),
			},
			want: false,
		},
		{
			name: "zero date",
			message: &telego.Message{
				Date: 0,
			},
			want: false,
		},
		{
			name:    "nil message",
			message: nil,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStaleTelegramMessage(tt.message, cutoff); got != tt.want {
				t.Fatalf("isStaleTelegramMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}
