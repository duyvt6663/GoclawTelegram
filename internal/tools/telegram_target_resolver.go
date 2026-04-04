package tools

import (
	"context"
	"strings"
	"unicode"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func resolveTelegramTarget(ctx context.Context, contacts store.ContactStore, target string) string {
	target = strings.TrimSpace(target)
	if target == "" || contacts == nil {
		return target
	}
	if looksCanonicalTelegramTarget(target) {
		return target
	}

	search := normalizeTelegramTargetLookup(target)
	if search == "" {
		return target
	}

	results, err := contacts.ListContacts(ctx, store.ContactListOpts{
		Search:      search,
		ChannelType: "telegram",
		ContactType: "user",
		Limit:       20,
	})
	if err != nil || len(results) == 0 {
		return target
	}

	channelName := strings.TrimSpace(ToolChannelFromCtx(ctx))
	bestIdx := -1
	bestScore := -1
	ambiguous := false
	for i := range results {
		score := scoreTelegramTargetCandidate(results[i], channelName, target)
		if score < 0 {
			continue
		}
		if score > bestScore {
			bestIdx = i
			bestScore = score
			ambiguous = false
			continue
		}
		if score == bestScore {
			ambiguous = true
		}
	}
	if bestIdx < 0 || bestScore < 70 || ambiguous {
		return target
	}

	if canonical := canonicalTelegramContactTarget(results[bestIdx]); canonical != "" {
		return canonical
	}
	return target
}

func looksCanonicalTelegramTarget(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if strings.Contains(target, "|") || strings.HasPrefix(target, "sender_chat:") {
		return true
	}
	if strings.HasPrefix(target, "@") {
		return true
	}
	for _, r := range target {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
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

	if username := contact.Username; username != nil && strings.TrimSpace(*username) != "" {
		name := normalizeTelegramTargetLookup(*username)
		switch {
		case name == target:
			score += 100
		case strings.HasPrefix(name, target) || strings.HasPrefix(target, name):
			score += 70
		}
	}
	if displayName := contact.DisplayName; displayName != nil && strings.TrimSpace(*displayName) != "" {
		name := normalizeTelegramTargetLookup(*displayName)
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

func canonicalTelegramContactTarget(contact store.ChannelContact) string {
	if contact.Username != nil && strings.TrimSpace(*contact.Username) != "" {
		return "@" + strings.TrimPrefix(strings.TrimSpace(*contact.Username), "@")
	}
	if senderID := strings.TrimSpace(contact.SenderID); senderID != "" {
		return senderID
	}
	if contact.UserID != nil && strings.TrimSpace(*contact.UserID) != "" {
		return strings.TrimSpace(*contact.UserID)
	}
	return ""
}
