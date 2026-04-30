package gptimageedit

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

const (
	runStatusCompleted = "completed"
	runStatusFailed    = "failed"
)

type runRecord struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	Prompt       string    `json:"prompt,omitempty"`
	Operation    string    `json:"operation"`
	InputSource  string    `json:"input_source,omitempty"`
	InputMIME    string    `json:"input_mime,omitempty"`
	InputBytes   int64     `json:"input_bytes,omitempty"`
	OutputPath   string    `json:"output_path,omitempty"`
	OutputMIME   string    `json:"output_mime,omitempty"`
	OutputBytes  int64     `json:"output_bytes,omitempty"`
	OutputFormat string    `json:"output_format,omitempty"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message,omitempty"`
	LatencyMS    int64     `json:"latency_ms,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type featureStore struct {
	db *sql.DB
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_gpt_image_edit_runs (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			prompt TEXT NOT NULL DEFAULT '',
			operation TEXT NOT NULL DEFAULT '',
			input_source TEXT NOT NULL DEFAULT '',
			input_mime TEXT NOT NULL DEFAULT '',
			input_bytes INTEGER NOT NULL DEFAULT 0,
			output_path TEXT NOT NULL DEFAULT '',
			output_mime TEXT NOT NULL DEFAULT '',
			output_bytes INTEGER NOT NULL DEFAULT 0,
			output_format TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			latency_ms INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_gpt_image_edit_runs_lookup ON beta_gpt_image_edit_runs(tenant_id, created_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) insertRun(record *runRecord) error {
	if record == nil {
		return nil
	}
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO beta_gpt_image_edit_runs (
			id, tenant_id, prompt, operation, input_source, input_mime, input_bytes,
			output_path, output_mime, output_bytes, output_format, status,
			error_message, latency_ms, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		record.ID,
		record.TenantID,
		record.Prompt,
		record.Operation,
		record.InputSource,
		record.InputMIME,
		record.InputBytes,
		record.OutputPath,
		record.OutputMIME,
		record.OutputBytes,
		record.OutputFormat,
		record.Status,
		record.ErrorMessage,
		record.LatencyMS,
		record.CreatedAt,
	)
	return err
}

func (s *featureStore) listRecentRuns(tenantID string, limit int) ([]runRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`
		SELECT id, tenant_id, prompt, operation, input_source, input_mime, input_bytes,
		       output_path, output_mime, output_bytes, output_format, status,
		       error_message, latency_ms, created_at
		FROM beta_gpt_image_edit_runs
		WHERE tenant_id=$1
		ORDER BY created_at DESC
		LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []runRecord
	for rows.Next() {
		var record runRecord
		if err := rows.Scan(
			&record.ID,
			&record.TenantID,
			&record.Prompt,
			&record.Operation,
			&record.InputSource,
			&record.InputMIME,
			&record.InputBytes,
			&record.OutputPath,
			&record.OutputMIME,
			&record.OutputBytes,
			&record.OutputFormat,
			&record.Status,
			&record.ErrorMessage,
			&record.LatencyMS,
			&record.CreatedAt,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
