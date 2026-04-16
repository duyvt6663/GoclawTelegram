package telegrampdfautoreview

import (
	"database/sql"
	"fmt"
	"time"
)

type fileCacheRecord struct {
	TenantID         string
	FileHash         string
	TelegramFileID   string
	TelegramUniqueID string
	OriginalFileName string
	MIMEType         string
	SavedPDFPath     string
	PaperID          string
	FileSizeBytes    int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type uploadRecord struct {
	ID                string
	TenantID          string
	FileHash          string
	Channel           string
	ChatID            string
	LocalKey          string
	TelegramMessageID string
	TelegramFileID    string
	TelegramUniqueID  string
	OriginalFileName  string
	CaptionText       string
	SavedPDFPath      string
	PaperID           string
	FileSizeBytes     int64
	Mode              string
	FocusText         string
	FocusKey          string
	ReviewID          string
	Status            string
	ErrorMessage      string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type featureStatusStats struct {
	CachedFiles      int
	UploadCount      int
	CompletedUploads int
	FailedUploads    int
	LastUploadID     string
	LastUploadAt     time.Time
}

type featureStore struct {
	db *sql.DB
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_telegram_pdf_auto_review_files (
			tenant_id TEXT NOT NULL DEFAULT '',
			file_hash TEXT NOT NULL DEFAULT '',
			telegram_file_id TEXT NOT NULL DEFAULT '',
			telegram_unique_id TEXT NOT NULL DEFAULT '',
			original_file_name TEXT NOT NULL DEFAULT '',
			mime_type TEXT NOT NULL DEFAULT '',
			saved_pdf_path TEXT NOT NULL DEFAULT '',
			paper_id TEXT NOT NULL DEFAULT '',
			file_size_bytes INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (tenant_id, file_hash)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_telegram_pdf_auto_review_uploads (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			file_hash TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			local_key TEXT NOT NULL DEFAULT '',
			telegram_message_id TEXT NOT NULL DEFAULT '',
			telegram_file_id TEXT NOT NULL DEFAULT '',
			telegram_unique_id TEXT NOT NULL DEFAULT '',
			original_file_name TEXT NOT NULL DEFAULT '',
			caption_text TEXT NOT NULL DEFAULT '',
			saved_pdf_path TEXT NOT NULL DEFAULT '',
			paper_id TEXT NOT NULL DEFAULT '',
			file_size_bytes INTEGER NOT NULL DEFAULT 0,
			mode TEXT NOT NULL DEFAULT '',
			focus_text TEXT NOT NULL DEFAULT '',
			focus_key TEXT NOT NULL DEFAULT '',
			review_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_telegram_pdf_auto_review_uploads_tenant_created ON beta_telegram_pdf_auto_review_uploads(tenant_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_telegram_pdf_auto_review_uploads_cache ON beta_telegram_pdf_auto_review_uploads(tenant_id, file_hash, mode, focus_key, status, updated_at DESC)`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) upsertFile(record *fileCacheRecord) error {
	if record == nil {
		return nil
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now

	_, err := s.db.Exec(`
		INSERT INTO beta_telegram_pdf_auto_review_files (
			tenant_id, file_hash, telegram_file_id, telegram_unique_id, original_file_name,
			mime_type, saved_pdf_path, paper_id, file_size_bytes, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (tenant_id, file_hash)
		DO UPDATE SET
			telegram_file_id=excluded.telegram_file_id,
			telegram_unique_id=excluded.telegram_unique_id,
			original_file_name=excluded.original_file_name,
			mime_type=excluded.mime_type,
			saved_pdf_path=excluded.saved_pdf_path,
			paper_id=excluded.paper_id,
			file_size_bytes=excluded.file_size_bytes,
			updated_at=excluded.updated_at`,
		record.TenantID,
		record.FileHash,
		record.TelegramFileID,
		record.TelegramUniqueID,
		record.OriginalFileName,
		record.MIMEType,
		record.SavedPDFPath,
		record.PaperID,
		record.FileSizeBytes,
		record.CreatedAt,
		record.UpdatedAt,
	)
	return err
}

func (s *featureStore) getFileByHash(tenantID, fileHash string) (*fileCacheRecord, error) {
	row := s.db.QueryRow(`
		SELECT tenant_id, file_hash, telegram_file_id, telegram_unique_id, original_file_name,
		       mime_type, saved_pdf_path, paper_id, file_size_bytes, created_at, updated_at
		FROM beta_telegram_pdf_auto_review_files
		WHERE tenant_id=$1 AND file_hash=$2`,
		tenantID, fileHash,
	)

	record := &fileCacheRecord{}
	if err := row.Scan(
		&record.TenantID,
		&record.FileHash,
		&record.TelegramFileID,
		&record.TelegramUniqueID,
		&record.OriginalFileName,
		&record.MIMEType,
		&record.SavedPDFPath,
		&record.PaperID,
		&record.FileSizeBytes,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return record, nil
}

func (s *featureStore) insertUpload(record *uploadRecord) error {
	if record == nil {
		return nil
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now

	_, err := s.db.Exec(`
		INSERT INTO beta_telegram_pdf_auto_review_uploads (
			id, tenant_id, file_hash, channel, chat_id, local_key, telegram_message_id,
			telegram_file_id, telegram_unique_id, original_file_name, caption_text,
			saved_pdf_path, paper_id, file_size_bytes, mode, focus_text, focus_key,
			review_id, status, error_message, created_at, updated_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14, $15, $16, $17,
			$18, $19, $20, $21, $22
		)`,
		record.ID,
		record.TenantID,
		record.FileHash,
		record.Channel,
		record.ChatID,
		record.LocalKey,
		record.TelegramMessageID,
		record.TelegramFileID,
		record.TelegramUniqueID,
		record.OriginalFileName,
		record.CaptionText,
		record.SavedPDFPath,
		record.PaperID,
		record.FileSizeBytes,
		record.Mode,
		record.FocusText,
		record.FocusKey,
		record.ReviewID,
		record.Status,
		record.ErrorMessage,
		record.CreatedAt,
		record.UpdatedAt,
	)
	return err
}

func (s *featureStore) updateUpload(record *uploadRecord) error {
	if record == nil {
		return nil
	}
	record.UpdatedAt = time.Now().UTC()

	_, err := s.db.Exec(`
		UPDATE beta_telegram_pdf_auto_review_uploads
		SET file_hash=$3,
		    channel=$4,
		    chat_id=$5,
		    local_key=$6,
		    telegram_message_id=$7,
		    telegram_file_id=$8,
		    telegram_unique_id=$9,
		    original_file_name=$10,
		    caption_text=$11,
		    saved_pdf_path=$12,
		    paper_id=$13,
		    file_size_bytes=$14,
		    mode=$15,
		    focus_text=$16,
		    focus_key=$17,
		    review_id=$18,
		    status=$19,
		    error_message=$20,
		    updated_at=$21
		WHERE tenant_id=$1 AND id=$2`,
		record.TenantID,
		record.ID,
		record.FileHash,
		record.Channel,
		record.ChatID,
		record.LocalKey,
		record.TelegramMessageID,
		record.TelegramFileID,
		record.TelegramUniqueID,
		record.OriginalFileName,
		record.CaptionText,
		record.SavedPDFPath,
		record.PaperID,
		record.FileSizeBytes,
		record.Mode,
		record.FocusText,
		record.FocusKey,
		record.ReviewID,
		record.Status,
		record.ErrorMessage,
		record.UpdatedAt,
	)
	return err
}

func (s *featureStore) getUpload(tenantID, uploadID string) (*uploadRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, file_hash, channel, chat_id, local_key, telegram_message_id,
		       telegram_file_id, telegram_unique_id, original_file_name, caption_text,
		       saved_pdf_path, paper_id, file_size_bytes, mode, focus_text, focus_key,
		       review_id, status, error_message, created_at, updated_at
		FROM beta_telegram_pdf_auto_review_uploads
		WHERE tenant_id=$1 AND id=$2`,
		tenantID, uploadID,
	)
	record, err := scanUploadRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("upload not found: %s", uploadID)
		}
		return nil, err
	}
	return record, nil
}

func (s *featureStore) getLatestCompletedUploadByCacheKey(tenantID, fileHash, mode, focusKey string) (*uploadRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, file_hash, channel, chat_id, local_key, telegram_message_id,
		       telegram_file_id, telegram_unique_id, original_file_name, caption_text,
		       saved_pdf_path, paper_id, file_size_bytes, mode, focus_text, focus_key,
		       review_id, status, error_message, created_at, updated_at
		FROM beta_telegram_pdf_auto_review_uploads
		WHERE tenant_id=$1 AND file_hash=$2 AND mode=$3 AND focus_key=$4 AND status=$5 AND review_id <> ''
		ORDER BY updated_at DESC
		LIMIT 1`,
		tenantID, fileHash, mode, focusKey, uploadStatusCompleted,
	)
	record, err := scanUploadRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return record, nil
}

func (s *featureStore) statusStats(tenantID string) (featureStatusStats, error) {
	stats := featureStatusStats{}
	row := s.db.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM beta_telegram_pdf_auto_review_files WHERE tenant_id=$1) AS cached_files,
			(SELECT COUNT(*) FROM beta_telegram_pdf_auto_review_uploads WHERE tenant_id=$1) AS upload_count,
			(SELECT COUNT(*) FROM beta_telegram_pdf_auto_review_uploads WHERE tenant_id=$1 AND status=$2) AS completed_uploads,
			(SELECT COUNT(*) FROM beta_telegram_pdf_auto_review_uploads WHERE tenant_id=$1 AND status=$3) AS failed_uploads`,
		tenantID, uploadStatusCompleted, uploadStatusFailed,
	)
	if err := row.Scan(&stats.CachedFiles, &stats.UploadCount, &stats.CompletedUploads, &stats.FailedUploads); err != nil {
		return stats, err
	}

	var lastID sql.NullString
	var lastAt sql.NullTime
	lastRow := s.db.QueryRow(`
		SELECT id, updated_at
		FROM beta_telegram_pdf_auto_review_uploads
		WHERE tenant_id=$1
		ORDER BY updated_at DESC
		LIMIT 1`,
		tenantID,
	)
	switch err := lastRow.Scan(&lastID, &lastAt); err {
	case nil:
		stats.LastUploadID = lastID.String
		if lastAt.Valid {
			stats.LastUploadAt = lastAt.Time
		}
	case sql.ErrNoRows:
	default:
		return stats, err
	}

	return stats, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUploadRecord(scanner rowScanner) (*uploadRecord, error) {
	record := &uploadRecord{}
	err := scanner.Scan(
		&record.ID,
		&record.TenantID,
		&record.FileHash,
		&record.Channel,
		&record.ChatID,
		&record.LocalKey,
		&record.TelegramMessageID,
		&record.TelegramFileID,
		&record.TelegramUniqueID,
		&record.OriginalFileName,
		&record.CaptionText,
		&record.SavedPDFPath,
		&record.PaperID,
		&record.FileSizeBytes,
		&record.Mode,
		&record.FocusText,
		&record.FocusKey,
		&record.ReviewID,
		&record.Status,
		&record.ErrorMessage,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return record, nil
}
