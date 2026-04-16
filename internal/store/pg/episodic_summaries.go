package pg

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGEpisodicStore implements store.EpisodicStore backed by PostgreSQL.
type PGEpisodicStore struct {
	db          *sql.DB
	embProvider store.EmbeddingProvider
}

// NewPGEpisodicStore creates a new PG-backed episodic store.
func NewPGEpisodicStore(db *sql.DB) *PGEpisodicStore {
	return &PGEpisodicStore{db: db}
}

func (s *PGEpisodicStore) SetEmbeddingProvider(provider store.EmbeddingProvider) {
	s.embProvider = provider
}

func (s *PGEpisodicStore) Close() error { return nil }

// Create inserts a new episodic summary with an optional embedding.
func (s *PGEpisodicStore) Create(ctx context.Context, ep *store.EpisodicSummary) error {
	if ep == nil {
		return fmt.Errorf("episodic create: nil summary")
	}

	if ep.ID == uuid.Nil {
		ep.ID = uuid.Must(uuid.NewV7())
	}
	if ep.TenantID == uuid.Nil {
		ep.TenantID = tenantIDForInsert(ctx)
	}
	if ep.AgentID == uuid.Nil {
		return fmt.Errorf("episodic create: missing agent_id")
	}
	if ep.SourceType == "" {
		ep.SourceType = "session"
	}
	if ep.CreatedAt.IsZero() {
		ep.CreatedAt = time.Now().UTC()
	}

	var embStr *string
	if s.embProvider != nil && ep.Summary != "" {
		vecs, err := s.embProvider.Embed(ctx, []string{ep.Summary})
		if err == nil && len(vecs) > 0 {
			v := vectorToString(vecs[0])
			embStr = &v
		} else if err != nil {
			slog.Warn("episodic embedding failed", "error", err)
		}
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO episodic_summaries
			(id, tenant_id, agent_id, user_id, session_key, summary, l0_abstract,
			 key_topics, embedding, source_type, source_id, turn_count, token_count,
			 created_at, expires_at)
		VALUES
			($1, $2, $3, $4, $5, $6, $7,
			 $8, $9, $10, $11, $12, $13,
			 $14, $15)
		ON CONFLICT (agent_id, user_id, source_id)
		WHERE source_id IS NOT NULL DO NOTHING`,
		ep.ID,
		ep.TenantID,
		ep.AgentID,
		ep.UserID,
		ep.SessionKey,
		ep.Summary,
		ep.L0Abstract,
		pq.Array(ep.KeyTopics),
		embStr,
		ep.SourceType,
		nilStr(ep.SourceID),
		ep.TurnCount,
		ep.TokenCount,
		ep.CreatedAt,
		nilTime(ep.ExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("episodic create: %w", err)
	}
	return nil
}

// Get retrieves an episodic summary by ID within the current tenant.
func (s *PGEpisodicStore) Get(ctx context.Context, id string) (*store.EpisodicSummary, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, agent_id, user_id, session_key, summary, l0_abstract,
		       key_topics, source_type, source_id, turn_count, token_count,
		       created_at, expires_at
		FROM episodic_summaries
		WHERE id = $1 AND tenant_id = $2`,
		id, store.TenantIDFromContext(ctx),
	)
	return scanPGEpisodic(row)
}

// Delete removes an episodic summary by ID within the current tenant.
func (s *PGEpisodicStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM episodic_summaries WHERE id = $1 AND tenant_id = $2`,
		id, store.TenantIDFromContext(ctx),
	)
	return err
}

// List returns episodic summaries ordered by created_at DESC.
func (s *PGEpisodicStore) List(ctx context.Context, agentID, userID string, limit, offset int) ([]store.EpisodicSummary, error) {
	if limit <= 0 {
		limit = 20
	}

	aid := mustParseUUID(agentID)
	tid := store.TenantIDFromContext(ctx)

	var (
		rows *sql.Rows
		err  error
	)
	if userID != "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, tenant_id, agent_id, user_id, session_key, summary, l0_abstract,
			       key_topics, source_type, source_id, turn_count, token_count,
			       created_at, expires_at
			FROM episodic_summaries
			WHERE agent_id = $1 AND user_id = $2 AND tenant_id = $3
			ORDER BY created_at DESC
			LIMIT $4 OFFSET $5`,
			aid, userID, tid, limit, offset,
		)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, tenant_id, agent_id, user_id, session_key, summary, l0_abstract,
			       key_topics, source_type, source_id, turn_count, token_count,
			       created_at, expires_at
			FROM episodic_summaries
			WHERE agent_id = $1 AND tenant_id = $2
			ORDER BY created_at DESC
			LIMIT $3 OFFSET $4`,
			aid, tid, limit, offset,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []store.EpisodicSummary
	for rows.Next() {
		ep, scanErr := scanPGEpisodicRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		results = append(results, *ep)
	}
	return results, rows.Err()
}

// ExistsBySourceID checks if a summary has already been captured for a source.
func (s *PGEpisodicStore) ExistsBySourceID(ctx context.Context, agentID, userID, sourceID string) (bool, error) {
	if sourceID == "" {
		return false, nil
	}

	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM episodic_summaries
			WHERE agent_id = $1
			  AND user_id = $2
			  AND source_id = $3
			  AND tenant_id = $4
		)`,
		mustParseUUID(agentID), userID, sourceID, store.TenantIDFromContext(ctx),
	).Scan(&exists)
	return exists, err
}

func scanPGEpisodic(row *sql.Row) (*store.EpisodicSummary, error) {
	var (
		ep         store.EpisodicSummary
		topics     pq.StringArray
		sourceType sql.NullString
		sourceID   sql.NullString
	)
	if err := row.Scan(
		&ep.ID,
		&ep.TenantID,
		&ep.AgentID,
		&ep.UserID,
		&ep.SessionKey,
		&ep.Summary,
		&ep.L0Abstract,
		&topics,
		&sourceType,
		&sourceID,
		&ep.TurnCount,
		&ep.TokenCount,
		&ep.CreatedAt,
		&ep.ExpiresAt,
	); err != nil {
		return nil, err
	}
	ep.KeyTopics = []string(topics)
	ep.SourceType = sourceType.String
	ep.SourceID = sourceID.String
	return &ep, nil
}

func scanPGEpisodicRow(rows *sql.Rows) (*store.EpisodicSummary, error) {
	var (
		ep         store.EpisodicSummary
		topics     pq.StringArray
		sourceType sql.NullString
		sourceID   sql.NullString
	)
	if err := rows.Scan(
		&ep.ID,
		&ep.TenantID,
		&ep.AgentID,
		&ep.UserID,
		&ep.SessionKey,
		&ep.Summary,
		&ep.L0Abstract,
		&topics,
		&sourceType,
		&sourceID,
		&ep.TurnCount,
		&ep.TokenCount,
		&ep.CreatedAt,
		&ep.ExpiresAt,
	); err != nil {
		return nil, err
	}
	ep.KeyTopics = []string(topics)
	ep.SourceType = sourceType.String
	ep.SourceID = sourceID.String
	return &ep, nil
}

var _ store.EpisodicStore = (*PGEpisodicStore)(nil)
