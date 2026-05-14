package pdfurlautoreview

import (
	"database/sql"
	"fmt"
	"time"
)

type fetchRecord struct {
	ID                string
	TenantID          string
	SourceURL         string
	ResolvedURL       string
	ContentType       string
	SourceKind        string
	Channel           string
	ChatID            string
	LocalKey          string
	TelegramMessageID string
	SavedPath         string
	FileHash          string
	FileSizeBytes     int64
	Mode              string
	FocusText         string
	FocusKey          string
	UploadID          string
	PaperID           string
	ReviewID          string
	Status            string
	ErrorMessage      string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type featureStatusStats struct {
	FetchCount       int
	CompletedFetches int
	FailedFetches    int
	LastFetchID      string
	LastFetchAt      time.Time
}

type featureStore struct {
	db *sql.DB
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_pdf_url_auto_review_fetches (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			source_url TEXT NOT NULL DEFAULT '',
			resolved_url TEXT NOT NULL DEFAULT '',
			content_type TEXT NOT NULL DEFAULT '',
			source_kind TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			local_key TEXT NOT NULL DEFAULT '',
			telegram_message_id TEXT NOT NULL DEFAULT '',
			saved_path TEXT NOT NULL DEFAULT '',
			file_hash TEXT NOT NULL DEFAULT '',
			file_size_bytes INTEGER NOT NULL DEFAULT 0,
			mode TEXT NOT NULL DEFAULT '',
			focus_text TEXT NOT NULL DEFAULT '',
			focus_key TEXT NOT NULL DEFAULT '',
			upload_id TEXT NOT NULL DEFAULT '',
			paper_id TEXT NOT NULL DEFAULT '',
			review_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_pdf_url_auto_review_tenant_created ON beta_pdf_url_auto_review_fetches(tenant_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_pdf_url_auto_review_tenant_source ON beta_pdf_url_auto_review_fetches(tenant_id, source_url, updated_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) insertFetch(record *fetchRecord) error {
	if record == nil {
		return nil
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now

	_, err := s.db.Exec(`
		INSERT INTO beta_pdf_url_auto_review_fetches (
			id, tenant_id, source_url, resolved_url, content_type, source_kind,
			channel, chat_id, local_key, telegram_message_id, saved_path, file_hash,
			file_size_bytes, mode, focus_text, focus_key, upload_id, paper_id,
			review_id, status, error_message, created_at, updated_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17, $18,
			$19, $20, $21, $22, $23
		)`,
		record.ID,
		record.TenantID,
		record.SourceURL,
		record.ResolvedURL,
		record.ContentType,
		record.SourceKind,
		record.Channel,
		record.ChatID,
		record.LocalKey,
		record.TelegramMessageID,
		record.SavedPath,
		record.FileHash,
		record.FileSizeBytes,
		record.Mode,
		record.FocusText,
		record.FocusKey,
		record.UploadID,
		record.PaperID,
		record.ReviewID,
		record.Status,
		record.ErrorMessage,
		record.CreatedAt,
		record.UpdatedAt,
	)
	return err
}

func (s *featureStore) updateFetch(record *fetchRecord) error {
	if record == nil {
		return nil
	}
	record.UpdatedAt = time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE beta_pdf_url_auto_review_fetches
		SET source_url=$3,
		    resolved_url=$4,
		    content_type=$5,
		    source_kind=$6,
		    channel=$7,
		    chat_id=$8,
		    local_key=$9,
		    telegram_message_id=$10,
		    saved_path=$11,
		    file_hash=$12,
		    file_size_bytes=$13,
		    mode=$14,
		    focus_text=$15,
		    focus_key=$16,
		    upload_id=$17,
		    paper_id=$18,
		    review_id=$19,
		    status=$20,
		    error_message=$21,
		    updated_at=$22
		WHERE tenant_id=$1 AND id=$2`,
		record.TenantID,
		record.ID,
		record.SourceURL,
		record.ResolvedURL,
		record.ContentType,
		record.SourceKind,
		record.Channel,
		record.ChatID,
		record.LocalKey,
		record.TelegramMessageID,
		record.SavedPath,
		record.FileHash,
		record.FileSizeBytes,
		record.Mode,
		record.FocusText,
		record.FocusKey,
		record.UploadID,
		record.PaperID,
		record.ReviewID,
		record.Status,
		record.ErrorMessage,
		record.UpdatedAt,
	)
	return err
}

func (s *featureStore) getFetch(tenantID, fetchID string) (*fetchRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, source_url, resolved_url, content_type, source_kind,
		       channel, chat_id, local_key, telegram_message_id, saved_path, file_hash,
		       file_size_bytes, mode, focus_text, focus_key, upload_id, paper_id,
		       review_id, status, error_message, created_at, updated_at
		FROM beta_pdf_url_auto_review_fetches
		WHERE tenant_id=$1 AND id=$2`,
		tenantID, fetchID,
	)
	record, err := scanFetchRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("fetch not found: %s", fetchID)
		}
		return nil, err
	}
	return record, nil
}

func (s *featureStore) statusStats(tenantID string) (featureStatusStats, error) {
	stats := featureStatusStats{}
	row := s.db.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM beta_pdf_url_auto_review_fetches WHERE tenant_id=$1) AS fetch_count,
			(SELECT COUNT(*) FROM beta_pdf_url_auto_review_fetches WHERE tenant_id=$1 AND status=$2) AS completed_fetches,
			(SELECT COUNT(*) FROM beta_pdf_url_auto_review_fetches WHERE tenant_id=$1 AND status=$3) AS failed_fetches`,
		tenantID, fetchStatusCompleted, fetchStatusFailed,
	)
	if err := row.Scan(&stats.FetchCount, &stats.CompletedFetches, &stats.FailedFetches); err != nil {
		return stats, err
	}

	var lastID sql.NullString
	var lastAt sql.NullTime
	lastRow := s.db.QueryRow(`
		SELECT id, updated_at
		FROM beta_pdf_url_auto_review_fetches
		WHERE tenant_id=$1
		ORDER BY updated_at DESC
		LIMIT 1`,
		tenantID,
	)
	switch err := lastRow.Scan(&lastID, &lastAt); err {
	case nil:
		stats.LastFetchID = lastID.String
		if lastAt.Valid {
			stats.LastFetchAt = lastAt.Time
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

func scanFetchRecord(scanner rowScanner) (*fetchRecord, error) {
	record := &fetchRecord{}
	err := scanner.Scan(
		&record.ID,
		&record.TenantID,
		&record.SourceURL,
		&record.ResolvedURL,
		&record.ContentType,
		&record.SourceKind,
		&record.Channel,
		&record.ChatID,
		&record.LocalKey,
		&record.TelegramMessageID,
		&record.SavedPath,
		&record.FileHash,
		&record.FileSizeBytes,
		&record.Mode,
		&record.FocusText,
		&record.FocusKey,
		&record.UploadID,
		&record.PaperID,
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
