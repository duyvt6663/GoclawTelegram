package researchreviewercodex

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type paperRecord struct {
	ID             string
	TenantID       string
	AgentID        string
	Title          string
	SourceKind     string
	SourceURL      string
	SourceKey      string
	ContentHash    string
	MemoryPath     string
	StructuredJSON json.RawMessage
	RawText        string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (r *paperRecord) structured() (*StructuredPaper, error) {
	if r == nil {
		return nil, fmt.Errorf("paper record is nil")
	}
	var paper StructuredPaper
	if len(r.StructuredJSON) == 0 {
		return &paper, nil
	}
	if err := json.Unmarshal(r.StructuredJSON, &paper); err != nil {
		return nil, err
	}
	return &paper, nil
}

type reviewRecord struct {
	ID           string
	TenantID     string
	AgentID      string
	PaperID      string
	Mode         string
	Focus        string
	Status       string
	PromptText   string
	RelatedJSON  json.RawMessage
	ReportText   string
	ErrorMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (r *reviewRecord) relatedPapers() ([]RelatedPaper, error) {
	if r == nil || len(r.RelatedJSON) == 0 {
		return nil, nil
	}
	var out []RelatedPaper
	if err := json.Unmarshal(r.RelatedJSON, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type featureStatusStats struct {
	IndexedPapers int
	StoredReviews int
	LastReviewID  string
	LastReviewAt  time.Time
}

type featureStore struct {
	db *sql.DB
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_research_reviewer_papers (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			agent_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			source_kind TEXT NOT NULL DEFAULT '',
			source_url TEXT NOT NULL DEFAULT '',
			source_key TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '',
			memory_path TEXT NOT NULL DEFAULT '',
			structured_json TEXT NOT NULL DEFAULT '',
			raw_text TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, source_key)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_research_reviewer_reviews (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			agent_id TEXT NOT NULL DEFAULT '',
			paper_id TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT '',
			focus TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			prompt_text TEXT NOT NULL DEFAULT '',
			related_json TEXT NOT NULL DEFAULT '',
			report_text TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_research_reviewer_papers_tenant_updated ON beta_research_reviewer_papers(tenant_id, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_research_reviewer_papers_memory_path ON beta_research_reviewer_papers(tenant_id, memory_path)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_research_reviewer_reviews_tenant_created ON beta_research_reviewer_reviews(tenant_id, created_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) upsertPaper(record *paperRecord) error {
	if record == nil {
		return nil
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now

	_, err := s.db.Exec(`
		INSERT INTO beta_research_reviewer_papers (
			id, tenant_id, agent_id, title, source_kind, source_url, source_key,
			content_hash, memory_path, structured_json, raw_text, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (tenant_id, source_key)
		DO UPDATE SET
			agent_id=excluded.agent_id,
			title=excluded.title,
			source_kind=excluded.source_kind,
			source_url=excluded.source_url,
			content_hash=excluded.content_hash,
			memory_path=excluded.memory_path,
			structured_json=excluded.structured_json,
			raw_text=excluded.raw_text,
			updated_at=excluded.updated_at`,
		record.ID,
		record.TenantID,
		record.AgentID,
		record.Title,
		record.SourceKind,
		record.SourceURL,
		record.SourceKey,
		record.ContentHash,
		record.MemoryPath,
		string(record.StructuredJSON),
		record.RawText,
		record.CreatedAt,
		record.UpdatedAt,
	)
	return err
}

func (s *featureStore) getPaperByID(tenantID, paperID string) (*paperRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, agent_id, title, source_kind, source_url, source_key,
		       content_hash, memory_path, structured_json, raw_text, created_at, updated_at
		FROM beta_research_reviewer_papers
		WHERE tenant_id=$1 AND id=$2`,
		tenantID, paperID,
	)
	record, err := scanPaperRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("paper not found: %s", paperID)
		}
		return nil, err
	}
	return record, nil
}

func (s *featureStore) getPaperBySourceKey(tenantID, sourceKey string) (*paperRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, agent_id, title, source_kind, source_url, source_key,
		       content_hash, memory_path, structured_json, raw_text, created_at, updated_at
		FROM beta_research_reviewer_papers
		WHERE tenant_id=$1 AND source_key=$2`,
		tenantID, sourceKey,
	)
	record, err := scanPaperRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return record, nil
}

func (s *featureStore) getPaperByMemoryPath(tenantID, memoryPath string) (*paperRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, agent_id, title, source_kind, source_url, source_key,
		       content_hash, memory_path, structured_json, raw_text, created_at, updated_at
		FROM beta_research_reviewer_papers
		WHERE tenant_id=$1 AND memory_path=$2`,
		tenantID, memoryPath,
	)
	record, err := scanPaperRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return record, nil
}

func (s *featureStore) searchPapersByText(tenantID, term, excludePaperID string, limit int) ([]*paperRecord, error) {
	term = strings.TrimSpace(strings.ToLower(term))
	if term == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	pattern := "%" + term + "%"

	rows, err := s.db.Query(`
		SELECT id, tenant_id, agent_id, title, source_kind, source_url, source_key,
		       content_hash, memory_path, structured_json, raw_text, created_at, updated_at
		FROM beta_research_reviewer_papers
		WHERE tenant_id=$1 AND id <> $2
		  AND (LOWER(title) LIKE $3 OR LOWER(raw_text) LIKE $3)
		ORDER BY updated_at DESC
		LIMIT $4`,
		tenantID, excludePaperID, pattern, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*paperRecord
	for rows.Next() {
		record, scanErr := scanPaperRows(rows)
		if scanErr != nil {
			continue
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *featureStore) insertReview(record *reviewRecord) error {
	if record == nil {
		return nil
	}
	_, err := s.db.Exec(`
		INSERT INTO beta_research_reviewer_reviews (
			id, tenant_id, agent_id, paper_id, mode, focus, status, prompt_text,
			related_json, report_text, error_message, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		record.ID,
		record.TenantID,
		record.AgentID,
		record.PaperID,
		record.Mode,
		record.Focus,
		record.Status,
		record.PromptText,
		string(record.RelatedJSON),
		record.ReportText,
		record.ErrorMessage,
		record.CreatedAt,
		record.UpdatedAt,
	)
	return err
}

func (s *featureStore) updateReview(record *reviewRecord) error {
	if record == nil {
		return nil
	}
	_, err := s.db.Exec(`
		UPDATE beta_research_reviewer_reviews
		SET mode=$1, focus=$2, status=$3, prompt_text=$4, related_json=$5,
		    report_text=$6, error_message=$7, updated_at=$8
		WHERE tenant_id=$9 AND id=$10`,
		record.Mode,
		record.Focus,
		record.Status,
		record.PromptText,
		string(record.RelatedJSON),
		record.ReportText,
		record.ErrorMessage,
		record.UpdatedAt,
		record.TenantID,
		record.ID,
	)
	return err
}

func (s *featureStore) getReview(tenantID, reviewID string) (*reviewRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, agent_id, paper_id, mode, focus, status, prompt_text,
		       related_json, report_text, error_message, created_at, updated_at
		FROM beta_research_reviewer_reviews
		WHERE tenant_id=$1 AND id=$2`,
		tenantID, reviewID,
	)

	var (
		record      reviewRecord
		relatedJSON string
	)
	if err := row.Scan(
		&record.ID,
		&record.TenantID,
		&record.AgentID,
		&record.PaperID,
		&record.Mode,
		&record.Focus,
		&record.Status,
		&record.PromptText,
		&relatedJSON,
		&record.ReportText,
		&record.ErrorMessage,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("review not found: %s", reviewID)
		}
		return nil, err
	}
	record.RelatedJSON = json.RawMessage(relatedJSON)
	return &record, nil
}

func (s *featureStore) statusStats(tenantID string) (*featureStatusStats, error) {
	stats := &featureStatusStats{}
	if err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM beta_research_reviewer_papers
		WHERE tenant_id=$1`,
		tenantID,
	).Scan(&stats.IndexedPapers); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM beta_research_reviewer_reviews
		WHERE tenant_id=$1`,
		tenantID,
	).Scan(&stats.StoredReviews); err != nil {
		return nil, err
	}

	row := s.db.QueryRow(`
		SELECT id, created_at
		FROM beta_research_reviewer_reviews
		WHERE tenant_id=$1
		ORDER BY created_at DESC
		LIMIT 1`,
		tenantID,
	)
	if err := row.Scan(&stats.LastReviewID, &stats.LastReviewAt); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	return stats, nil
}

func scanPaperRecord(row *sql.Row) (*paperRecord, error) {
	var (
		record         paperRecord
		structuredJSON string
	)
	if err := row.Scan(
		&record.ID,
		&record.TenantID,
		&record.AgentID,
		&record.Title,
		&record.SourceKind,
		&record.SourceURL,
		&record.SourceKey,
		&record.ContentHash,
		&record.MemoryPath,
		&structuredJSON,
		&record.RawText,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		return nil, err
	}
	record.StructuredJSON = json.RawMessage(structuredJSON)
	return &record, nil
}

func scanPaperRows(rows *sql.Rows) (*paperRecord, error) {
	var (
		record         paperRecord
		structuredJSON string
	)
	if err := rows.Scan(
		&record.ID,
		&record.TenantID,
		&record.AgentID,
		&record.Title,
		&record.SourceKind,
		&record.SourceURL,
		&record.SourceKey,
		&record.ContentHash,
		&record.MemoryPath,
		&structuredJSON,
		&record.RawText,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		return nil, err
	}
	record.StructuredJSON = json.RawMessage(structuredJSON)
	return &record, nil
}
