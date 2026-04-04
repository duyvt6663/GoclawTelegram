package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nextlevelbuilder/goclaw/internal/sodaubai"
)

const (
	createSoDauBaiPollToolName = "create_so_dau_bai_poll"
	soDauBaiPollOpenPeriod     = 600
)

type SoDauBaiPollCreator interface {
	CreateSoDauBaiPoll(ctx context.Context, chatID int64, threadID int, question, yesOption, noOption string, openPeriodSeconds int) (pollID string, messageID int, err error)
}

type SoDauBaiPollCreatorResolver func(channel string) SoDauBaiPollCreator

type CreateSoDauBaiPollTool struct {
	soDauBai *sodaubai.Service
	polls    *sodaubai.PollService
	resolve  SoDauBaiPollCreatorResolver
}

func NewCreateSoDauBaiPollTool(soDauBai *sodaubai.Service, polls *sodaubai.PollService, resolve SoDauBaiPollCreatorResolver) *CreateSoDauBaiPollTool {
	return &CreateSoDauBaiPollTool{
		soDauBai: soDauBai,
		polls:    polls,
		resolve:  resolve,
	}
}

func (t *CreateSoDauBaiPollTool) Name() string { return createSoDauBaiPollToolName }

func (t *CreateSoDauBaiPollTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *CreateSoDauBaiPollTool) Description() string {
	return "Create a non-anonymous Telegram poll in the current group or topic to vote whether someone should be added to today's sổ đầu bài. " +
		"When the yes vote count reaches 5, the bot automatically adds that person to today's sổ đầu bài and announces it."
}

func (t *CreateSoDauBaiPollTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": map[string]any{
				"type":        "string",
				"description": "The Telegram user to put on trial. Use @username when possible, or a raw sender form like 123456|username.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Optional short accusation or joke reason to include in the poll question.",
			},
		},
		"required": []string{"target"},
	}
}

func (t *CreateSoDauBaiPollTool) Execute(ctx context.Context, args map[string]any) *Result {
	if t.resolve == nil {
		return ErrorResult("no Telegram poll creator is available")
	}
	if t.soDauBai == nil || t.polls == nil {
		return ErrorResult("sổ đầu bài poll services are not configured")
	}
	if ToolPeerKindFromCtx(ctx) != "group" {
		return ErrorResult("create_so_dau_bai_poll only works in Telegram group chats or topics")
	}

	channel := strings.TrimSpace(ToolChannelFromCtx(ctx))
	if channel == "" {
		return ErrorResult("current Telegram channel is missing from tool context")
	}
	creator := t.resolve(channel)
	if creator == nil {
		return ErrorResult("current Telegram channel cannot create polls")
	}

	target := strings.TrimSpace(GetParamString(args, "target", ""))
	reason := strings.TrimSpace(GetParamString(args, "reason", ""))
	if target == "" {
		return ErrorResult("target is required")
	}

	chatIDStr := strings.TrimSpace(ToolChatIDFromCtx(ctx))
	if chatIDStr == "" {
		return ErrorResult("current chat is missing from tool context")
	}
	chatID, err := parseTelegramChatID(chatIDStr)
	if err != nil {
		return ErrorResult(fmt.Sprintf("invalid Telegram chat ID %q: %v", chatIDStr, err))
	}

	localKey := strings.TrimSpace(ToolLocalKeyFromCtx(ctx))
	if localKey == "" {
		localKey = chatIDStr
	}
	threadID := parseTelegramThreadID(localKey)
	scope := sodaubai.ScopeKey(channel, localKey, chatIDStr)

	if entry, err := t.soDauBai.FindTodayForScope(scope, target); err != nil {
		return ErrorResult(fmt.Sprintf("failed to check today's sổ đầu bài: %v", err))
	} else if entry != nil {
		return NewResult(fmt.Sprintf("%s is already in today's sổ đầu bài.\n\n%s", prettyTelegramTarget(target), formatSoDauBaiToday(sodaubai.State{
			Date:    entry.AddedDay,
			Entries: []sodaubai.Entry{*entry},
		})))
	}

	if active, err := t.polls.FindActiveByTarget(scope, target); err != nil {
		return ErrorResult(fmt.Sprintf("failed to check active sổ đầu bài polls: %v", err))
	} else if active != nil {
		return NewResult(fmt.Sprintf("An active sổ đầu bài poll for %s is already open in this chat (message_id=%d, yes_votes=%d/%d).",
			pollTargetDisplay(active), active.MessageID, len(active.YesVoters), active.Threshold))
	}

	targetDisplay := prettyTelegramTarget(target)
	question := buildSoDauBaiPollQuestion(targetDisplay, reason)
	yesOption, noOption := soDauBaiPollOptions()
	pollID, messageID, err := creator.CreateSoDauBaiPoll(ctx, chatID, threadID, question, yesOption, noOption, soDauBaiPollOpenPeriod)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create Telegram poll: %v", err))
	}

	entry, err := t.polls.CreatePoll(sodaubai.PollCreate{
		PollID:        pollID,
		Scope:         scope,
		Channel:       channel,
		ChatID:        chatIDStr,
		LocalKey:      localKey,
		ThreadID:      threadID,
		MessageID:     messageID,
		Target:        target,
		TargetDisplay: targetDisplay,
		Reason:        reason,
		Question:      question,
		Threshold:     sodaubai.DefaultPollThreshold,
	})
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to track sổ đầu bài poll: %v", err))
	}

	payload := map[string]any{
		"poll_id":        entry.PollID,
		"message_id":     entry.MessageID,
		"target":         entry.Target,
		"target_display": pollTargetDisplay(&entry),
		"threshold":      entry.Threshold,
		"question":       entry.Question,
	}
	data, _ := json.Marshal(payload)
	return NewResult(string(data))
}

func parseTelegramChatID(chatIDStr string) (int64, error) {
	var chatID int64
	_, err := fmt.Sscanf(chatIDStr, "%d", &chatID)
	return chatID, err
}

func parseTelegramThreadID(localKey string) int {
	var threadID int
	if idx := strings.Index(localKey, ":topic:"); idx > 0 {
		fmt.Sscanf(localKey[idx+7:], "%d", &threadID)
	} else if idx := strings.Index(localKey, ":thread:"); idx > 0 {
		fmt.Sscanf(localKey[idx+8:], "%d", &threadID)
	}
	return threadID
}

func prettyTelegramTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return target
	}
	if idx := strings.Index(target, "|"); idx >= 0 && idx+1 < len(target) {
		name := strings.TrimSpace(target[idx+1:])
		if name != "" {
			return "@" + strings.TrimPrefix(name, "@")
		}
		target = strings.TrimSpace(target[:idx])
	}
	if strings.HasPrefix(target, "@") || strings.HasPrefix(target, "sender_chat:") || startsWithDigit(target) {
		return target
	}
	if strings.ContainsAny(target, " \t\n") {
		return target
	}
	return "@" + strings.TrimPrefix(target, "@")
}

func buildSoDauBaiPollQuestion(targetDisplay, reason string) string {
	question := fmt.Sprintf("Biểu quyết nhanh: có tống %s vào sổ đầu bài hôm nay không?", targetDisplay)
	if reason != "" {
		reason = strings.TrimSpace(strings.TrimSuffix(reason, "."))
		question = fmt.Sprintf("Biểu quyết nhanh: có tống %s vào sổ đầu bài hôm nay vì %s không?", targetDisplay, reason)
	}
	return truncateRunes(question, 300)
}

func soDauBaiPollOptions() (string, string) {
	return "Cho nhập sổ", "Tha kiếp này"
}

func truncateRunes(input string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(input) <= maxRunes {
		return input
	}
	runes := []rune(input)
	return string(runes[:maxRunes])
}

func pollTargetDisplay(entry *sodaubai.PollEntry) string {
	if entry == nil {
		return ""
	}
	if strings.TrimSpace(entry.TargetDisplay) != "" {
		return entry.TargetDisplay
	}
	return prettyTelegramTarget(entry.Target)
}

func startsWithDigit(value string) bool {
	if value == "" {
		return false
	}
	c := value[0]
	return c >= '0' && c <= '9'
}
