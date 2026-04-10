package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/sodaubai"
)

const soDauBaiPollAddedBy = "@tap_the_lop"

// CreatePoll posts a non-anonymous single-choice Telegram poll and returns the
// poll ID plus the message ID that contains it.
func (c *Channel) CreatePoll(ctx context.Context, chatID int64, threadID int, question string, options []string, openPeriodSeconds int) (string, int, error) {
	if len(options) < 2 {
		return "", 0, fmt.Errorf("telegram poll requires at least two options")
	}

	pollOptions := make([]telego.InputPollOption, 0, len(options))
	for _, option := range options {
		text := strings.TrimSpace(option)
		if text == "" {
			return "", 0, fmt.Errorf("telegram poll option cannot be empty")
		}
		pollOptions = append(pollOptions, telego.InputPollOption{Text: text})
	}

	isAnonymous := false
	params := &telego.SendPollParams{
		ChatID:                telego.ChatID{ID: chatID},
		Question:              strings.TrimSpace(question),
		Options:               pollOptions,
		IsAnonymous:           &isAnonymous,
		AllowsMultipleAnswers: false,
	}
	if openPeriodSeconds > 0 {
		params.OpenPeriod = openPeriodSeconds
	}
	if sendThreadID := resolveThreadIDForSend(threadID); sendThreadID > 0 {
		params.MessageThreadID = sendThreadID
	}

	msg, err := c.bot.SendPoll(ctx, params)
	if err != nil {
		return "", 0, fmt.Errorf("telegram API: %w", err)
	}
	if msg == nil || msg.Poll == nil || msg.Poll.ID == "" {
		return "", 0, fmt.Errorf("telegram API returned no poll id")
	}
	return msg.Poll.ID, msg.MessageID, nil
}

func (c *Channel) CreateSoDauBaiPoll(ctx context.Context, chatID int64, threadID int, question, yesOption, noOption string, openPeriodSeconds int) (string, int, error) {
	return c.CreatePoll(ctx, chatID, threadID, question, []string{yesOption, noOption}, openPeriodSeconds)
}

// StopPoll closes a Telegram poll message by message ID.
func (c *Channel) StopPoll(ctx context.Context, chatID int64, messageID int) error {
	if messageID <= 0 {
		return nil
	}
	_, err := c.bot.StopPoll(ctx, &telego.StopPollParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: messageID,
	})
	return err
}

func (c *Channel) handlePollAnswer(ctx context.Context, answer *telego.PollAnswer) {
	if c == nil || answer == nil {
		return
	}

	voterID := resolvePollVoterID(answer)
	if voterID == "" {
		return
	}

	// Broadcast poll answer to all bus subscribers (beta features, extensions, etc.)
	c.Bus().Broadcast(bus.Event{
		Name:     bus.TopicTelegramPollAnswer,
		TenantID: c.TenantID(),
		Payload: map[string]any{
			"poll_id":    answer.PollID,
			"voter_id":   voterID,
			"option_ids": answer.OptionIDs,
			"channel":    c.Name(),
		},
	})

	if c.soDauBaiPolls == nil {
		return
	}

	result, err := c.soDauBaiPolls.RecordVote(answer.PollID, voterID, answer.OptionIDs)
	if err != nil {
		slog.Warn("telegram poll vote record failed", "poll_id", answer.PollID, "error", err)
		return
	}
	if result.Poll == nil {
		return
	}

	slog.Debug("telegram poll vote recorded",
		"poll_id", answer.PollID,
		"target", result.Poll.Target,
		"yes_votes", result.YesVotes,
		"threshold", result.Poll.Threshold,
	)

	if !result.ThresholdReached {
		return
	}
	c.resolveSoDauBaiPollThreshold(ctx, result)
}

func (c *Channel) handlePollUpdate(_ context.Context, poll *telego.Poll) {
	if c == nil || poll == nil || !poll.IsClosed {
		return
	}

	c.Bus().Broadcast(bus.Event{
		Name:     bus.TopicTelegramPollClosed,
		TenantID: c.TenantID(),
		Payload: map[string]any{
			"poll_id": poll.ID,
			"channel": c.Name(),
		},
	})

	if c.soDauBaiPolls == nil {
		return
	}
	if _, err := c.soDauBaiPolls.MarkClosed(poll.ID); err != nil {
		slog.Warn("telegram poll close mark failed", "poll_id", poll.ID, "error", err)
	}
}

func (c *Channel) resolveSoDauBaiPollThreshold(ctx context.Context, result sodaubai.PollVoteResult) {
	entry := result.Poll
	if entry == nil {
		return
	}

	chatID, err := parseChatID(entry.ChatID)
	if err != nil {
		slog.Warn("telegram poll threshold: invalid chat id", "chat_id", entry.ChatID, "error", err)
		return
	}

	var text string
	switch sodaubai.NormalizePollAction(entry.Action) {
	case sodaubai.PollActionRemove:
		alreadyAlways := false
		removed := false
		var removeErr error
		if c.soDauBai != nil {
			alreadyAlways = c.soDauBai.HasAlways(entry.Scope, entry.Target)
			if !alreadyAlways {
				_, removed, removeErr = c.soDauBai.RemoveToday(entry.Target)
			}
		}
		text = buildSoDauBaiPardonAnnouncement(entry, result.YesVotes, removed, alreadyAlways, removeErr)
	default:
		alreadyAlways := false
		added := false
		var addErr error
		if c.soDauBai != nil {
			alreadyAlways = c.soDauBai.HasAlways(entry.Scope, entry.Target)
			if !alreadyAlways {
				_, added, addErr = c.soDauBai.AddToday(entry.Target, soDauBaiPollAddedBy, buildSoDauBaiPollNote(entry, result.YesVotes))
			}
		}
		text = buildSoDauBaiPollAnnouncement(entry, result.YesVotes, added, alreadyAlways, addErr)
	}

	if err := c.stopSoDauBaiPoll(ctx, chatID, entry.MessageID); err != nil {
		slog.Warn("telegram poll stop failed", "poll_id", entry.PollID, "message_id", entry.MessageID, "error", err)
	}
	if _, err := c.soDauBaiPolls.MarkClosed(entry.PollID); err != nil {
		slog.Warn("telegram poll close mark failed after threshold", "poll_id", entry.PollID, "error", err)
	}

	if err := c.sendSoDauBaiPollAnnouncement(ctx, chatID, entry.ThreadID, text); err != nil {
		slog.Warn("telegram poll announcement failed", "poll_id", entry.PollID, "error", err)
	}
}

func (c *Channel) stopSoDauBaiPoll(ctx context.Context, chatID int64, messageID int) error {
	if messageID <= 0 {
		return nil
	}
	_, err := c.bot.StopPoll(ctx, &telego.StopPollParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: messageID,
	})
	return err
}

func (c *Channel) sendSoDauBaiPollAnnouncement(ctx context.Context, chatID int64, threadID int, text string) error {
	params := tu.Message(tu.ID(chatID), text)
	if sendThreadID := resolveThreadIDForSend(threadID); sendThreadID > 0 {
		params.MessageThreadID = sendThreadID
	}
	_, err := c.bot.SendMessage(ctx, params)
	return err
}

func resolvePollVoterID(answer *telego.PollAnswer) string {
	if answer == nil {
		return ""
	}
	if user := answer.User; user != nil {
		if user.Username != "" {
			return fmt.Sprintf("%d|%s", user.ID, user.Username)
		}
		return fmt.Sprintf("%d", user.ID)
	}
	if voterChat := answer.VoterChat; voterChat != nil {
		id := fmt.Sprintf("sender_chat:%d", voterChat.ID)
		if voterChat.Username != "" {
			return id + "|" + voterChat.Username
		}
		return id
	}
	return ""
}

func buildSoDauBaiPollNote(entry *sodaubai.PollEntry, yesVotes int) string {
	if entry == nil {
		return ""
	}
	note := fmt.Sprintf("telegram poll %d/%d votes", yesVotes, entry.Threshold)
	if reason := strings.TrimSpace(entry.Reason); reason != "" {
		note += ": " + reason
	}
	return note
}

func buildSoDauBaiPollAnnouncement(entry *sodaubai.PollEntry, yesVotes int, added, alreadyAlways bool, addErr error) string {
	target := pollAnnouncementTarget(entry)
	switch {
	case addErr != nil:
		return fmt.Sprintf("⚠️ %s đã đủ %d phiếu rồi mà tôi ghi sổ bị trượt tay. Sổ hôm nay chưa cập nhật được.", target, yesVotes)
	case alreadyAlways:
		return fmt.Sprintf("📒 %s đủ %d phiếu, nhưng tên này có hộ khẩu thường trú trong sổ đầu bài của chat này rồi.", target, yesVotes)
	case added:
		if reason := strings.TrimSpace(entry.Reason); reason != "" {
			return fmt.Sprintf("📒 %s chốt %d phiếu và đã bị nhập khẩu vào sổ đầu bài hôm nay.\nTội trạng: %s", target, yesVotes, reason)
		}
		return fmt.Sprintf("📒 %s chốt %d phiếu và đã bị nhập khẩu vào sổ đầu bài hôm nay.", target, yesVotes)
	default:
		return fmt.Sprintf("📒 %s đủ %d phiếu và tên đã nằm chình ình trong sổ đầu bài hôm nay rồi.", target, yesVotes)
	}
}

func buildSoDauBaiPardonAnnouncement(entry *sodaubai.PollEntry, yesVotes int, removed, alreadyAlways bool, removeErr error) string {
	target := pollAnnouncementTarget(entry)
	switch {
	case removeErr != nil:
		return fmt.Sprintf("⚠️ %s đủ %d phiếu tha rồi mà tôi gạch sổ bị trượt tay. Sổ hôm nay chưa cập nhật được.", target, yesVotes)
	case alreadyAlways:
		return fmt.Sprintf("📒 %s đủ %d phiếu tha, nhưng tên này thuộc deny_from nên vẫn chưa được xóa khỏi sổ đầu bài của chat này.", target, yesVotes)
	case removed:
		if reason := strings.TrimSpace(entry.Reason); reason != "" {
			return fmt.Sprintf("🧽 %s chốt %d phiếu và đã được gạch tên khỏi sổ đầu bài hôm nay.\nTình tiết giảm nhẹ: %s", target, yesVotes, reason)
		}
		return fmt.Sprintf("🧽 %s chốt %d phiếu và đã được gạch tên khỏi sổ đầu bài hôm nay.", target, yesVotes)
	default:
		return fmt.Sprintf("🧽 %s đủ %d phiếu tha, nhưng tên đã bay khỏi sổ đầu bài hôm nay từ trước rồi.", target, yesVotes)
	}
}

func pollAnnouncementTarget(entry *sodaubai.PollEntry) string {
	if entry == nil {
		return "Nguoi do"
	}
	if strings.TrimSpace(entry.TargetDisplay) != "" {
		return entry.TargetDisplay
	}
	if strings.TrimSpace(entry.Target) != "" {
		return entry.Target
	}
	return "Nguoi do"
}
