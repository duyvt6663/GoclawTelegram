package dailyichingquery

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type chunkEmbeddingRecord struct {
	ID              string
	TenantID        string
	SourceSignature string
	ProviderName    string
	ProviderModel   string
	SectionNumber   int
	ChunkOrder      int
	ChunkTextHash   string
	Embedding       []float32
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type queryRunRecord struct {
	ID                 string
	TenantID           string
	SourceSignature    string
	ProviderName       string
	ProviderModel      string
	Question           string
	QuestionNormalized string
	Confidence         float64
	LowConfidence      bool
	Response           json.RawMessage
	CreatedAt          time.Time
}

type queryStore interface {
	migrate() error
	listChunkEmbeddings(tenantID, sourceSignature, providerName, providerModel string) ([]chunkEmbeddingRecord, error)
	upsertChunkEmbeddings(records []chunkEmbeddingRecord) error
	insertRun(record *queryRunRecord) error
}

type featureStore struct {
	db *sql.DB
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_daily_iching_query_chunk_embeddings (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			source_signature TEXT NOT NULL DEFAULT '',
			provider_name TEXT NOT NULL DEFAULT '',
			provider_model TEXT NOT NULL DEFAULT '',
			section_number INTEGER NOT NULL DEFAULT 0,
			chunk_order INTEGER NOT NULL DEFAULT 0,
			chunk_text_hash TEXT NOT NULL DEFAULT '',
			embedding_json TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, source_signature, provider_name, provider_model, section_number, chunk_order)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_daily_iching_query_runs (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			source_signature TEXT NOT NULL DEFAULT '',
			provider_name TEXT NOT NULL DEFAULT '',
			provider_model TEXT NOT NULL DEFAULT '',
			question TEXT NOT NULL DEFAULT '',
			question_normalized TEXT NOT NULL DEFAULT '',
			confidence REAL NOT NULL DEFAULT 0,
			low_confidence INTEGER NOT NULL DEFAULT 0,
			response_json TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_daily_iching_query_chunk_embeddings_lookup ON beta_daily_iching_query_chunk_embeddings(tenant_id, source_signature, provider_name, provider_model, section_number, chunk_order)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_daily_iching_query_runs_lookup ON beta_daily_iching_query_runs(tenant_id, created_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) listChunkEmbeddings(tenantID, sourceSignature, providerName, providerModel string) ([]chunkEmbeddingRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, source_signature, provider_name, provider_model, section_number, chunk_order,
		       chunk_text_hash, embedding_json, created_at, updated_at
		FROM beta_daily_iching_query_chunk_embeddings
		WHERE tenant_id=$1 AND source_signature=$2 AND provider_name=$3 AND provider_model=$4
		ORDER BY section_number ASC, chunk_order ASC`,
		tenantID, sourceSignature, providerName, providerModel,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []chunkEmbeddingRecord
	for rows.Next() {
		var (
			record        chunkEmbeddingRecord
			embeddingJSON string
		)
		if err := rows.Scan(
			&record.ID,
			&record.TenantID,
			&record.SourceSignature,
			&record.ProviderName,
			&record.ProviderModel,
			&record.SectionNumber,
			&record.ChunkOrder,
			&record.ChunkTextHash,
			&embeddingJSON,
			&record.CreatedAt,
			&record.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if embeddingJSON != "" {
			if err := json.Unmarshal([]byte(embeddingJSON), &record.Embedding); err != nil {
				return nil, err
			}
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *featureStore) upsertChunkEmbeddings(records []chunkEmbeddingRecord) error {
	if len(records) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	stmt, err := tx.Prepare(`
		INSERT INTO beta_daily_iching_query_chunk_embeddings (
			id, tenant_id, source_signature, provider_name, provider_model, section_number, chunk_order,
			chunk_text_hash, embedding_json, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (tenant_id, source_signature, provider_name, provider_model, section_number, chunk_order)
		DO UPDATE SET
			chunk_text_hash=excluded.chunk_text_hash,
			embedding_json=excluded.embedding_json,
			updated_at=excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, record := range records {
		if record.ID == "" {
			record.ID = uuid.NewString()
		}
		embeddingJSON, err := json.Marshal(record.Embedding)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(
			record.ID,
			record.TenantID,
			record.SourceSignature,
			record.ProviderName,
			record.ProviderModel,
			record.SectionNumber,
			record.ChunkOrder,
			record.ChunkTextHash,
			string(embeddingJSON),
			now,
			now,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *featureStore) insertRun(record *queryRunRecord) error {
	if record == nil {
		return nil
	}
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	lowConfidence := 0
	if record.LowConfidence {
		lowConfidence = 1
	}
	responseJSON := ""
	if len(record.Response) > 0 {
		responseJSON = string(record.Response)
	}

	_, err := s.db.Exec(`
		INSERT INTO beta_daily_iching_query_runs (
			id, tenant_id, source_signature, provider_name, provider_model,
			question, question_normalized, confidence, low_confidence, response_json, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		record.ID,
		record.TenantID,
		record.SourceSignature,
		record.ProviderName,
		record.ProviderModel,
		record.Question,
		record.QuestionNormalized,
		record.Confidence,
		lowConfidence,
		responseJSON,
		record.CreatedAt,
	)
	return err
}
