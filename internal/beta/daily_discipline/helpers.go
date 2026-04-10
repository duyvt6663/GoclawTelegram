package dailydiscipline

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	questionKindWake       = "wake"
	questionKindDiscipline = "discipline"
	questionKindActivity   = "activity"

	postKindWakePoll       = "wake_poll"
	postKindDisciplinePoll = "discipline_poll"
	postKindActivityPoll   = "activity_poll"
	postKindSurveyHeader   = "survey_header"
	postKindSummary        = "summary"

	statusYes = "yes"
	statusNo  = "no"

	activityNone  = "none"
	activityGym   = "gym"
	activityRun   = "run"
	activitySport = "sport"
)

var configKeyRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func tenantKey(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return strings.TrimSpace(id.String())
}

func tenantKeyFromCtx(ctx context.Context) string {
	return tenantKey(store.TenantIDFromContext(ctx))
}

func normalizeConfigKey(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}

	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-', r == '_', r == ' ', r == '/':
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func normalizeYesNo(value string) (string, bool) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "yes", "y", "true", "1", "done", "early":
		return statusYes, true
	case "no", "n", "false", "0", "late":
		return statusNo, true
	default:
		return "", false
	}
}

func normalizeActivity(value string) (string, bool) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "no", "none", "rest":
		return activityNone, true
	case activityGym, "lift", "lifting", "workout":
		return activityGym, true
	case activityRun, "running", "jog", "jogging":
		return activityRun, true
	case activitySport, "sports":
		return activitySport, true
	default:
		return "", false
	}
}

func parseTimeOfDay(value string) (int, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("time must use HH:MM")
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid hour")
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid minute")
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, fmt.Errorf("time must be in 00:00-23:59")
	}
	return hour*60 + minute, nil
}

func minutesToClock(total int) string {
	if total < 0 {
		total = 0
	}
	hour := total / 60
	minute := total % 60
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

func withinWindow(minuteOfDay, startMinute, endMinute int) bool {
	if startMinute <= endMinute {
		return minuteOfDay >= startMinute && minuteOfDay <= endMinute
	}
	return minuteOfDay >= startMinute || minuteOfDay <= endMinute
}

func parseCompositeChatTarget(target string) (string, int) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", 0
	}
	if idx := strings.Index(target, ":topic:"); idx > 0 {
		threadID, _ := strconv.Atoi(target[idx+7:])
		return target[:idx], threadID
	}
	if idx := strings.Index(target, ":thread:"); idx > 0 {
		threadID, _ := strconv.Atoi(target[idx+8:])
		return target[:idx], threadID
	}
	return target, 0
}

func composeLocalKey(chatID string, threadID int) string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return ""
	}
	if threadID <= 0 {
		return chatID
	}
	return fmt.Sprintf("%s:topic:%d", chatID, threadID)
}

func chatTargetFromToolContext(ctx context.Context, explicitChatID string, explicitThreadID int) (string, int) {
	if explicitChatID != "" {
		chatID, threadID := parseCompositeChatTarget(explicitChatID)
		if explicitThreadID > 0 {
			threadID = explicitThreadID
		}
		return chatID, threadID
	}

	if localKey := tools.ToolLocalKeyFromCtx(ctx); localKey != "" {
		chatID, threadID := parseCompositeChatTarget(localKey)
		if explicitThreadID > 0 {
			threadID = explicitThreadID
		}
		return chatID, threadID
	}

	chatID := strings.TrimSpace(tools.ToolChatIDFromCtx(ctx))
	return chatID, explicitThreadID
}

type userIdentity struct {
	ID    string
	Label string
}

func parseUserIdentity(raw string) userIdentity {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return userIdentity{}
	}
	id := raw
	label := raw
	if before, after, ok := strings.Cut(raw, "|"); ok {
		id = strings.TrimSpace(before)
		after = strings.TrimSpace(after)
		if after != "" {
			if strings.HasPrefix(after, "@") {
				label = after
			} else {
				label = "@" + after
			}
		}
	}
	if id == "" {
		id = raw
	}
	if label == "" {
		label = id
	}
	return userIdentity{ID: id, Label: label}
}

func statusLabel(value string) string {
	switch value {
	case statusYes:
		return "yes"
	case statusNo:
		return "no"
	default:
		return "unknown"
	}
}

func activityLabel(value string) string {
	switch value {
	case activityGym:
		return "gym"
	case activityRun:
		return "run"
	case activitySport:
		return "sport"
	case activityNone:
		return "none"
	default:
		return "unknown"
	}
}

func loadLocation(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func parseISODate(value string) (time.Time, error) {
	return time.Parse("2006-01-02", strings.TrimSpace(value))
}
