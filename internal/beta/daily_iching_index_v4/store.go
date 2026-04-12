package dailyichingindexv4

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type snapshotRecord struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenant_id,omitempty"`
	SourceSignature string          `json:"source_signature"`
	IndexVersion    int             `json:"index_version"`
	Extractor       string          `json:"extractor,omitempty"`
	SourceRoot      string          `json:"source_root,omitempty"`
	CachePath       string          `json:"cache_path,omitempty"`
	SourceCount     int             `json:"source_count"`
	HexagramCount   int             `json:"hexagram_count"`
	ChunkCount      int             `json:"chunk_count"`
	GeneratedAt     *time.Time      `json:"generated_at,omitempty"`
	Summary         json.RawMessage `json:"summary,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type compareRunRecord struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenant_id,omitempty"`
	SourceSignature string          `json:"source_signature"`
	IndexVersion    int             `json:"index_version"`
	QueryCount      int             `json:"query_count"`
	Report          json.RawMessage `json:"report,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

type featureStore struct {
	db *sql.DB
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_daily_iching_index_v4_snapshots (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			source_signature TEXT NOT NULL DEFAULT '',
			index_version INTEGER NOT NULL DEFAULT 0,
			extractor TEXT NOT NULL DEFAULT '',
			source_root TEXT NOT NULL DEFAULT '',
			cache_path TEXT NOT NULL DEFAULT '',
			source_count INTEGER NOT NULL DEFAULT 0,
			hexagram_count INTEGER NOT NULL DEFAULT 0,
			chunk_count INTEGER NOT NULL DEFAULT 0,
			generated_at TIMESTAMP NULL,
			summary_json TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, source_signature, index_version)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_daily_iching_index_v4_runs (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			source_signature TEXT NOT NULL DEFAULT '',
			index_version INTEGER NOT NULL DEFAULT 0,
			query_count INTEGER NOT NULL DEFAULT 0,
			report_json TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_daily_iching_index_v4_snapshots_lookup ON beta_daily_iching_index_v4_snapshots(tenant_id, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_daily_iching_index_v4_runs_lookup ON beta_daily_iching_index_v4_runs(tenant_id, created_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) upsertSnapshot(record *snapshotRecord) (*snapshotRecord, error) {
	now := time.Now().UTC()
	if record.ID == "" {
		record.ID = uuid.NewString()
	}

	var summaryJSON string
	if len(record.Summary) > 0 {
		summaryJSON = string(record.Summary)
	}

	_, err := s.db.Exec(`
		INSERT INTO beta_daily_iching_index_v4_snapshots (
			id, tenant_id, source_signature, index_version, extractor, source_root, cache_path,
			source_count, hexagram_count, chunk_count, generated_at, summary_json, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (tenant_id, source_signature, index_version) DO UPDATE SET
			extractor=excluded.extractor,
			source_root=excluded.source_root,
			cache_path=excluded.cache_path,
			source_count=excluded.source_count,
			hexagram_count=excluded.hexagram_count,
			chunk_count=excluded.chunk_count,
			generated_at=excluded.generated_at,
			summary_json=excluded.summary_json,
			updated_at=excluded.updated_at`,
		record.ID,
		record.TenantID,
		record.SourceSignature,
		record.IndexVersion,
		record.Extractor,
		record.SourceRoot,
		record.CachePath,
		record.SourceCount,
		record.HexagramCount,
		record.ChunkCount,
		record.GeneratedAt,
		summaryJSON,
		now,
		now,
	)
	if err != nil {
		return nil, err
	}
	return s.snapshotByKey(record.TenantID, record.SourceSignature, record.IndexVersion)
}

func (s *featureStore) snapshotByKey(tenantID, sourceSignature string, indexVersion int) (*snapshotRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, source_signature, index_version, extractor, source_root, cache_path,
		       source_count, hexagram_count, chunk_count, generated_at, summary_json, created_at, updated_at
		FROM beta_daily_iching_index_v4_snapshots
		WHERE tenant_id=$1 AND source_signature=$2 AND index_version=$3`,
		tenantID, sourceSignature, indexVersion,
	)
	return scanSnapshotRecord(row)
}

func (s *featureStore) latestSnapshot(tenantID string) (*snapshotRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, source_signature, index_version, extractor, source_root, cache_path,
		       source_count, hexagram_count, chunk_count, generated_at, summary_json, created_at, updated_at
		FROM beta_daily_iching_index_v4_snapshots
		WHERE tenant_id=$1
		ORDER BY updated_at DESC
		LIMIT 1`,
		tenantID,
	)
	record, err := scanSnapshotRecord(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return record, err
}

func (s *featureStore) insertRun(record *compareRunRecord) (*compareRunRecord, error) {
	now := time.Now().UTC()
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	var reportJSON string
	if len(record.Report) > 0 {
		reportJSON = string(record.Report)
	}

	_, err := s.db.Exec(`
		INSERT INTO beta_daily_iching_index_v4_runs (
			id, tenant_id, source_signature, index_version, query_count, report_json, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		record.ID,
		record.TenantID,
		record.SourceSignature,
		record.IndexVersion,
		record.QueryCount,
		reportJSON,
		now,
	)
	if err != nil {
		return nil, err
	}
	return s.runByID(record.ID)
}

func (s *featureStore) runByID(id string) (*compareRunRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, source_signature, index_version, query_count, report_json, created_at
		FROM beta_daily_iching_index_v4_runs
		WHERE id=$1`,
		id,
	)
	return scanCompareRunRecord(row)
}

func (s *featureStore) latestRun(tenantID string) (*compareRunRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, source_signature, index_version, query_count, report_json, created_at
		FROM beta_daily_iching_index_v4_runs
		WHERE tenant_id=$1
		ORDER BY created_at DESC
		LIMIT 1`,
		tenantID,
	)
	record, err := scanCompareRunRecord(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return record, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanSnapshotRecord(row scanner) (*snapshotRecord, error) {
	var (
		record      snapshotRecord
		generatedAt sql.NullTime
		summaryJSON string
	)
	if err := row.Scan(
		&record.ID,
		&record.TenantID,
		&record.SourceSignature,
		&record.IndexVersion,
		&record.Extractor,
		&record.SourceRoot,
		&record.CachePath,
		&record.SourceCount,
		&record.HexagramCount,
		&record.ChunkCount,
		&generatedAt,
		&summaryJSON,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if generatedAt.Valid {
		t := generatedAt.Time.UTC()
		record.GeneratedAt = &t
	}
	record.Summary = json.RawMessage(summaryJSON)
	return &record, nil
}

func scanCompareRunRecord(row scanner) (*compareRunRecord, error) {
	var (
		record     compareRunRecord
		reportJSON string
	)
	if err := row.Scan(
		&record.ID,
		&record.TenantID,
		&record.SourceSignature,
		&record.IndexVersion,
		&record.QueryCount,
		&reportJSON,
		&record.CreatedAt,
	); err != nil {
		return nil, err
	}
	record.Report = json.RawMessage(reportJSON)
	return &record, nil
}
