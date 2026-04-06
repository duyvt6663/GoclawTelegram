package telegram

import (
	"time"

	"github.com/mymmrac/telego"
)

const telegramStartupBacklogWindow = time.Minute

func telegramUpdateCutoff(now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now()
	}
	return now.Add(-telegramStartupBacklogWindow)
}

func isStaleTelegramMessage(message *telego.Message, cutoff time.Time) bool {
	if message == nil || cutoff.IsZero() || message.Date <= 0 {
		return false
	}
	messageTime := time.Unix(int64(message.Date), 0)
	return messageTime.Before(cutoff)
}
