package lopphopolldedupe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

var errClaimNotFound = errors.New("lớp phó poll dedupe claim not found")

type Store struct {
	db *sql.DB
}

type scanner interface {
	Scan(dest ...any) error
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Migrate() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("dedupe store is unavailable")
	}
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_lop_pho_poll_dedupe_claims (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			dedupe_key TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			thread_id INTEGER NOT NULL DEFAULT 0,
			local_key TEXT NOT NULL DEFAULT '',
			target_key TEXT NOT NULL DEFAULT '',
			target_label TEXT NOT NULL DEFAULT '',
			started_by_id TEXT NOT NULL DEFAULT '',
			started_by_label TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			owner_token TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			poll_id TEXT NOT NULL DEFAULT '',
			poll_message_id INTEGER NOT NULL DEFAULT 0,
			suppressed_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			window_started_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			claimed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			lease_expires_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			first_seen_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_seen_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, dedupe_key)
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_lop_pho_poll_dedupe_scope ON beta_lop_pho_poll_dedupe_claims(tenant_id, chat_id, thread_id, updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_lop_pho_poll_dedupe_target ON beta_lop_pho_poll_dedupe_claims(tenant_id, target_key, window_started_at)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_lop_pho_poll_dedupe_status ON beta_lop_pho_poll_dedupe_claims(tenant_id, status, lease_expires_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) BeginClaim(ctx context.Context, input ClaimRequest) (*ClaimDecision, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("dedupe store is unavailable")
	}

	input.TenantID = strings.TrimSpace(input.TenantID)
	input.Channel = strings.TrimSpace(input.Channel)
	input.ChatID = strings.TrimSpace(input.ChatID)
	input.ThreadID = normalizeThreadID(input.ThreadID)
	input.LocalKey = strings.TrimSpace(input.LocalKey)
	input.TargetKey = strings.TrimSpace(input.TargetKey)
	input.TargetLabel = strings.TrimSpace(input.TargetLabel)
	input.StartedByID = strings.TrimSpace(input.StartedByID)
	input.StartedByLabel = strings.TrimSpace(input.StartedByLabel)
	input.Source = strings.TrimSpace(input.Source)
	if input.LocalKey == "" {
		input.LocalKey = composeScopeKey(input.ChatID, input.ThreadID)
	}
	if input.TenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	if input.ChatID == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	if input.TargetKey == "" {
		return nil, fmt.Errorf("target_key is required")
	}

	now := time.Now().UTC()
	dedupeKey, windowStartedAt := buildDedupeKey(input.ChatID, input.ThreadID, input.TargetKey, now)
	claim := &DedupeClaim{
		ID:              uuid.NewString(),
		TenantID:        input.TenantID,
		DedupeKey:       dedupeKey,
		Channel:         input.Channel,
		ChatID:          input.ChatID,
		ThreadID:        input.ThreadID,
		LocalKey:        input.LocalKey,
		TargetKey:       input.TargetKey,
		TargetLabel:     input.TargetLabel,
		StartedByID:     input.StartedByID,
		StartedByLabel:  input.StartedByLabel,
		Source:          input.Source,
		OwnerToken:      uuid.NewString(),
		Status:          claimStatusPending,
		WindowStartedAt: windowStartedAt,
		ClaimedAt:       now,
		LeaseExpiresAt:  now.Add(claimLease),
		FirstSeenAt:     now,
		LastSeenAt:      now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	insertRes, err := s.db.ExecContext(ctx, `
		INSERT INTO beta_lop_pho_poll_dedupe_claims (
			id, tenant_id, dedupe_key, channel, chat_id, thread_id, local_key, target_key, target_label,
			started_by_id, started_by_label, source, owner_token, status, poll_id, poll_message_id,
			suppressed_count, last_error, window_started_at, claimed_at, lease_expires_at, first_seen_at,
			last_seen_at, created_at, updated_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, '', 0,
			0, '', $15, $16, $17, $18, $19, $20, $21
		)
		ON CONFLICT (tenant_id, dedupe_key) DO NOTHING`,
		claim.ID, claim.TenantID, claim.DedupeKey, claim.Channel, claim.ChatID, claim.ThreadID, claim.LocalKey,
		claim.TargetKey, claim.TargetLabel, claim.StartedByID, claim.StartedByLabel, claim.Source, claim.OwnerToken,
		claim.Status, claim.WindowStartedAt, claim.ClaimedAt, claim.LeaseExpiresAt, claim.FirstSeenAt, claim.LastSeenAt,
		claim.CreatedAt, claim.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if rows, _ := insertRes.RowsAffected(); rows > 0 {
		return &ClaimDecision{Claim: claim, Acquired: true}, nil
	}

	existing, err := s.getClaimByKey(ctx, input.TenantID, dedupeKey)
	if err != nil {
		return nil, err
	}

	canReclaim := existing.Status == claimStatusFailed || (existing.Status == claimStatusPending && !existing.LeaseExpiresAt.After(now))
	if canReclaim {
		reclaimRes, reclaimErr := s.db.ExecContext(ctx, `
			UPDATE beta_lop_pho_poll_dedupe_claims
			SET channel=$3, local_key=$4, target_label=$5, started_by_id=$6, started_by_label=$7, source=$8,
				owner_token=$9, status=$10, poll_id='', poll_message_id=0, last_error='',
				claimed_at=$11, lease_expires_at=$12, last_seen_at=$13, updated_at=$14
			WHERE tenant_id=$1 AND dedupe_key=$2
			  AND (status=$15 OR (status=$16 AND lease_expires_at <= $17))`,
			input.TenantID, dedupeKey, input.Channel, input.LocalKey, input.TargetLabel, input.StartedByID,
			input.StartedByLabel, input.Source, claim.OwnerToken, claimStatusPending, now, now.Add(claimLease),
			now, now, claimStatusFailed, claimStatusPending, now,
		)
		if reclaimErr != nil {
			return nil, reclaimErr
		}
		if rows, _ := reclaimRes.RowsAffected(); rows > 0 {
			claim.FirstSeenAt = existing.FirstSeenAt
			claim.CreatedAt = existing.CreatedAt
			return &ClaimDecision{Claim: claim, Acquired: true}, nil
		}
		existing, err = s.getClaimByKey(ctx, input.TenantID, dedupeKey)
		if err != nil {
			return nil, err
		}
	}

	if _, err := s.db.ExecContext(ctx, `
		UPDATE beta_lop_pho_poll_dedupe_claims
		SET suppressed_count=suppressed_count+1, last_seen_at=$3, updated_at=$3
		WHERE tenant_id=$1 AND dedupe_key=$2`,
		input.TenantID, dedupeKey, now,
	); err != nil {
		return nil, err
	}
	existing.SuppressedCount++
	existing.LastSeenAt = now
	existing.UpdatedAt = now

	slog.Info("beta lop pho poll dedupe suppressed duplicate",
		"tenant_id", input.TenantID,
		"chat_id", input.ChatID,
		"thread_id", input.ThreadID,
		"target_key", input.TargetKey,
		"dedupe_key", dedupeKey,
		"status", existing.Status,
		"poll_id", existing.PollID,
		"source", input.Source,
		"suppressed_count", existing.SuppressedCount,
	)

	return &ClaimDecision{
		Claim:     existing,
		Acquired:  false,
		Duplicate: true,
		Pending:   existing.Status == claimStatusPending,
	}, nil
}

func (s *Store) CompleteClaim(ctx context.Context, claimID, ownerToken, pollID string, messageID int) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("dedupe store is unavailable")
	}
	claimID = strings.TrimSpace(claimID)
	ownerToken = strings.TrimSpace(ownerToken)
	pollID = strings.TrimSpace(pollID)
	if claimID == "" || ownerToken == "" {
		return nil
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE beta_lop_pho_poll_dedupe_claims
		SET status=$3, poll_id=$4, poll_message_id=$5, last_error='',
			last_seen_at=$6, lease_expires_at=$6, updated_at=$6
		WHERE id=$1 AND owner_token=$2 AND status=$7`,
		claimID, ownerToken, claimStatusCreated, pollID, messageID, now, claimStatusPending,
	)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return fmt.Errorf("dedupe claim %q is not pending or no longer owned", claimID)
	}
	return nil
}

func (s *Store) FailClaim(ctx context.Context, claimID, ownerToken, lastError string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("dedupe store is unavailable")
	}
	claimID = strings.TrimSpace(claimID)
	ownerToken = strings.TrimSpace(ownerToken)
	if claimID == "" || ownerToken == "" {
		return nil
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE beta_lop_pho_poll_dedupe_claims
		SET status=$3, last_error=$4, lease_expires_at=$5, last_seen_at=$5, updated_at=$5
		WHERE id=$1 AND owner_token=$2 AND status=$6`,
		claimID, ownerToken, claimStatusFailed, strings.TrimSpace(lastError), now, claimStatusPending,
	)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return fmt.Errorf("dedupe claim %q is not pending or no longer owned", claimID)
	}
	return nil
}

func (s *Store) ListClaims(ctx context.Context, tenantID string, filter ClaimFilter) ([]DedupeClaim, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("dedupe store is unavailable")
	}
	tenantID = strings.TrimSpace(tenantID)
	filter.ChatID = strings.TrimSpace(filter.ChatID)
	filter.ThreadID = normalizeThreadID(filter.ThreadID)
	limit := normalizeLimit(filter.Limit)

	query := `
		SELECT id, tenant_id, dedupe_key, channel, chat_id, thread_id, local_key, target_key, target_label,
		       started_by_id, started_by_label, source, owner_token, status, poll_id, poll_message_id,
		       suppressed_count, last_error, window_started_at, claimed_at, lease_expires_at, first_seen_at,
		       last_seen_at, created_at, updated_at
		FROM beta_lop_pho_poll_dedupe_claims
		WHERE tenant_id=$1`
	args := []any{tenantID}
	argIdx := 2
	if filter.ChatID != "" {
		query += fmt.Sprintf(" AND chat_id=$%d", argIdx)
		args = append(args, filter.ChatID)
		argIdx++
	}
	if filter.HasThread {
		query += fmt.Sprintf(" AND thread_id=$%d", argIdx)
		args = append(args, filter.ThreadID)
		argIdx++
	}
	query += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT $%d", argIdx)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	claims := make([]DedupeClaim, 0, limit)
	for rows.Next() {
		claim, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		claims = append(claims, *claim)
	}
	return claims, rows.Err()
}

func (s *Store) getClaimByKey(ctx context.Context, tenantID, dedupeKey string) (*DedupeClaim, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, dedupe_key, channel, chat_id, thread_id, local_key, target_key, target_label,
		       started_by_id, started_by_label, source, owner_token, status, poll_id, poll_message_id,
		       suppressed_count, last_error, window_started_at, claimed_at, lease_expires_at, first_seen_at,
		       last_seen_at, created_at, updated_at
		FROM beta_lop_pho_poll_dedupe_claims
		WHERE tenant_id=$1 AND dedupe_key=$2`,
		strings.TrimSpace(tenantID), strings.TrimSpace(dedupeKey),
	)
	claim, err := scanClaim(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errClaimNotFound
	}
	return claim, err
}

func scanClaim(s scanner) (*DedupeClaim, error) {
	var claim DedupeClaim
	if err := s.Scan(
		&claim.ID,
		&claim.TenantID,
		&claim.DedupeKey,
		&claim.Channel,
		&claim.ChatID,
		&claim.ThreadID,
		&claim.LocalKey,
		&claim.TargetKey,
		&claim.TargetLabel,
		&claim.StartedByID,
		&claim.StartedByLabel,
		&claim.Source,
		&claim.OwnerToken,
		&claim.Status,
		&claim.PollID,
		&claim.PollMessageID,
		&claim.SuppressedCount,
		&claim.LastError,
		&claim.WindowStartedAt,
		&claim.ClaimedAt,
		&claim.LeaseExpiresAt,
		&claim.FirstSeenAt,
		&claim.LastSeenAt,
		&claim.CreatedAt,
		&claim.UpdatedAt,
	); err != nil {
		return nil, err
	}
	claim.ThreadID = normalizeThreadID(claim.ThreadID)
	return &claim, nil
}
