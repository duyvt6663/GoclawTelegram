package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nextlevelbuilder/goclaw/internal/sodaubai"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	createSoDauBaiPollToolName       = "create_so_dau_bai_poll"
	createSoDauBaiPardonPollToolName = "create_so_dau_bai_pardon_poll"
	soDauBaiPollOpenPeriod           = 600
)

type SoDauBaiPollCreator interface {
	CreateSoDauBaiPoll(ctx context.Context, chatID int64, threadID int, question, yesOption, noOption string, openPeriodSeconds int) (pollID string, messageID int, err error)
}

type SoDauBaiPollCreatorResolver func(channel string) SoDauBaiPollCreator

type CreateSoDauBaiPollTool struct {
	soDauBai *sodaubai.Service
	polls    *sodaubai.PollService
	resolve  SoDauBaiPollCreatorResolver
	action   string
	contacts store.ContactStore
}

func NewCreateSoDauBaiPollTool(soDauBai *sodaubai.Service, polls *sodaubai.PollService, resolve SoDauBaiPollCreatorResolver, contacts store.ContactStore) *CreateSoDauBaiPollTool {
	return newSoDauBaiPollTool(soDauBai, polls, resolve, contacts, sodaubai.PollActionAdd)
}

func NewCreateSoDauBaiPardonPollTool(soDauBai *sodaubai.Service, polls *sodaubai.PollService, resolve SoDauBaiPollCreatorResolver, contacts store.ContactStore) *CreateSoDauBaiPollTool {
	return newSoDauBaiPollTool(soDauBai, polls, resolve, contacts, sodaubai.PollActionRemove)
}

func newSoDauBaiPollTool(soDauBai *sodaubai.Service, polls *sodaubai.PollService, resolve SoDauBaiPollCreatorResolver, contacts store.ContactStore, action string) *CreateSoDauBaiPollTool {
	return &CreateSoDauBaiPollTool{
		soDauBai: soDauBai,
		polls:    polls,
		resolve:  resolve,
		action:   sodaubai.NormalizePollAction(action),
		contacts: contacts,
	}
}

func (t *CreateSoDauBaiPollTool) Name() string {
	if t.pollAction() == sodaubai.PollActionRemove {
		return createSoDauBaiPardonPollToolName
	}
	return createSoDauBaiPollToolName
}

func (t *CreateSoDauBaiPollTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *CreateSoDauBaiPollTool) Description() string {
	if t.pollAction() == sodaubai.PollActionRemove {
		return "Create a non-anonymous Telegram poll in the current group or topic to vote whether someone should be removed from today's sổ đầu bài. " +
			"Use this when the user asks to thả, ân xá, gạch tên, pardon, or xóa someone khỏi sổ. " +
			"When the yes vote count reaches 5, the bot automatically removes that person from today's sổ đầu bài and announces it."
	}
	return "Create a non-anonymous Telegram poll in the current group or topic to vote whether someone should be added to today's sổ đầu bài. " +
		"Use this when the user asks to mở poll xử, vote cho ai lên sổ, or cho ai vào sổ. " +
		"When the yes vote count reaches 5, the bot automatically adds that person to today's sổ đầu bài and announces it."
}

func (t *CreateSoDauBaiPollTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": map[string]any{
				"type":        "string",
				"description": "The Telegram user to put on trial. Use @username when possible, or a raw sender form like 123456|username. Known display names or nicknames from this Telegram chat also work.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": soDauBaiPollReasonDescription(t.pollAction()),
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
		return ErrorResult(fmt.Sprintf("%s only works in Telegram group chats or topics", t.Name()))
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
	target = resolveTelegramTarget(ctx, t.contacts, target)

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
	action := t.pollAction()

	if entry, err := t.soDauBai.FindTodayForScope(scope, target); err != nil {
		return ErrorResult(fmt.Sprintf("failed to check today's sổ đầu bài: %v", err))
	} else if action == sodaubai.PollActionAdd && entry != nil {
		return NewResult(fmt.Sprintf("%s is already in today's sổ đầu bài.\n\n%s", prettyTelegramTarget(target), formatSoDauBaiToday(sodaubai.State{
			Date:    entry.AddedDay,
			Entries: []sodaubai.Entry{*entry},
		})))
	} else if action == sodaubai.PollActionRemove {
		if t.soDauBai.HasAlways(scope, target) {
			state, stateErr := t.soDauBai.TodayForScope(scope)
			if stateErr != nil {
				return ErrorResult(fmt.Sprintf("failed to read today's so_dau_bai: %v", stateErr))
			}
			return ErrorResult(fmt.Sprintf("%s is in deny_from, so this chat cannot vote them out of today's sổ đầu bài.\n\n%s", prettyTelegramTarget(target), formatSoDauBaiToday(state)))
		}
		if entry == nil {
			state, stateErr := t.soDauBai.TodayForScope(scope)
			if stateErr != nil {
				return ErrorResult(fmt.Sprintf("failed to read today's so_dau_bai: %v", stateErr))
			}
			return NewResult(fmt.Sprintf("%s is not currently in today's sổ đầu bài.\n\n%s", prettyTelegramTarget(target), formatSoDauBaiToday(state)))
		}
	}

	if active, err := t.polls.FindActiveByChatTargetAction(channel, chatIDStr, target, action); err != nil {
		return ErrorResult(fmt.Sprintf("failed to check active sổ đầu bài polls: %v", err))
	} else if active != nil {
		return NewResult(formatExistingSoDauBaiPollReuse(active, localKey))
	}

	targetDisplay := prettyTelegramTarget(target)
	question := buildSoDauBaiPollQuestion(action, targetDisplay, reason)
	yesOption, noOption := soDauBaiPollOptions(action)
	pollID, messageID, err := creator.CreateSoDauBaiPoll(ctx, chatID, threadID, question, yesOption, noOption, soDauBaiPollOpenPeriod)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create Telegram poll: %v", err))
	}

	entry, err := t.polls.CreatePoll(sodaubai.PollCreate{
		PollID:        pollID,
		Action:        action,
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
		"action":         entry.Action,
		"message_id":     entry.MessageID,
		"target":         entry.Target,
		"target_display": pollTargetDisplay(&entry),
		"threshold":      entry.Threshold,
		"question":       entry.Question,
	}
	data, _ := json.Marshal(payload)
	return NewResult(string(data))
}

func (t *CreateSoDauBaiPollTool) pollAction() string {
	return sodaubai.NormalizePollAction(t.action)
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

func buildSoDauBaiPollQuestion(action, targetDisplay, reason string) string {
	action = sodaubai.NormalizePollAction(action)
	reason = strings.TrimSpace(strings.TrimSuffix(reason, "."))

	switch action {
	case sodaubai.PollActionRemove:
		question := fmt.Sprintf("Biểu quyết nhanh: có gạch tên %s khỏi sổ đầu bài hôm nay không?", targetDisplay)
		if reason != "" {
			question = fmt.Sprintf("Biểu quyết nhanh: có gạch tên %s khỏi sổ đầu bài hôm nay vì %s không?", targetDisplay, reason)
		}
		return truncateRunes(question, 300)
	default:
		question := fmt.Sprintf("Biểu quyết nhanh: có tống %s vào sổ đầu bài hôm nay không?", targetDisplay)
		if reason != "" {
			question = fmt.Sprintf("Biểu quyết nhanh: có tống %s vào sổ đầu bài hôm nay vì %s không?", targetDisplay, reason)
		}
		return truncateRunes(question, 300)
	}
}

func soDauBaiPollOptions(action string) (string, string) {
	switch sodaubai.NormalizePollAction(action) {
	case sodaubai.PollActionRemove:
		return "Tha ra ngoài", "Ngồi lại sổ"
	default:
		return "Cho nhập sổ", "Tha kiếp này"
	}
}

func soDauBaiPollReasonDescription(action string) string {
	switch sodaubai.NormalizePollAction(action) {
	case sodaubai.PollActionRemove:
		return "Optional short excuse, defense, or joke reason to include in the pardon poll question."
	default:
		return "Optional short accusation or joke reason to include in the poll question."
	}
}

func soDauBaiPollKindLabel(action string) string {
	switch sodaubai.NormalizePollAction(action) {
	case sodaubai.PollActionRemove:
		return "sổ đầu bài pardon poll"
	default:
		return "sổ đầu bài poll"
	}
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

func formatExistingSoDauBaiPollReuse(entry *sodaubai.PollEntry, currentLocalKey string) string {
	if entry == nil {
		return "An active sổ đầu bài poll is already open in this chat."
	}

	location := "this chat"
	switch {
	case strings.TrimSpace(entry.LocalKey) == strings.TrimSpace(currentLocalKey):
		if entry.ThreadID > 0 {
			location = "this topic"
		}
	case entry.ThreadID > 0:
		location = fmt.Sprintf("topic %d in this chat", entry.ThreadID)
	default:
		location = "another part of this chat"
	}

	msg := fmt.Sprintf("An active %s for %s is already open in %s (message_id=%d, yes_votes=%d/%d).",
		soDauBaiPollKindLabel(entry.Action), pollTargetDisplay(entry), location, entry.MessageID, len(entry.YesVoters), entry.Threshold)
	if strings.TrimSpace(entry.LocalKey) != strings.TrimSpace(currentLocalKey) {
		msg += " Reusing that poll instead of opening another."
	}
	return msg
}

func startsWithDigit(value string) bool {
	if value == "" {
		return false
	}
	c := value[0]
	return c >= '0' && c <= '9'
}
