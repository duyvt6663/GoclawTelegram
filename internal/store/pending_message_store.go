package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// PendingMessage represents a buffered group chat message (or LLM-generated summary)
// stored in channel_pending_messages table.
type PendingMessage struct {
	ID            uuid.UUID `json:"id"`
	ChannelName   string    `json:"channel_name"`
	HistoryKey    string    `json:"history_key"`
	Sender        string    `json:"sender"`
	SenderID      string    `json:"sender_id"`
	Body          string    `json:"body"`
	PlatformMsgID string    `json:"platform_msg_id"`
	IsSummary     bool      `json:"is_summary"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// PendingMessageGroup is a summary row for the grouped overview page.
type PendingMessageGroup struct {
	ChannelName  string    `json:"channel_name"`
	HistoryKey   string    `json:"history_key"`
	GroupTitle   string    `json:"group_title,omitempty"`
	MessageCount int       `json:"message_count"`
	HasSummary   bool      `json:"has_summary"`
	LastActivity time.Time `json:"last_activity"`
}

// PendingMessageStore persists group chat messages for context when bot is mentioned.
type PendingMessageStore interface {
	// AppendBatch inserts multiple pending messages in a single query.
	AppendBatch(ctx context.Context, msgs []PendingMessage) error

	// ListByKey returns all pending messages for a channel+historyKey, ordered by created_at ASC.
	ListByKey(ctx context.Context, channelName, historyKey string) ([]PendingMessage, error)

	// DeleteByKey removes all pending messages for a channel+historyKey.
	DeleteByKey(ctx context.Context, channelName, historyKey string) error

	// DeleteBefore removes pending messages older than cutoff for a channel.
	DeleteBefore(ctx context.Context, channelName string, cutoff time.Time) (int64, error)

	// Compact atomically deletes old messages (by IDs) and inserts a summary row.
	Compact(ctx context.Context, deleteIDs []uuid.UUID, summary *PendingMessage) error

	// DeleteStale removes messages older than the given duration for inactive groups.
	DeleteStale(ctx context.Context, olderThan time.Duration) (int64, error)

	// ListGroups returns all distinct channel+historyKey groups with message counts.
	ListGroups(ctx context.Context) ([]PendingMessageGroup, error)

	// CountAll returns the total number of pending messages across all groups.
	CountAll(ctx context.Context) (int64, error)

	// CountByKey returns the number of pending messages for a specific channel+historyKey.
	CountByKey(ctx context.Context, channelName, historyKey string) (int, error)

	// ResolveGroupTitles looks up chat_title from session metadata for each group.
	// Returns a map of "channel_name:history_key" → title. Used only by the UI layer.
	ResolveGroupTitles(ctx context.Context, groups []PendingMessageGroup) (map[string]string, error)
}
