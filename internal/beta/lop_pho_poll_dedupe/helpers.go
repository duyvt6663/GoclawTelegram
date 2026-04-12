package lopphopolldedupe

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	featureName = "lop_pho_poll_dedupe"

	claimStatusPending = "pending"
	claimStatusCreated = "created"
	claimStatusFailed  = "failed"

	defaultListLimit = 20
	maxListLimit     = 100

	dedupeWindow = 30 * time.Second
	claimLease   = 20 * time.Second
)

type ClaimRequest struct {
	TenantID       string
	Channel        string
	ChatID         string
	ThreadID       int
	LocalKey       string
	TargetKey      string
	TargetLabel    string
	StartedByID    string
	StartedByLabel string
	Source         string
}

type ClaimDecision struct {
	Claim     *DedupeClaim `json:"claim,omitempty"`
	Acquired  bool         `json:"acquired"`
	Duplicate bool         `json:"duplicate,omitempty"`
	Pending   bool         `json:"pending,omitempty"`
}

type DedupeClaim struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenant_id,omitempty"`
	DedupeKey       string    `json:"dedupe_key"`
	Channel         string    `json:"channel,omitempty"`
	ChatID          string    `json:"chat_id"`
	ThreadID        int       `json:"thread_id"`
	LocalKey        string    `json:"local_key,omitempty"`
	TargetKey       string    `json:"target_key"`
	TargetLabel     string    `json:"target_label,omitempty"`
	StartedByID     string    `json:"started_by_id,omitempty"`
	StartedByLabel  string    `json:"started_by_label,omitempty"`
	Source          string    `json:"source,omitempty"`
	OwnerToken      string    `json:"owner_token,omitempty"`
	Status          string    `json:"status"`
	PollID          string    `json:"poll_id,omitempty"`
	PollMessageID   int       `json:"poll_message_id,omitempty"`
	SuppressedCount int       `json:"suppressed_count"`
	LastError       string    `json:"last_error,omitempty"`
	WindowStartedAt time.Time `json:"window_started_at"`
	ClaimedAt       time.Time `json:"claimed_at"`
	LeaseExpiresAt  time.Time `json:"lease_expires_at"`
	FirstSeenAt     time.Time `json:"first_seen_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ClaimFilter struct {
	ChatID    string
	ThreadID  int
	HasThread bool
	Limit     int
}

type StatusSnapshot struct {
	Claims []DedupeClaim `json:"claims"`
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

func composeScopeKey(chatID string, threadID int) string {
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

func buildDedupeKey(chatID string, threadID int, targetKey string, now time.Time) (string, time.Time) {
	windowStart := now.UTC().Truncate(dedupeWindow)
	scopeKey := composeScopeKey(chatID, threadID)
	return fmt.Sprintf("%s|%s|%d", scopeKey, strings.TrimSpace(targetKey), windowStart.Unix()), windowStart
}

func chatTargetFromToolContext(ctx context.Context) (string, int) {
	if localKey := strings.TrimSpace(tools.ToolLocalKeyFromCtx(ctx)); localKey != "" {
		return parseCompositeChatTarget(localKey)
	}
	return strings.TrimSpace(tools.ToolChatIDFromCtx(ctx)), 0
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
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

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if value, ok := args[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func intArg(args map[string]any, key string) (int, bool) {
	if args == nil {
		return 0, false
	}
	switch value := args[key].(type) {
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}
