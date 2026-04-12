package linkupwebsearch

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type cachedSearchRecord struct {
	ID          string
	TenantID    string
	Query       string
	LookupKey   string
	Response    json.RawMessage
	SourceCount int
	FetchedAt   time.Time
	ExpiresAt   time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type searchRunRecord struct {
	ID           string
	TenantID     string
	Query        string
	LookupKey    string
	CacheHit     bool
	Status       string
	SourceCount  int
	ErrorMessage string
	Response     json.RawMessage
	CreatedAt    time.Time
}

type featureStore struct {
	db *sql.DB
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_linkup_web_search_cache (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			query TEXT NOT NULL DEFAULT '',
			query_normalized TEXT NOT NULL DEFAULT '',
			response_json TEXT NOT NULL DEFAULT '',
			source_count INTEGER NOT NULL DEFAULT 0,
			fetched_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, query_normalized)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_linkup_web_search_runs (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			query TEXT NOT NULL DEFAULT '',
			query_normalized TEXT NOT NULL DEFAULT '',
			cache_hit INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT '',
			source_count INTEGER NOT NULL DEFAULT 0,
			error_message TEXT NOT NULL DEFAULT '',
			response_json TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_linkup_web_search_cache_lookup ON beta_linkup_web_search_cache(tenant_id, query_normalized, expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_linkup_web_search_runs_lookup ON beta_linkup_web_search_runs(tenant_id, created_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) getCachedSearch(tenantID, lookupKey string) (*cachedSearchRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, query, query_normalized, response_json, source_count,
		       fetched_at, expires_at, created_at, updated_at
		FROM beta_linkup_web_search_cache
		WHERE tenant_id=$1 AND query_normalized=$2 AND expires_at > $3`,
		tenantID, lookupKey, time.Now().UTC(),
	)

	var (
		record       cachedSearchRecord
		responseJSON string
	)
	if err := row.Scan(
		&record.ID,
		&record.TenantID,
		&record.Query,
		&record.LookupKey,
		&responseJSON,
		&record.SourceCount,
		&record.FetchedAt,
		&record.ExpiresAt,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	record.Response = json.RawMessage(responseJSON)
	return &record, nil
}

func (s *featureStore) upsertCachedSearch(record *cachedSearchRecord) error {
	if record == nil {
		return nil
	}
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if record.FetchedAt.IsZero() {
		record.FetchedAt = now
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = now
	}

	_, err := s.db.Exec(`
		INSERT INTO beta_linkup_web_search_cache (
			id, tenant_id, query, query_normalized, response_json, source_count,
			fetched_at, expires_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (tenant_id, query_normalized)
		DO UPDATE SET
			query=excluded.query,
			response_json=excluded.response_json,
			source_count=excluded.source_count,
			fetched_at=excluded.fetched_at,
			expires_at=excluded.expires_at,
			updated_at=excluded.updated_at`,
		record.ID,
		record.TenantID,
		record.Query,
		record.LookupKey,
		string(record.Response),
		record.SourceCount,
		record.FetchedAt,
		record.ExpiresAt,
		now,
		now,
	)
	return err
}

func (s *featureStore) insertRun(record *searchRunRecord) error {
	if record == nil {
		return nil
	}
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}

	cacheHit := 0
	if record.CacheHit {
		cacheHit = 1
	}

	_, err := s.db.Exec(`
		INSERT INTO beta_linkup_web_search_runs (
			id, tenant_id, query, query_normalized, cache_hit, status, source_count,
			error_message, response_json, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		record.ID,
		record.TenantID,
		record.Query,
		record.LookupKey,
		cacheHit,
		record.Status,
		record.SourceCount,
		record.ErrorMessage,
		string(record.Response),
		record.CreatedAt,
	)
	return err
}
