package dailyiching

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	errDailyIChingConfigNotFound = errors.New("daily i ching config not found")
	errDailyIChingProgressAbsent = errors.New("daily i ching progress not found")
)

type DailyIChingConfig struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id,omitempty"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Channel   string    `json:"channel"`
	ChatID    string    `json:"chat_id"`
	ThreadID  int       `json:"thread_id"`
	Timezone  string    `json:"timezone"`
	PostTime  string    `json:"post_time"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (cfg DailyIChingConfig) withDefaults() DailyIChingConfig {
	cfg.Key = normalizeConfigKey(cfg.Key)
	cfg.ThreadID = normalizeThreadID(cfg.ThreadID)
	if cfg.Name == "" {
		cfg.Name = cfg.Key
	}
	if cfg.Channel == "" {
		cfg.Channel = "telegram"
	}
	if cfg.Timezone == "" {
		cfg.Timezone = defaultTimezone
	}
	if cfg.PostTime == "" {
		cfg.PostTime = defaultPostTime
	}
	return cfg
}

func (cfg DailyIChingConfig) localNow(now time.Time) time.Time {
	return now.In(loadLocation(cfg.Timezone))
}

func (cfg DailyIChingConfig) localDate(now time.Time) string {
	return cfg.localNow(now).Format("2006-01-02")
}

type ProgressState struct {
	ID                  string     `json:"id"`
	TenantID            string     `json:"tenant_id,omitempty"`
	ConfigID            string     `json:"config_id"`
	SequenceIndex       int        `json:"sequence_index"`
	CurrentHexagram     int        `json:"current_hexagram"`
	LastPostedLocalDate string     `json:"last_posted_local_date,omitempty"`
	LastPostedAt        *time.Time `json:"last_posted_at,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type LessonPost struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id,omitempty"`
	ConfigID      string    `json:"config_id"`
	LocalDate     string    `json:"local_date"`
	PostKind      string    `json:"post_kind"`
	TriggerKind   string    `json:"trigger_kind"`
	SequenceIndex int       `json:"sequence_index"`
	Hexagram      int       `json:"hexagram"`
	CreatedAt     time.Time `json:"created_at"`
}

type featureStore struct {
	db *sql.DB
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_daily_iching_configs (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_key TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			thread_id INTEGER NOT NULL DEFAULT 0,
			timezone TEXT NOT NULL DEFAULT '` + defaultTimezone + `',
			post_time TEXT NOT NULL DEFAULT '` + defaultPostTime + `',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, config_key),
			UNIQUE (tenant_id, channel, chat_id, thread_id)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_daily_iching_progress (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_id TEXT NOT NULL,
			sequence_index INTEGER NOT NULL DEFAULT 0,
			current_hexagram INTEGER NOT NULL DEFAULT 0,
			last_posted_local_date TEXT NOT NULL DEFAULT '',
			last_posted_at TIMESTAMP NULL,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, config_id)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_daily_iching_posts (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_id TEXT NOT NULL,
			local_date TEXT NOT NULL,
			post_kind TEXT NOT NULL DEFAULT '` + postKindLesson + `',
			trigger_kind TEXT NOT NULL DEFAULT '` + triggerKindScheduled + `',
			sequence_index INTEGER NOT NULL DEFAULT 0,
			hexagram INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_daily_iching_configs_enabled ON beta_daily_iching_configs(tenant_id, enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_daily_iching_progress_lookup ON beta_daily_iching_progress(tenant_id, config_id)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_daily_iching_posts_lookup ON beta_daily_iching_posts(tenant_id, config_id, local_date, post_kind, created_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) upsertConfig(cfg *DailyIChingConfig) (*DailyIChingConfig, error) {
	cfgValue := cfg.withDefaults()
	now := time.Now().UTC()

	existing, err := s.getConfigByKey(cfgValue.TenantID, cfgValue.Key)
	switch {
	case err == nil:
		cfgValue.ID = existing.ID
		cfgValue.CreatedAt = existing.CreatedAt
		cfgValue.UpdatedAt = now
		_, err = s.db.Exec(`
			UPDATE beta_daily_iching_configs
			SET name=$3, channel=$4, chat_id=$5, thread_id=$6, timezone=$7,
			    post_time=$8, enabled=$9, updated_at=$10
			WHERE id=$1 AND tenant_id=$2`,
			cfgValue.ID, cfgValue.TenantID, cfgValue.Name, cfgValue.Channel, cfgValue.ChatID,
			cfgValue.ThreadID, cfgValue.Timezone, cfgValue.PostTime, boolToInt(cfgValue.Enabled), cfgValue.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		return s.getConfigByKey(cfgValue.TenantID, cfgValue.Key)
	case errors.Is(err, errDailyIChingConfigNotFound):
		if cfgValue.ID == "" {
			cfgValue.ID = uuidString()
		}
		cfgValue.CreatedAt = now
		cfgValue.UpdatedAt = now
		_, err = s.db.Exec(`
			INSERT INTO beta_daily_iching_configs (
				id, tenant_id, config_key, name, channel, chat_id, thread_id,
				timezone, post_time, enabled, created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			cfgValue.ID, cfgValue.TenantID, cfgValue.Key, cfgValue.Name, cfgValue.Channel, cfgValue.ChatID,
			cfgValue.ThreadID, cfgValue.Timezone, cfgValue.PostTime, boolToInt(cfgValue.Enabled),
			cfgValue.CreatedAt, cfgValue.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		return s.getConfigByKey(cfgValue.TenantID, cfgValue.Key)
	default:
		return nil, err
	}
}

func (s *featureStore) getConfigByKey(tenantID, key string) (*DailyIChingConfig, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       post_time, enabled, created_at, updated_at
		FROM beta_daily_iching_configs
		WHERE tenant_id=$1 AND config_key=$2`,
		tenantID, normalizeConfigKey(key),
	)
	return scanDailyIChingConfig(row)
}

func (s *featureStore) getConfigByTarget(tenantID, channel, chatID string, threadID int) (*DailyIChingConfig, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       post_time, enabled, created_at, updated_at
		FROM beta_daily_iching_configs
		WHERE tenant_id=$1 AND channel=$2 AND chat_id=$3 AND thread_id=$4`,
		tenantID, strings.TrimSpace(channel), strings.TrimSpace(chatID), normalizeThreadID(threadID),
	)
	return scanDailyIChingConfig(row)
}

func (s *featureStore) listConfigs(tenantID string) ([]DailyIChingConfig, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       post_time, enabled, created_at, updated_at
		FROM beta_daily_iching_configs
		WHERE tenant_id=$1
		ORDER BY config_key ASC`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []DailyIChingConfig
	for rows.Next() {
		cfg, err := scanDailyIChingConfig(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, *cfg)
	}
	return configs, rows.Err()
}

func (s *featureStore) listEnabledConfigs() ([]DailyIChingConfig, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       post_time, enabled, created_at, updated_at
		FROM beta_daily_iching_configs
		WHERE enabled=1
		ORDER BY tenant_id ASC, config_key ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []DailyIChingConfig
	for rows.Next() {
		cfg, err := scanDailyIChingConfig(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, *cfg)
	}
	return configs, rows.Err()
}

func (s *featureStore) getProgress(tenantID, configID string) (*ProgressState, error) {
	var (
		lastPostedAt sql.NullTime
		progress     ProgressState
	)
	err := s.db.QueryRow(`
		SELECT id, tenant_id, config_id, sequence_index, current_hexagram,
		       last_posted_local_date, last_posted_at, updated_at
		FROM beta_daily_iching_progress
		WHERE tenant_id=$1 AND config_id=$2`,
		tenantID, configID,
	).Scan(
		&progress.ID,
		&progress.TenantID,
		&progress.ConfigID,
		&progress.SequenceIndex,
		&progress.CurrentHexagram,
		&progress.LastPostedLocalDate,
		&lastPostedAt,
		&progress.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errDailyIChingProgressAbsent
		}
		return nil, err
	}
	if lastPostedAt.Valid {
		progress.LastPostedAt = &lastPostedAt.Time
	}
	return &progress, nil
}

func (s *featureStore) getOrCreateProgress(tenantID, configID string) (*ProgressState, error) {
	progress, err := s.getProgress(tenantID, configID)
	if err == nil {
		return progress, nil
	}
	if !errors.Is(err, errDailyIChingProgressAbsent) {
		return nil, err
	}

	now := time.Now().UTC()
	progress = &ProgressState{
		ID:        uuidString(),
		TenantID:  tenantID,
		ConfigID:  configID,
		UpdatedAt: now,
	}
	_, err = s.db.Exec(`
		INSERT INTO beta_daily_iching_progress (
			id, tenant_id, config_id, sequence_index, current_hexagram,
			last_posted_local_date, last_posted_at, updated_at
		)
		VALUES ($1, $2, $3, 0, 0, '', NULL, $4)`,
		progress.ID, progress.TenantID, progress.ConfigID, progress.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return s.getProgress(tenantID, configID)
}

func (s *featureStore) updateProgress(tenantID, configID string, sequenceIndex, hexagram int, localDate string, postedAt time.Time) error {
	_, err := s.db.Exec(`
		UPDATE beta_daily_iching_progress
		SET sequence_index=$3, current_hexagram=$4, last_posted_local_date=$5,
		    last_posted_at=$6, updated_at=$7
		WHERE tenant_id=$1 AND config_id=$2`,
		tenantID, configID, sequenceIndex, hexagram, localDate, postedAt, postedAt,
	)
	return err
}

func (s *featureStore) recordPost(post *LessonPost) error {
	if post.ID == "" {
		post.ID = uuidString()
	}
	if post.CreatedAt.IsZero() {
		post.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO beta_daily_iching_posts (
			id, tenant_id, config_id, local_date, post_kind,
			trigger_kind, sequence_index, hexagram, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		post.ID, post.TenantID, post.ConfigID, post.LocalDate, post.PostKind,
		post.TriggerKind, post.SequenceIndex, post.Hexagram, post.CreatedAt,
	)
	return err
}

func (s *featureStore) hasPost(tenantID, configID, localDate, postKind string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`
		SELECT 1
		FROM beta_daily_iching_posts
		WHERE tenant_id=$1 AND config_id=$2 AND local_date=$3 AND post_kind=$4
		LIMIT 1`,
		tenantID, configID, localDate, postKind,
	).Scan(&exists)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDailyIChingConfig(scanner rowScanner) (*DailyIChingConfig, error) {
	var (
		enabled int
		cfg     DailyIChingConfig
	)
	err := scanner.Scan(
		&cfg.ID,
		&cfg.TenantID,
		&cfg.Key,
		&cfg.Name,
		&cfg.Channel,
		&cfg.ChatID,
		&cfg.ThreadID,
		&cfg.Timezone,
		&cfg.PostTime,
		&enabled,
		&cfg.CreatedAt,
		&cfg.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errDailyIChingConfigNotFound
		}
		return nil, err
	}
	cfg.Enabled = intToBool(enabled)
	cfg.ThreadID = normalizeThreadID(cfg.ThreadID)
	return &cfg, nil
}

func uuidString() string {
	return uuid.NewString()
}
