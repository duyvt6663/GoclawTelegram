package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// EpisodicSummary represents a per-session summary captured by the v3 memory layer.
// It sits between raw session history and long-term document memory.
type EpisodicSummary struct {
	ID         uuid.UUID `json:"id"`
	TenantID   uuid.UUID `json:"tenant_id"`
	AgentID    uuid.UUID `json:"agent_id"`
	UserID     string    `json:"user_id"`
	SessionKey string    `json:"session_key"`

	Summary    string   `json:"summary"`
	L0Abstract string   `json:"l0_abstract"`
	KeyTopics  []string `json:"key_topics"`

	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`

	TurnCount  int `json:"turn_count"`
	TokenCount int `json:"token_count"`

	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// EpisodicSearchResult is a lightweight hit used by auto-inject and memory_search.
type EpisodicSearchResult struct {
	EpisodicID string    `json:"episodic_id"`
	L0Abstract string    `json:"l0_abstract"`
	Score      float64   `json:"score"`
	CreatedAt  time.Time `json:"created_at"`
	SessionKey string    `json:"session_key"`
}

// EpisodicSearchOptions configures hybrid search over episodic summaries.
type EpisodicSearchOptions struct {
	MaxResults   int
	MinScore     float64
	VectorWeight float64
	TextWeight   float64
}

// EpisodicStore manages v3 episodic memory entries.
// Implementations must scope all queries by the tenant stored in context.
type EpisodicStore interface {
	Create(ctx context.Context, ep *EpisodicSummary) error
	Get(ctx context.Context, id string) (*EpisodicSummary, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, agentID, userID string, limit, offset int) ([]EpisodicSummary, error)
	Search(ctx context.Context, query string, agentID, userID string, opts EpisodicSearchOptions) ([]EpisodicSearchResult, error)
	ExistsBySourceID(ctx context.Context, agentID, userID, sourceID string) (bool, error)

	SetEmbeddingProvider(provider EmbeddingProvider)
	Close() error
}
