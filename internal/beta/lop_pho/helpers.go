package loppho

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	featureName = "lop_pho"

	voteCommandName = "/vote_lp"

	pollOptionBau           = "bầu"
	pollOptionHaHanhKiem    = "hạ hạnh kiểm"
	voteChoiceBau           = "bau"
	voteChoiceHaHanhKiem    = "ha_hanh_kiem"
	pollStatusActive        = "active"
	pollStatusResolved      = "resolved"
	pollStatusClosed        = "closed"
	pollOpenPeriodSeconds   = 600
	bauVoteThreshold        = 5
	haHanhKiemVoteThreshold = 3
	muteDuration            = 2 * 60 * 60

	defaultCongratsStickerRelPath = ".local-stickers/telegram/captured/2026-04-10/sticker_fa671eb404c5.tgs"

	voteOpenSourceCommand = "telegram_command:/vote_lp"
	voteOpenSourceTool    = "tool:vote_lop_pho_open"
	voteOpenSourceRPC     = "rpc:beta.lop_pho.open_vote"
	voteOpenSourceHTTP    = "http:POST /v1/beta/lop-pho/polls"
	voteOpenSourceUnknown = "unknown"
)

type telegramIdentity struct {
	UserID   string
	SenderID string
	Label    string
}

type PollFilter struct {
	Channel    string
	ChatID     string
	ThreadID   int
	HasThread  bool
	ActiveOnly bool
	Limit      int
}

type StatusSnapshot struct {
	Roles []LopPhoRole `json:"roles"`
	Polls []LopPhoPoll `json:"polls"`
}

func tenantKey(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return strings.TrimSpace(id.String())
}

func tenantKeyFromCtx(ctx context.Context) string {
	return tenantKey(store.TenantIDFromContext(ctx))
}

func normalizeThreadID(threadID int) int {
	if threadID <= 1 {
		return 0
	}
	return threadID
}

func parseCompositeChatTarget(target string) (string, int) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", 0
	}
	if idx := strings.Index(target, ":topic:"); idx > 0 {
		threadID, _ := strconv.Atoi(target[idx+7:])
		return target[:idx], normalizeThreadID(threadID)
	}
	if idx := strings.Index(target, ":thread:"); idx > 0 {
		threadID, _ := strconv.Atoi(target[idx+8:])
		return target[:idx], normalizeThreadID(threadID)
	}
	return target, 0
}

func composeLocalKey(chatID string, threadID int) string {
	chatID = strings.TrimSpace(chatID)
	threadID = normalizeThreadID(threadID)
	if chatID == "" {
		return ""
	}
	if threadID == 0 {
		return chatID
	}
	return fmt.Sprintf("%s:topic:%d", chatID, threadID)
}

func chatTargetFromToolContext(ctx context.Context) (string, int) {
	if localKey := strings.TrimSpace(tools.ToolLocalKeyFromCtx(ctx)); localKey != "" {
		return parseCompositeChatTarget(localKey)
	}
	return strings.TrimSpace(tools.ToolChatIDFromCtx(ctx)), 0
}

func parseActorIdentity(raw string) telegramIdentity {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return telegramIdentity{}
	}
	if ident, ok := parseCanonicalTelegramIdentity(raw); ok {
		return ident
	}
	return telegramIdentity{
		UserID:   raw,
		SenderID: raw,
		Label:    raw,
	}
}

func parseCanonicalTelegramIdentity(raw string) (telegramIdentity, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return telegramIdentity{}, false
	}
	if before, after, ok := strings.Cut(raw, "|"); ok {
		id := strings.TrimSpace(before)
		after = strings.TrimSpace(after)
		label := id
		if after != "" {
			label = "@" + strings.TrimPrefix(after, "@")
		}
		if id == "" {
			id = userIDFromSenderID(raw)
		}
		return telegramIdentity{
			UserID:   id,
			SenderID: raw,
			Label:    firstNonEmpty(label, raw),
		}, true
	}
	if strings.HasPrefix(raw, "@") {
		return telegramIdentity{
			SenderID: raw,
			Label:    raw,
		}, true
	}
	if isDigitsOnly(raw) {
		return telegramIdentity{
			UserID:   raw,
			SenderID: raw,
			Label:    raw,
		}, true
	}
	return telegramIdentity{}, false
}

func userIDFromSenderID(senderID string) string {
	senderID = strings.TrimSpace(senderID)
	if senderID == "" {
		return ""
	}
	if before, _, ok := strings.Cut(senderID, "|"); ok {
		senderID = strings.TrimSpace(before)
	}
	if !isDigitsOnly(senderID) {
		return ""
	}
	return senderID
}

func dedupeTargetKey(ident telegramIdentity) string {
	if userID := strings.TrimSpace(firstNonEmpty(ident.UserID, userIDFromSenderID(ident.SenderID))); userID != "" {
		return "user:" + userID
	}

	senderID := strings.ToLower(strings.TrimSpace(ident.SenderID))
	if before, after, ok := strings.Cut(senderID, "|"); ok {
		before = strings.TrimSpace(before)
		after = strings.TrimPrefix(strings.TrimSpace(after), "@")
		if before != "" && isDigitsOnly(before) {
			return "user:" + before
		}
		if after != "" {
			return "sender:@" + after
		}
		senderID = before
	}
	if senderID != "" {
		return "sender:" + strings.TrimPrefix(senderID, "@")
	}

	label := strings.ToLower(strings.TrimSpace(ident.Label))
	if label != "" {
		return "label:" + strings.TrimPrefix(label, "@")
	}
	return ""
}

func stableVoteTargetKey(rawTarget string, ident telegramIdentity) string {
	rawTarget = strings.TrimSpace(rawTarget)
	if parsed, ok := parseCanonicalTelegramIdentity(rawTarget); ok {
		if key := dedupeTargetKey(parsed); key != "" {
			return key
		}
	}

	if normalized := normalizeTelegramTargetLookup(rawTarget); normalized != "" {
		return "input:" + normalized
	}

	return dedupeTargetKey(ident)
}

func isDigitsOnly(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func extractOptionIDs(raw any) []int {
	switch v := raw.(type) {
	case []int:
		return append([]int(nil), v...)
	case []any:
		out := make([]int, 0, len(v))
		for _, item := range v {
			switch n := item.(type) {
			case int:
				out = append(out, n)
			case int32:
				out = append(out, int(n))
			case int64:
				out = append(out, int(n))
			case float64:
				out = append(out, int(n))
			}
		}
		return out
	default:
		return nil
	}
}

func pollChoiceFromOptionIDs(optionIDs []int) string {
	for _, optionID := range optionIDs {
		switch optionID {
		case 0:
			return voteChoiceBau
		case 1:
			return voteChoiceHaHanhKiem
		}
	}
	return ""
}

func buildVoteQuestion(targetLabel string) string {
	targetLabel = strings.TrimSpace(targetLabel)
	if targetLabel == "" {
		targetLabel = "bạn này"
	}
	return truncateRunes(fmt.Sprintf("Vote lớp phó cho %s?", targetLabel), 300)
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizeTelegramTargetLookup(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "@"))
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"đ", "d",
		"Đ", "d",
	)
	value = strings.ToLower(replacer.Replace(value))
	value = strings.Join(strings.Fields(value), " ")
	return value
}

func scoreTelegramTargetCandidate(contact store.ChannelContact, channelName, rawTarget string) int {
	if !strings.EqualFold(contact.ChannelType, "telegram") {
		return -1
	}
	if contact.ContactType != "" && !strings.EqualFold(contact.ContactType, "user") {
		return -1
	}

	target := normalizeTelegramTargetLookup(rawTarget)
	if target == "" {
		return -1
	}

	score := 0
	if contact.ChannelInstance != nil && channelName != "" && strings.EqualFold(strings.TrimSpace(*contact.ChannelInstance), channelName) {
		score += 20
	}
	if username := strings.TrimSpace(stringPtrValue(contact.Username)); username != "" {
		name := normalizeTelegramTargetLookup(username)
		switch {
		case name == target:
			score += 100
		case strings.HasPrefix(name, target) || strings.HasPrefix(target, name):
			score += 70
		}
	}
	if displayName := strings.TrimSpace(stringPtrValue(contact.DisplayName)); displayName != "" {
		name := normalizeTelegramTargetLookup(displayName)
		switch {
		case name == target:
			score += 95
		case strings.HasPrefix(name, target) || strings.HasPrefix(target, name):
			score += 75
		}
	}
	if senderID := strings.TrimSpace(contact.SenderID); senderID != "" {
		name := normalizeTelegramTargetLookup(senderID)
		switch {
		case name == target:
			score += 90
		case strings.HasPrefix(name, target) || strings.HasPrefix(target, name):
			score += 60
		}
	}
	return score
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func contactToTelegramIdentity(contact store.ChannelContact) telegramIdentity {
	senderID := strings.TrimSpace(contact.SenderID)
	userID := strings.TrimSpace(stringPtrValue(contact.UserID))
	if userID == "" {
		userID = userIDFromSenderID(senderID)
	}
	username := strings.TrimSpace(stringPtrValue(contact.Username))
	if senderID == "" {
		switch {
		case username != "":
			senderID = "@" + strings.TrimPrefix(username, "@")
		case userID != "":
			senderID = userID
		}
	}

	label := firstNonEmpty(
		func() string {
			if username == "" {
				return ""
			}
			return "@" + strings.TrimPrefix(username, "@")
		}(),
		stringPtrValue(contact.DisplayName),
		senderID,
		userID,
	)
	return telegramIdentity{
		UserID:   userID,
		SenderID: senderID,
		Label:    label,
	}
}
