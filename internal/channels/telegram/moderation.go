package telegram

import (
	"context"
	"fmt"
	"time"

	"github.com/mymmrac/telego"
)

// MuteMember temporarily restricts a Telegram user from sending messages.
// The bot must be an admin with moderation rights in the target supergroup.
func (c *Channel) MuteMember(ctx context.Context, chatID int64, userID int64, duration time.Duration) error {
	if c == nil || c.bot == nil {
		return fmt.Errorf("telegram bot is unavailable")
	}
	if duration < 30*time.Second {
		duration = 30 * time.Second
	}

	deny := false
	permissions := telego.ChatPermissions{
		CanSendMessages:       &deny,
		CanSendAudios:         &deny,
		CanSendDocuments:      &deny,
		CanSendPhotos:         &deny,
		CanSendVideos:         &deny,
		CanSendVideoNotes:     &deny,
		CanSendVoiceNotes:     &deny,
		CanSendPolls:          &deny,
		CanSendOtherMessages:  &deny,
		CanAddWebPagePreviews: &deny,
	}

	return c.bot.RestrictChatMember(ctx, &telego.RestrictChatMemberParams{
		ChatID:                        telego.ChatID{ID: chatID},
		UserID:                        userID,
		Permissions:                   permissions,
		UseIndependentChatPermissions: true,
		UntilDate:                     time.Now().Add(duration).Unix(),
	})
}
