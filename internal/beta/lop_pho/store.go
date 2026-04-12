package loppho

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

var errLopPhoPollNotFound = errors.New("lớp phó poll not found")

type LopPhoRole struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenant_id,omitempty"`
	UserID          string    `json:"user_id,omitempty"`
	SenderID        string    `json:"sender_id"`
	UserLabel       string    `json:"user_label"`
	GrantedByPollID string    `json:"granted_by_poll_id,omitempty"`
	GrantedAt       time.Time `json:"granted_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type LopPhoPoll struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenant_id,omitempty"`
	PollID          string     `json:"poll_id"`
	Channel         string     `json:"channel"`
	ChatID          string     `json:"chat_id"`
	ThreadID        int        `json:"thread_id"`
	LocalKey        string     `json:"local_key"`
	MessageID       int        `json:"message_id"`
	TargetUserID    string     `json:"target_user_id,omitempty"`
	TargetSenderID  string     `json:"target_sender_id"`
	TargetLabel     string     `json:"target_label"`
	StartedByID     string     `json:"started_by_id,omitempty"`
	StartedByLabel  string     `json:"started_by_label,omitempty"`
	BauVotes        int        `json:"bau_votes"`
	HaHanhKiemVotes int        `json:"ha_hanh_kiem_votes"`
	Status          string     `json:"status"`
	ResultChoice    string     `json:"result_choice,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
}

type PollCreate struct {
	TenantID  string
	PollID    string
	Channel   string
	ChatID    string
	ThreadID  int
	LocalKey  string
	MessageID int
	Target    telegramIdentity
	StartedBy telegramIdentity
}

type featureStore struct {
	db *sql.DB
}

type scanner interface {
	Scan(dest ...any) error
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_lop_pho_roles (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			user_id TEXT NOT NULL DEFAULT '',
			sender_id TEXT NOT NULL DEFAULT '',
			user_label TEXT NOT NULL DEFAULT '',
			granted_by_poll_id TEXT NOT NULL DEFAULT '',
			granted_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, sender_id)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_lop_pho_polls (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			poll_id TEXT NOT NULL,
			channel TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			thread_id INTEGER NOT NULL DEFAULT 0,
			local_key TEXT NOT NULL DEFAULT '',
			message_id INTEGER NOT NULL DEFAULT 0,
			target_user_id TEXT NOT NULL DEFAULT '',
			target_sender_id TEXT NOT NULL DEFAULT '',
			target_label TEXT NOT NULL DEFAULT '',
			started_by_id TEXT NOT NULL DEFAULT '',
			started_by_label TEXT NOT NULL DEFAULT '',
			bau_votes INTEGER NOT NULL DEFAULT 0,
			ha_hanh_kiem_votes INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active',
			result_choice TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			resolved_at TIMESTAMP NULL,
			UNIQUE (tenant_id, poll_id)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_lop_pho_poll_votes (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			poll_id TEXT NOT NULL,
			voter_id TEXT NOT NULL,
			voter_label TEXT NOT NULL DEFAULT '',
			choice TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, poll_id, voter_id)
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_lop_pho_roles_sender ON beta_lop_pho_roles(tenant_id, sender_id)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_lop_pho_polls_target ON beta_lop_pho_polls(tenant_id, channel, chat_id, thread_id, status, target_sender_id)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_lop_pho_polls_scope_target ON beta_lop_pho_polls(tenant_id, chat_id, thread_id, status, target_sender_id, target_user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_lop_pho_votes_poll ON beta_lop_pho_poll_votes(tenant_id, poll_id, choice)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) listRoles(tenantID string) ([]LopPhoRole, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, user_id, sender_id, user_label, granted_by_poll_id, granted_at, updated_at
		FROM beta_lop_pho_roles
		WHERE tenant_id=$1
		ORDER BY granted_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	roles := make([]LopPhoRole, 0)
	for rows.Next() {
		role, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		roles = append(roles, *role)
	}
	return roles, rows.Err()
}

func (s *featureStore) isLopPho(tenantID, senderID string) (bool, error) {
	senderID = strings.TrimSpace(senderID)
	if tenantID == "" || senderID == "" {
		return false, nil
	}
	roles, err := s.listRoles(tenantID)
	if err != nil {
		return false, err
	}
	for _, role := range roles {
		if roleMatchesSender(role, senderID) {
			return true, nil
		}
	}
	return false, nil
}

func (s *featureStore) grantRole(tenantID string, target telegramIdentity, grantedByPollID string) (*LopPhoRole, bool, error) {
	target.SenderID = strings.TrimSpace(target.SenderID)
	target.UserID = strings.TrimSpace(target.UserID)
	target.Label = strings.TrimSpace(target.Label)
	grantedByPollID = strings.TrimSpace(grantedByPollID)
	if tenantID == "" {
		return nil, false, fmt.Errorf("tenant_id is required")
	}
	if target.SenderID == "" {
		return nil, false, fmt.Errorf("target sender_id is required")
	}
	if target.Label == "" {
		target.Label = firstNonEmpty(target.SenderID, target.UserID)
	}

	now := time.Now().UTC()
	roles, err := s.listRoles(tenantID)
	if err != nil {
		return nil, false, err
	}
	for _, role := range roles {
		if !roleMatchesIdentity(role, target) {
			continue
		}
		role.UserID = firstNonEmpty(target.UserID, role.UserID)
		role.SenderID = target.SenderID
		role.UserLabel = firstNonEmpty(target.Label, role.UserLabel)
		role.GrantedByPollID = firstNonEmpty(grantedByPollID, role.GrantedByPollID)
		role.UpdatedAt = now
		if _, err := s.db.Exec(`
			UPDATE beta_lop_pho_roles
			SET user_id=$3, sender_id=$4, user_label=$5, granted_by_poll_id=$6, updated_at=$7
			WHERE id=$1 AND tenant_id=$2`,
			role.ID, tenantID, role.UserID, role.SenderID, role.UserLabel, role.GrantedByPollID, role.UpdatedAt,
		); err != nil {
			return nil, false, err
		}
		return &role, false, nil
	}

	role := &LopPhoRole{
		ID:              uuidString(),
		TenantID:        tenantID,
		UserID:          target.UserID,
		SenderID:        target.SenderID,
		UserLabel:       target.Label,
		GrantedByPollID: grantedByPollID,
		GrantedAt:       now,
		UpdatedAt:       now,
	}
	if _, err := s.db.Exec(`
		INSERT INTO beta_lop_pho_roles (
			id, tenant_id, user_id, sender_id, user_label, granted_by_poll_id, granted_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		role.ID, role.TenantID, role.UserID, role.SenderID, role.UserLabel, role.GrantedByPollID, role.GrantedAt, role.UpdatedAt,
	); err != nil {
		return nil, false, err
	}
	return role, true, nil
}

func (s *featureStore) createPoll(input PollCreate) (*LopPhoPoll, error) {
	input.TenantID = strings.TrimSpace(input.TenantID)
	input.PollID = strings.TrimSpace(input.PollID)
	input.Channel = strings.TrimSpace(input.Channel)
	input.ChatID = strings.TrimSpace(input.ChatID)
	input.ThreadID = normalizeThreadID(input.ThreadID)
	input.LocalKey = strings.TrimSpace(input.LocalKey)
	input.Target.UserID = strings.TrimSpace(input.Target.UserID)
	input.Target.SenderID = strings.TrimSpace(input.Target.SenderID)
	input.Target.Label = strings.TrimSpace(input.Target.Label)
	input.StartedBy.UserID = strings.TrimSpace(input.StartedBy.UserID)
	input.StartedBy.SenderID = strings.TrimSpace(input.StartedBy.SenderID)
	input.StartedBy.Label = strings.TrimSpace(input.StartedBy.Label)
	if input.LocalKey == "" {
		input.LocalKey = composeLocalKey(input.ChatID, input.ThreadID)
	}
	if input.TenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	if input.PollID == "" {
		return nil, fmt.Errorf("poll_id is required")
	}
	if input.Channel == "" {
		return nil, fmt.Errorf("channel is required")
	}
	if input.ChatID == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	if input.Target.SenderID == "" {
		return nil, fmt.Errorf("target sender_id is required")
	}
	if input.Target.Label == "" {
		input.Target.Label = firstNonEmpty(input.Target.SenderID, input.Target.UserID)
	}
	if input.StartedBy.Label == "" {
		input.StartedBy.Label = firstNonEmpty(input.StartedBy.SenderID, input.StartedBy.UserID)
	}

	now := time.Now().UTC()
	poll := &LopPhoPoll{
		ID:             uuidString(),
		TenantID:       input.TenantID,
		PollID:         input.PollID,
		Channel:        input.Channel,
		ChatID:         input.ChatID,
		ThreadID:       input.ThreadID,
		LocalKey:       input.LocalKey,
		MessageID:      input.MessageID,
		TargetUserID:   input.Target.UserID,
		TargetSenderID: input.Target.SenderID,
		TargetLabel:    input.Target.Label,
		StartedByID:    firstNonEmpty(input.StartedBy.UserID, input.StartedBy.SenderID),
		StartedByLabel: input.StartedBy.Label,
		Status:         pollStatusActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, err := s.db.Exec(`
		INSERT INTO beta_lop_pho_polls (
			id, tenant_id, poll_id, channel, chat_id, thread_id, local_key, message_id,
			target_user_id, target_sender_id, target_label, started_by_id, started_by_label,
			bau_votes, ha_hanh_kiem_votes, status, result_choice, created_at, updated_at, resolved_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 0, 0, $14, '', $15, $16, NULL)`,
		poll.ID, poll.TenantID, poll.PollID, poll.Channel, poll.ChatID, poll.ThreadID, poll.LocalKey, poll.MessageID,
		poll.TargetUserID, poll.TargetSenderID, poll.TargetLabel, poll.StartedByID, poll.StartedByLabel,
		poll.Status, poll.CreatedAt, poll.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return poll, nil
}

func (s *featureStore) getPollByPollID(tenantID, pollID string) (*LopPhoPoll, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, poll_id, channel, chat_id, thread_id, local_key, message_id,
		       target_user_id, target_sender_id, target_label, started_by_id, started_by_label,
		       bau_votes, ha_hanh_kiem_votes, status, result_choice, created_at, updated_at, resolved_at
		FROM beta_lop_pho_polls
		WHERE tenant_id=$1 AND poll_id=$2`,
		strings.TrimSpace(tenantID), strings.TrimSpace(pollID),
	)
	poll, err := scanPoll(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errLopPhoPollNotFound
	}
	return poll, err
}

func (s *featureStore) getActivePollByTarget(tenantID, chatID string, threadID int, target telegramIdentity) (*LopPhoPoll, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, poll_id, channel, chat_id, thread_id, local_key, message_id,
		       target_user_id, target_sender_id, target_label, started_by_id, started_by_label,
		       bau_votes, ha_hanh_kiem_votes, status, result_choice, created_at, updated_at, resolved_at
		FROM beta_lop_pho_polls
		WHERE tenant_id=$1 AND chat_id=$2 AND thread_id=$3 AND status=$4
		ORDER BY created_at DESC`,
		strings.TrimSpace(tenantID), strings.TrimSpace(chatID), normalizeThreadID(threadID), pollStatusActive,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		poll, err := scanPoll(rows)
		if err != nil {
			return nil, err
		}
		if pollMatchesTarget(*poll, target) {
			return poll, nil
		}
	}
	return nil, rows.Err()
}

func (s *featureStore) listPolls(tenantID string, filter PollFilter) ([]LopPhoPoll, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, poll_id, channel, chat_id, thread_id, local_key, message_id,
		       target_user_id, target_sender_id, target_label, started_by_id, started_by_label,
		       bau_votes, ha_hanh_kiem_votes, status, result_choice, created_at, updated_at, resolved_at
		FROM beta_lop_pho_polls
		WHERE tenant_id=$1
		ORDER BY created_at DESC`, strings.TrimSpace(tenantID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	filter.Channel = strings.TrimSpace(filter.Channel)
	filter.ChatID = strings.TrimSpace(filter.ChatID)
	filter.ThreadID = normalizeThreadID(filter.ThreadID)
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}

	polls := make([]LopPhoPoll, 0)
	for rows.Next() {
		poll, err := scanPoll(rows)
		if err != nil {
			return nil, err
		}
		if filter.ActiveOnly && poll.Status != pollStatusActive {
			continue
		}
		if filter.Channel != "" && poll.Channel != filter.Channel {
			continue
		}
		if filter.ChatID != "" && poll.ChatID != filter.ChatID {
			continue
		}
		if filter.HasThread && poll.ThreadID != filter.ThreadID {
			continue
		}
		polls = append(polls, *poll)
		if len(polls) >= limit {
			break
		}
	}
	return polls, rows.Err()
}

func (s *featureStore) recordVote(tenantID, pollID string, voter telegramIdentity, choice string) (*LopPhoPoll, string, error) {
	tenantID = strings.TrimSpace(tenantID)
	pollID = strings.TrimSpace(pollID)
	choice = strings.TrimSpace(choice)
	voterID := firstNonEmpty(strings.TrimSpace(voter.UserID), strings.TrimSpace(voter.SenderID))
	if tenantID == "" || pollID == "" || voterID == "" {
		return nil, "", nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	poll, err := s.getPollByPollIDTx(tx, tenantID, pollID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", errLopPhoPollNotFound
		}
		return nil, "", err
	}
	if poll.Status != pollStatusActive {
		return poll, "", nil
	}

	now := time.Now().UTC()
	if choice == "" {
		if _, err := tx.Exec(`
			DELETE FROM beta_lop_pho_poll_votes
			WHERE tenant_id=$1 AND poll_id=$2 AND voter_id=$3`,
			tenantID, pollID, voterID,
		); err != nil {
			return nil, "", err
		}
	} else {
		if _, err := tx.Exec(`
			INSERT INTO beta_lop_pho_poll_votes (
				id, tenant_id, poll_id, voter_id, voter_label, choice, created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (tenant_id, poll_id, voter_id) DO UPDATE SET
				voter_label=excluded.voter_label,
				choice=excluded.choice,
				updated_at=excluded.updated_at`,
			uuidString(), tenantID, pollID, voterID, firstNonEmpty(voter.Label, voterID), choice, now, now,
		); err != nil {
			return nil, "", err
		}
	}

	bauVotes, haVotes, err := s.countVotesTx(tx, tenantID, pollID)
	if err != nil {
		return nil, "", err
	}

	resultChoice := ""
	switch {
	case bauVotes >= bauVoteThreshold:
		resultChoice = voteChoiceBau
	case haVotes >= haHanhKiemVoteThreshold:
		resultChoice = voteChoiceHaHanhKiem
	}

	if resultChoice != "" {
		if _, err := tx.Exec(`
			UPDATE beta_lop_pho_polls
			SET bau_votes=$3, ha_hanh_kiem_votes=$4, status=$5, result_choice=$6, resolved_at=$7, updated_at=$8
			WHERE tenant_id=$1 AND poll_id=$2`,
			tenantID, pollID, bauVotes, haVotes, pollStatusResolved, resultChoice, now, now,
		); err != nil {
			return nil, "", err
		}
	} else {
		if _, err := tx.Exec(`
			UPDATE beta_lop_pho_polls
			SET bau_votes=$3, ha_hanh_kiem_votes=$4, updated_at=$5
			WHERE tenant_id=$1 AND poll_id=$2`,
			tenantID, pollID, bauVotes, haVotes, now,
		); err != nil {
			return nil, "", err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, "", err
	}

	poll.BauVotes = bauVotes
	poll.HaHanhKiemVotes = haVotes
	poll.UpdatedAt = now
	if resultChoice != "" {
		poll.Status = pollStatusResolved
		poll.ResultChoice = resultChoice
		poll.ResolvedAt = &now
	}
	return poll, resultChoice, nil
}

func (s *featureStore) markClosed(tenantID, pollID string) (*LopPhoPoll, error) {
	poll, err := s.getPollByPollID(tenantID, pollID)
	if err != nil {
		if errors.Is(err, errLopPhoPollNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if poll.Status != pollStatusActive {
		return poll, nil
	}

	now := time.Now().UTC()
	if _, err := s.db.Exec(`
		UPDATE beta_lop_pho_polls
		SET status=$3, updated_at=$4
		WHERE tenant_id=$1 AND poll_id=$2 AND status=$5`,
		strings.TrimSpace(tenantID), strings.TrimSpace(pollID), pollStatusClosed, now, pollStatusActive,
	); err != nil {
		return nil, err
	}
	poll.Status = pollStatusClosed
	poll.UpdatedAt = now
	return poll, nil
}

func (s *featureStore) getPollByPollIDTx(tx *sql.Tx, tenantID, pollID string) (*LopPhoPoll, error) {
	row := tx.QueryRow(`
		SELECT id, tenant_id, poll_id, channel, chat_id, thread_id, local_key, message_id,
		       target_user_id, target_sender_id, target_label, started_by_id, started_by_label,
		       bau_votes, ha_hanh_kiem_votes, status, result_choice, created_at, updated_at, resolved_at
		FROM beta_lop_pho_polls
		WHERE tenant_id=$1 AND poll_id=$2`,
		tenantID, pollID,
	)
	return scanPoll(row)
}

func (s *featureStore) countVotesTx(tx *sql.Tx, tenantID, pollID string) (int, int, error) {
	row := tx.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN choice=$3 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN choice=$4 THEN 1 ELSE 0 END), 0)
		FROM beta_lop_pho_poll_votes
		WHERE tenant_id=$1 AND poll_id=$2`,
		tenantID, pollID, voteChoiceBau, voteChoiceHaHanhKiem,
	)
	var bauVotes, haVotes int
	if err := row.Scan(&bauVotes, &haVotes); err != nil {
		return 0, 0, err
	}
	return bauVotes, haVotes, nil
}

func scanRole(s scanner) (*LopPhoRole, error) {
	var role LopPhoRole
	if err := s.Scan(
		&role.ID,
		&role.TenantID,
		&role.UserID,
		&role.SenderID,
		&role.UserLabel,
		&role.GrantedByPollID,
		&role.GrantedAt,
		&role.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &role, nil
}

func scanPoll(s scanner) (*LopPhoPoll, error) {
	var poll LopPhoPoll
	var resolvedAt sql.NullTime
	if err := s.Scan(
		&poll.ID,
		&poll.TenantID,
		&poll.PollID,
		&poll.Channel,
		&poll.ChatID,
		&poll.ThreadID,
		&poll.LocalKey,
		&poll.MessageID,
		&poll.TargetUserID,
		&poll.TargetSenderID,
		&poll.TargetLabel,
		&poll.StartedByID,
		&poll.StartedByLabel,
		&poll.BauVotes,
		&poll.HaHanhKiemVotes,
		&poll.Status,
		&poll.ResultChoice,
		&poll.CreatedAt,
		&poll.UpdatedAt,
		&resolvedAt,
	); err != nil {
		return nil, err
	}
	poll.ThreadID = normalizeThreadID(poll.ThreadID)
	poll.ResolvedAt = nullTimePtr(resolvedAt)
	return &poll, nil
}

func roleMatchesSender(role LopPhoRole, senderID string) bool {
	rules := make([]string, 0, 2)
	if role.SenderID != "" {
		rules = append(rules, role.SenderID)
	}
	if role.UserID != "" {
		rules = append(rules, role.UserID)
	}
	if len(rules) == 0 {
		return false
	}
	return channels.SenderMatchesList(senderID, rules) || channels.SenderMatchesList(userIDFromSenderID(senderID), rules)
}

func roleMatchesIdentity(role LopPhoRole, ident telegramIdentity) bool {
	if ident.UserID != "" && role.UserID != "" && ident.UserID == role.UserID {
		return true
	}
	return roleMatchesSender(role, ident.SenderID)
}

func pollMatchesTarget(poll LopPhoPoll, ident telegramIdentity) bool {
	rules := make([]string, 0, 2)
	if poll.TargetSenderID != "" {
		rules = append(rules, poll.TargetSenderID)
	}
	if poll.TargetUserID != "" {
		rules = append(rules, poll.TargetUserID)
	}
	if len(rules) == 0 {
		return false
	}
	if ident.SenderID != "" && channels.SenderMatchesList(ident.SenderID, rules) {
		return true
	}
	if ident.UserID != "" && channels.SenderMatchesList(ident.UserID, rules) {
		return true
	}
	return false
}

func uuidString() string { return uuid.NewString() }

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}
