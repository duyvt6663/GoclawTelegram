package zlibrarymcp

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

type featureStore struct {
	db *sql.DB
}

type callRecord struct {
	ID           string
	TenantID     string
	ToolName     string
	Status       string
	ErrorMessage string
	Response     []byte
	CreatedAt    time.Time
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_zlibrary_mcp_setup (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_zlibrary_mcp_calls (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			tool_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			response_json TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_zlibrary_mcp_calls_lookup ON beta_zlibrary_mcp_calls(tenant_id, created_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) setStateBestEffort(ctx context.Context, key, value string) {
	if s == nil || s.db == nil || key == "" {
		return
	}
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO beta_zlibrary_mcp_setup (key, value, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (key)
		DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, time.Now().UTC(),
	)
}

func (s *featureStore) insertCall(record *callRecord) error {
	if s == nil || s.db == nil || record == nil {
		return nil
	}
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO beta_zlibrary_mcp_calls (
			id, tenant_id, tool_name, status, error_message, response_json, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		record.ID,
		record.TenantID,
		record.ToolName,
		record.Status,
		record.ErrorMessage,
		string(record.Response),
		record.CreatedAt,
	)
	return err
}
