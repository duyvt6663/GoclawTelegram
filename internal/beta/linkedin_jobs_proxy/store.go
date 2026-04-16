package linkedinjobsproxy

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type cachedSearchRecord struct {
	ID          string
	TenantID    string
	LookupKey   string
	Query       string
	Response    json.RawMessage
	ResultCount int
	FetchedAt   time.Time
	ExpiresAt   time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type cachedPreviewRecord struct {
	ID           string
	URLHash      string
	CanonicalURL string
	Title        string
	Company      string
	Location     string
	Snippet      string
	Description  string
	PostedAt     *time.Time
	FetchedAt    time.Time
	ExpiresAt    time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type searchRunRecord struct {
	ID           string
	TenantID     string
	LookupKey    string
	Query        string
	CacheHit     bool
	Status       string
	ResultCount  int
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
		CREATE TABLE IF NOT EXISTS beta_linkedin_jobs_proxy_cache (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			lookup_key TEXT NOT NULL DEFAULT '',
			query_text TEXT NOT NULL DEFAULT '',
			response_json TEXT NOT NULL DEFAULT '',
			result_count INTEGER NOT NULL DEFAULT 0,
			fetched_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, lookup_key)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_linkedin_jobs_proxy_previews (
			id TEXT PRIMARY KEY,
			url_hash TEXT NOT NULL DEFAULT '',
			canonical_url TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			company TEXT NOT NULL DEFAULT '',
			location_text TEXT NOT NULL DEFAULT '',
			snippet TEXT NOT NULL DEFAULT '',
			description_text TEXT NOT NULL DEFAULT '',
			posted_at TIMESTAMP NULL,
			fetched_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (url_hash)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_linkedin_jobs_proxy_runs (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			lookup_key TEXT NOT NULL DEFAULT '',
			query_text TEXT NOT NULL DEFAULT '',
			cache_hit INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT '',
			result_count INTEGER NOT NULL DEFAULT 0,
			error_message TEXT NOT NULL DEFAULT '',
			response_json TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_linkedin_jobs_proxy_cache_lookup ON beta_linkedin_jobs_proxy_cache(tenant_id, lookup_key, expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_linkedin_jobs_proxy_previews_lookup ON beta_linkedin_jobs_proxy_previews(url_hash, expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_linkedin_jobs_proxy_runs_lookup ON beta_linkedin_jobs_proxy_runs(tenant_id, created_at DESC)`,
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
		SELECT id, tenant_id, lookup_key, query_text, response_json, result_count,
		       fetched_at, expires_at, created_at, updated_at
		FROM beta_linkedin_jobs_proxy_cache
		WHERE tenant_id=$1 AND lookup_key=$2 AND expires_at > $3`,
		tenantID, lookupKey, time.Now().UTC(),
	)

	var record cachedSearchRecord
	var responseJSON string
	if err := row.Scan(
		&record.ID,
		&record.TenantID,
		&record.LookupKey,
		&record.Query,
		&responseJSON,
		&record.ResultCount,
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
		INSERT INTO beta_linkedin_jobs_proxy_cache (
			id, tenant_id, lookup_key, query_text, response_json, result_count,
			fetched_at, expires_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (tenant_id, lookup_key)
		DO UPDATE SET
			query_text=EXCLUDED.query_text,
			response_json=EXCLUDED.response_json,
			result_count=EXCLUDED.result_count,
			fetched_at=EXCLUDED.fetched_at,
			expires_at=EXCLUDED.expires_at,
			updated_at=EXCLUDED.updated_at`,
		record.ID,
		record.TenantID,
		record.LookupKey,
		record.Query,
		string(record.Response),
		record.ResultCount,
		record.FetchedAt,
		record.ExpiresAt,
		now,
		now,
	)
	return err
}

func (s *featureStore) getPreview(urlHash string) (*cachedPreviewRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, url_hash, canonical_url, title, company, location_text, snippet,
		       description_text, posted_at, fetched_at, expires_at, created_at, updated_at
		FROM beta_linkedin_jobs_proxy_previews
		WHERE url_hash=$1 AND expires_at > $2`,
		urlHash, time.Now().UTC(),
	)

	var record cachedPreviewRecord
	var postedAt sql.NullTime
	if err := row.Scan(
		&record.ID,
		&record.URLHash,
		&record.CanonicalURL,
		&record.Title,
		&record.Company,
		&record.Location,
		&record.Snippet,
		&record.Description,
		&postedAt,
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
	if postedAt.Valid {
		ts := postedAt.Time.UTC()
		record.PostedAt = &ts
	}
	return &record, nil
}

func (s *featureStore) upsertPreview(record *cachedPreviewRecord) error {
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
		INSERT INTO beta_linkedin_jobs_proxy_previews (
			id, url_hash, canonical_url, title, company, location_text, snippet,
			description_text, posted_at, fetched_at, expires_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (url_hash)
		DO UPDATE SET
			canonical_url=EXCLUDED.canonical_url,
			title=EXCLUDED.title,
			company=EXCLUDED.company,
			location_text=EXCLUDED.location_text,
			snippet=EXCLUDED.snippet,
			description_text=EXCLUDED.description_text,
			posted_at=EXCLUDED.posted_at,
			fetched_at=EXCLUDED.fetched_at,
			expires_at=EXCLUDED.expires_at,
			updated_at=EXCLUDED.updated_at`,
		record.ID,
		record.URLHash,
		record.CanonicalURL,
		record.Title,
		record.Company,
		record.Location,
		record.Snippet,
		record.Description,
		record.PostedAt,
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
		INSERT INTO beta_linkedin_jobs_proxy_runs (
			id, tenant_id, lookup_key, query_text, cache_hit, status, result_count,
			error_message, response_json, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		record.ID,
		record.TenantID,
		record.LookupKey,
		record.Query,
		cacheHit,
		record.Status,
		record.ResultCount,
		record.ErrorMessage,
		string(record.Response),
		record.CreatedAt,
	)
	return err
}
