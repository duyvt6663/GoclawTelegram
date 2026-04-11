package russianroulette

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	errRouletteConfigNotFound = errors.New("russian roulette config not found")
	errRouletteRoundNotFound  = errors.New("russian roulette round not found")
)

type RouletteConfig struct {
	ID                      string    `json:"id"`
	TenantID                string    `json:"tenant_id,omitempty"`
	Key                     string    `json:"key"`
	Name                    string    `json:"name"`
	Channel                 string    `json:"channel"`
	ChatID                  string    `json:"chat_id"`
	ThreadID                int       `json:"thread_id"`
	ChamberSize             int       `json:"chamber_size"`
	TurnCooldownSeconds     int       `json:"turn_cooldown_seconds"`
	PenaltyMode             string    `json:"penalty_mode"`
	PenaltyDurationSeconds  int       `json:"penalty_duration_seconds"`
	PenaltyTag              string    `json:"penalty_tag,omitempty"`
	SafeStickerFileID       string    `json:"safe_sticker_file_id,omitempty"`
	EliminatedStickerFileID string    `json:"eliminated_sticker_file_id,omitempty"`
	WinnerStickerFileID     string    `json:"winner_sticker_file_id,omitempty"`
	Enabled                 bool      `json:"enabled"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

func (cfg RouletteConfig) withDefaults() RouletteConfig {
	cfg.Key = normalizeConfigKey(cfg.Key)
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.Channel = strings.TrimSpace(cfg.Channel)
	cfg.ChatID = strings.TrimSpace(cfg.ChatID)
	cfg.ThreadID = normalizeThreadID(cfg.ThreadID)
	if cfg.Key == "" && cfg.Channel != "" && cfg.ChatID != "" {
		cfg.Key = defaultConfigKey(cfg.Channel, cfg.ChatID, cfg.ThreadID)
	}
	if cfg.Name == "" {
		cfg.Name = defaultConfigDisplayName
	}
	if cfg.ChamberSize <= 0 {
		cfg.ChamberSize = defaultChamberSize
	}
	if cfg.TurnCooldownSeconds <= 0 {
		cfg.TurnCooldownSeconds = defaultTurnCooldownSeconds
	}
	cfg.PenaltyMode = strings.TrimSpace(strings.ToLower(cfg.PenaltyMode))
	if cfg.PenaltyMode == "" {
		cfg.PenaltyMode = penaltyModeNone
	}
	if cfg.PenaltyMode == penaltyModeMute && cfg.PenaltyDurationSeconds <= 0 {
		cfg.PenaltyDurationSeconds = defaultPenaltyDurationSecs
	}
	if cfg.PenaltyMode == penaltyModeTag && strings.TrimSpace(cfg.PenaltyTag) == "" {
		cfg.PenaltyTag = defaultPenaltyTag
	}
	cfg.PenaltyTag = strings.TrimSpace(cfg.PenaltyTag)
	cfg.SafeStickerFileID = strings.TrimSpace(cfg.SafeStickerFileID)
	cfg.EliminatedStickerFileID = strings.TrimSpace(cfg.EliminatedStickerFileID)
	cfg.WinnerStickerFileID = strings.TrimSpace(cfg.WinnerStickerFileID)
	return cfg
}

func (cfg RouletteConfig) validate() error {
	if cfg.Key == "" {
		return fmt.Errorf("key is required")
	}
	if cfg.Channel == "" {
		return fmt.Errorf("channel is required")
	}
	if cfg.ChatID == "" {
		return fmt.Errorf("chat_id is required")
	}
	if cfg.ChamberSize < minChamberSize || cfg.ChamberSize > maxChamberSize {
		return fmt.Errorf("chamber_size must be between %d and %d", minChamberSize, maxChamberSize)
	}
	if cfg.TurnCooldownSeconds < 0 || cfg.TurnCooldownSeconds > maxTurnCooldownSeconds {
		return fmt.Errorf("turn_cooldown_seconds must be between 0 and %d", maxTurnCooldownSeconds)
	}
	switch cfg.PenaltyMode {
	case penaltyModeNone:
		return nil
	case penaltyModeMute:
		if cfg.PenaltyDurationSeconds < 30 || cfg.PenaltyDurationSeconds > maxPenaltyDurationSecs {
			return fmt.Errorf("penalty_duration_seconds must be between 30 and %d when penalty_mode is mute", maxPenaltyDurationSecs)
		}
		return nil
	case penaltyModeTag:
		if cfg.PenaltyDurationSeconds < 0 || cfg.PenaltyDurationSeconds > maxPenaltyDurationSecs {
			return fmt.Errorf("penalty_duration_seconds must be between 0 and %d when penalty_mode is tag", maxPenaltyDurationSecs)
		}
		if strings.TrimSpace(cfg.PenaltyTag) == "" {
			return fmt.Errorf("penalty_tag is required when penalty_mode is tag")
		}
		return nil
	default:
		return fmt.Errorf("penalty_mode must be one of: %s, %s, %s", penaltyModeNone, penaltyModeMute, penaltyModeTag)
	}
}

type RouletteRound struct {
	ID                 string     `json:"id"`
	TenantID           string     `json:"tenant_id,omitempty"`
	ConfigID           string     `json:"config_id"`
	Status             string     `json:"status"`
	ChamberSize        int        `json:"chamber_size"`
	BulletPosition     int        `json:"bullet_position,omitempty"`
	PullCount          int        `json:"pull_count"`
	CurrentPlayerOrder int        `json:"current_player_order,omitempty"`
	StartedByID        string     `json:"started_by_id,omitempty"`
	StartedByLabel     string     `json:"started_by_label,omitempty"`
	WinnerUserID       string     `json:"winner_user_id,omitempty"`
	WinnerLabel        string     `json:"winner_label,omitempty"`
	CooldownUntil      *time.Time `json:"cooldown_until,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	UpdatedAt          time.Time  `json:"updated_at"`
	EndedAt            *time.Time `json:"ended_at,omitempty"`
}

type RoulettePlayer struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenant_id,omitempty"`
	RoundID      string     `json:"round_id"`
	UserID       string     `json:"user_id"`
	UserLabel    string     `json:"user_label"`
	SeatOrder    int        `json:"seat_order"`
	SafePulls    int        `json:"safe_pulls"`
	IsAlive      bool       `json:"is_alive"`
	EliminatedAt *time.Time `json:"eliminated_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type RouletteStat struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenant_id,omitempty"`
	ConfigID    string     `json:"config_id"`
	UserID      string     `json:"user_id"`
	UserLabel   string     `json:"user_label"`
	GamesPlayed int        `json:"games_played"`
	Wins        int        `json:"wins"`
	Losses      int        `json:"losses"`
	SafePulls   int        `json:"safe_pulls"`
	LastWinAt   *time.Time `json:"last_win_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
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
		CREATE TABLE IF NOT EXISTS beta_russian_roulette_configs (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_key TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			thread_id INTEGER NOT NULL DEFAULT 0,
			chamber_size INTEGER NOT NULL DEFAULT 6,
			turn_cooldown_seconds INTEGER NOT NULL DEFAULT 8,
			penalty_mode TEXT NOT NULL DEFAULT 'none',
			penalty_duration_seconds INTEGER NOT NULL DEFAULT 0,
			penalty_tag TEXT NOT NULL DEFAULT '',
			safe_sticker_file_id TEXT NOT NULL DEFAULT '',
			eliminated_sticker_file_id TEXT NOT NULL DEFAULT '',
			winner_sticker_file_id TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, config_key),
			UNIQUE (tenant_id, channel, chat_id, thread_id)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_russian_roulette_rounds (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'lobby',
			chamber_size INTEGER NOT NULL DEFAULT 6,
			bullet_position INTEGER NOT NULL DEFAULT 0,
			pull_count INTEGER NOT NULL DEFAULT 0,
			current_player_order INTEGER NOT NULL DEFAULT 0,
			started_by_id TEXT NOT NULL DEFAULT '',
			started_by_label TEXT NOT NULL DEFAULT '',
			winner_user_id TEXT NOT NULL DEFAULT '',
			winner_label TEXT NOT NULL DEFAULT '',
			cooldown_until TIMESTAMP NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			started_at TIMESTAMP NULL,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			ended_at TIMESTAMP NULL
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_russian_roulette_players (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			round_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			user_label TEXT NOT NULL DEFAULT '',
			seat_order INTEGER NOT NULL DEFAULT 0,
			safe_pulls INTEGER NOT NULL DEFAULT 0,
			is_alive INTEGER NOT NULL DEFAULT 1,
			eliminated_at TIMESTAMP NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, round_id, user_id)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_russian_roulette_stats (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			user_label TEXT NOT NULL DEFAULT '',
			games_played INTEGER NOT NULL DEFAULT 0,
			wins INTEGER NOT NULL DEFAULT 0,
			losses INTEGER NOT NULL DEFAULT 0,
			safe_pulls INTEGER NOT NULL DEFAULT 0,
			last_win_at TIMESTAMP NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, config_id, user_id)
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_russian_roulette_configs_target ON beta_russian_roulette_configs(tenant_id, channel, chat_id, thread_id)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_russian_roulette_rounds_status ON beta_russian_roulette_rounds(tenant_id, config_id, status, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_russian_roulette_players_round ON beta_russian_roulette_players(tenant_id, round_id, seat_order)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_russian_roulette_stats_rank ON beta_russian_roulette_stats(tenant_id, config_id, wins, safe_pulls)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) withTx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *featureStore) upsertConfig(cfg *RouletteConfig) (*RouletteConfig, error) {
	value := cfg.withDefaults()
	if err := value.validate(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()

	existing, err := s.getConfigByKey(value.TenantID, value.Key)
	switch {
	case err == nil:
		value.ID = existing.ID
		value.CreatedAt = existing.CreatedAt
		value.UpdatedAt = now
		_, err = s.db.Exec(`
			UPDATE beta_russian_roulette_configs
			SET name=$3, channel=$4, chat_id=$5, thread_id=$6, chamber_size=$7,
			    turn_cooldown_seconds=$8, penalty_mode=$9, penalty_duration_seconds=$10,
			    penalty_tag=$11, safe_sticker_file_id=$12, eliminated_sticker_file_id=$13,
			    winner_sticker_file_id=$14, enabled=$15, updated_at=$16
			WHERE id=$1 AND tenant_id=$2`,
			value.ID, value.TenantID, value.Name, value.Channel, value.ChatID, value.ThreadID,
			value.ChamberSize, value.TurnCooldownSeconds, value.PenaltyMode,
			value.PenaltyDurationSeconds, value.PenaltyTag, value.SafeStickerFileID,
			value.EliminatedStickerFileID, value.WinnerStickerFileID, boolToInt(value.Enabled),
			value.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		return s.getConfigByKey(value.TenantID, value.Key)
	case errors.Is(err, errRouletteConfigNotFound):
		value.ID = uuid.NewString()
		value.CreatedAt = now
		value.UpdatedAt = now
		_, err = s.db.Exec(`
			INSERT INTO beta_russian_roulette_configs (
				id, tenant_id, config_key, name, channel, chat_id, thread_id, chamber_size,
				turn_cooldown_seconds, penalty_mode, penalty_duration_seconds, penalty_tag,
				safe_sticker_file_id, eliminated_sticker_file_id, winner_sticker_file_id,
				enabled, created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
			value.ID, value.TenantID, value.Key, value.Name, value.Channel, value.ChatID, value.ThreadID,
			value.ChamberSize, value.TurnCooldownSeconds, value.PenaltyMode, value.PenaltyDurationSeconds,
			value.PenaltyTag, value.SafeStickerFileID, value.EliminatedStickerFileID,
			value.WinnerStickerFileID, boolToInt(value.Enabled), value.CreatedAt, value.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		return s.getConfigByKey(value.TenantID, value.Key)
	default:
		return nil, err
	}
}

func (s *featureStore) getConfigByKey(tenantID, key string) (*RouletteConfig, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, chamber_size,
		       turn_cooldown_seconds, penalty_mode, penalty_duration_seconds, penalty_tag,
		       safe_sticker_file_id, eliminated_sticker_file_id, winner_sticker_file_id,
		       enabled, created_at, updated_at
		FROM beta_russian_roulette_configs
		WHERE tenant_id=$1 AND config_key=$2`,
		tenantID, normalizeConfigKey(key),
	)
	cfg, err := scanRouletteConfig(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errRouletteConfigNotFound
		}
		return nil, err
	}
	return cfg, nil
}

func (s *featureStore) getConfigByTarget(tenantID, channel, chatID string, threadID int) (*RouletteConfig, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, chamber_size,
		       turn_cooldown_seconds, penalty_mode, penalty_duration_seconds, penalty_tag,
		       safe_sticker_file_id, eliminated_sticker_file_id, winner_sticker_file_id,
		       enabled, created_at, updated_at
		FROM beta_russian_roulette_configs
		WHERE tenant_id=$1 AND channel=$2 AND chat_id=$3 AND thread_id=$4`,
		tenantID, strings.TrimSpace(channel), strings.TrimSpace(chatID), normalizeThreadID(threadID),
	)
	cfg, err := scanRouletteConfig(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errRouletteConfigNotFound
		}
		return nil, err
	}
	return cfg, nil
}

func (s *featureStore) listConfigs(tenantID string) ([]RouletteConfig, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, chamber_size,
		       turn_cooldown_seconds, penalty_mode, penalty_duration_seconds, penalty_tag,
		       safe_sticker_file_id, eliminated_sticker_file_id, winner_sticker_file_id,
		       enabled, created_at, updated_at
		FROM beta_russian_roulette_configs
		WHERE tenant_id=$1
		ORDER BY updated_at DESC, name ASC`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	configs := make([]RouletteConfig, 0)
	for rows.Next() {
		cfg, err := scanRouletteConfig(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, *cfg)
	}
	return configs, rows.Err()
}

func (s *featureStore) getActiveRoundByConfig(tenantID, configID string) (*RouletteRound, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_id, status, chamber_size, bullet_position, pull_count,
		       current_player_order, started_by_id, started_by_label, winner_user_id,
		       winner_label, cooldown_until, created_at, started_at, updated_at, ended_at
		FROM beta_russian_roulette_rounds
		WHERE tenant_id=$1 AND config_id=$2 AND status IN ($3, $4)
		ORDER BY created_at DESC
		LIMIT 1`,
		tenantID, configID, roundStatusLobby, roundStatusActive,
	)
	round, err := scanRouletteRound(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errRouletteRoundNotFound
		}
		return nil, err
	}
	return round, nil
}

func (s *featureStore) listPlayers(tenantID, roundID string) ([]RoulettePlayer, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, round_id, user_id, user_label, seat_order, safe_pulls,
		       is_alive, eliminated_at, created_at, updated_at
		FROM beta_russian_roulette_players
		WHERE tenant_id=$1 AND round_id=$2
		ORDER BY seat_order ASC, created_at ASC`,
		tenantID, roundID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	players := make([]RoulettePlayer, 0)
	for rows.Next() {
		player, err := scanRoulettePlayer(rows)
		if err != nil {
			return nil, err
		}
		players = append(players, *player)
	}
	return players, rows.Err()
}

func (s *featureStore) listStats(tenantID, configID string, limit int) ([]RouletteStat, error) {
	if limit <= 0 {
		limit = leaderboardDefaultLimit
	}
	if limit > leaderboardMaxLimit {
		limit = leaderboardMaxLimit
	}
	rows, err := s.db.Query(`
		SELECT id, tenant_id, config_id, user_id, user_label, games_played, wins, losses,
		       safe_pulls, last_win_at, created_at, updated_at
		FROM beta_russian_roulette_stats
		WHERE tenant_id=$1 AND config_id=$2
		ORDER BY wins DESC, safe_pulls DESC, losses ASC, updated_at DESC
		LIMIT $3`,
		tenantID, configID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make([]RouletteStat, 0)
	for rows.Next() {
		stat, err := scanRouletteStat(rows)
		if err != nil {
			return nil, err
		}
		stats = append(stats, *stat)
	}
	return stats, rows.Err()
}

func (s *featureStore) createRoundTx(tx *sql.Tx, round *RouletteRound) error {
	_, err := tx.Exec(`
		INSERT INTO beta_russian_roulette_rounds (
			id, tenant_id, config_id, status, chamber_size, bullet_position, pull_count,
			current_player_order, started_by_id, started_by_label, winner_user_id,
			winner_label, cooldown_until, created_at, started_at, updated_at, ended_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		round.ID, round.TenantID, round.ConfigID, round.Status, round.ChamberSize,
		round.BulletPosition, round.PullCount, round.CurrentPlayerOrder, round.StartedByID,
		round.StartedByLabel, round.WinnerUserID, round.WinnerLabel, dbTime(round.CooldownUntil),
		round.CreatedAt, dbTime(round.StartedAt), round.UpdatedAt, dbTime(round.EndedAt),
	)
	return err
}

func (s *featureStore) updateRoundTx(tx *sql.Tx, round *RouletteRound) error {
	_, err := tx.Exec(`
		UPDATE beta_russian_roulette_rounds
		SET status=$3, chamber_size=$4, bullet_position=$5, pull_count=$6,
		    current_player_order=$7, started_by_id=$8, started_by_label=$9,
		    winner_user_id=$10, winner_label=$11, cooldown_until=$12,
		    started_at=$13, updated_at=$14, ended_at=$15
		WHERE id=$1 AND tenant_id=$2`,
		round.ID, round.TenantID, round.Status, round.ChamberSize, round.BulletPosition,
		round.PullCount, round.CurrentPlayerOrder, round.StartedByID, round.StartedByLabel,
		round.WinnerUserID, round.WinnerLabel, dbTime(round.CooldownUntil),
		dbTime(round.StartedAt), round.UpdatedAt, dbTime(round.EndedAt),
	)
	return err
}

func (s *featureStore) insertPlayerTx(tx *sql.Tx, player *RoulettePlayer) error {
	_, err := tx.Exec(`
		INSERT INTO beta_russian_roulette_players (
			id, tenant_id, round_id, user_id, user_label, seat_order, safe_pulls,
			is_alive, eliminated_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		player.ID, player.TenantID, player.RoundID, player.UserID, player.UserLabel, player.SeatOrder,
		player.SafePulls, boolToInt(player.IsAlive), dbTime(player.EliminatedAt), player.CreatedAt,
		player.UpdatedAt,
	)
	return err
}

func (s *featureStore) updatePlayerTx(tx *sql.Tx, player *RoulettePlayer) error {
	_, err := tx.Exec(`
		UPDATE beta_russian_roulette_players
		SET user_label=$4, seat_order=$5, safe_pulls=$6, is_alive=$7,
		    eliminated_at=$8, updated_at=$9
		WHERE id=$1 AND tenant_id=$2 AND round_id=$3`,
		player.ID, player.TenantID, player.RoundID, player.UserLabel, player.SeatOrder,
		player.SafePulls, boolToInt(player.IsAlive), dbTime(player.EliminatedAt), player.UpdatedAt,
	)
	return err
}

func (s *featureStore) deletePlayerTx(tx *sql.Tx, tenantID, roundID, userID string) error {
	_, err := tx.Exec(`
		DELETE FROM beta_russian_roulette_players
		WHERE tenant_id=$1 AND round_id=$2 AND user_id=$3`,
		tenantID, roundID, userID,
	)
	return err
}

func (s *featureStore) upsertStatTx(tx *sql.Tx, tenantID, configID string, player RoulettePlayer, winnerID string, now time.Time) error {
	winDelta := 0
	lossDelta := 1
	var lastWin any
	if player.UserID == winnerID {
		winDelta = 1
		lossDelta = 0
		lastWin = now
	}
	_, err := tx.Exec(`
		INSERT INTO beta_russian_roulette_stats (
			id, tenant_id, config_id, user_id, user_label, games_played, wins, losses,
			safe_pulls, last_win_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, 1, $6, $7, $8, $9, $10, $11)
		ON CONFLICT(tenant_id, config_id, user_id) DO UPDATE SET
			user_label=excluded.user_label,
			games_played=beta_russian_roulette_stats.games_played + 1,
			wins=beta_russian_roulette_stats.wins + excluded.wins,
			losses=beta_russian_roulette_stats.losses + excluded.losses,
			safe_pulls=beta_russian_roulette_stats.safe_pulls + excluded.safe_pulls,
			last_win_at=COALESCE(excluded.last_win_at, beta_russian_roulette_stats.last_win_at),
			updated_at=excluded.updated_at`,
		uuid.NewString(), tenantID, configID, player.UserID, player.UserLabel,
		winDelta, lossDelta, player.SafePulls, lastWin, now, now,
	)
	return err
}

func scanRouletteConfig(row scanner) (*RouletteConfig, error) {
	var cfg RouletteConfig
	var enabled int
	err := row.Scan(
		&cfg.ID, &cfg.TenantID, &cfg.Key, &cfg.Name, &cfg.Channel, &cfg.ChatID, &cfg.ThreadID,
		&cfg.ChamberSize, &cfg.TurnCooldownSeconds, &cfg.PenaltyMode, &cfg.PenaltyDurationSeconds,
		&cfg.PenaltyTag, &cfg.SafeStickerFileID, &cfg.EliminatedStickerFileID,
		&cfg.WinnerStickerFileID, &enabled, &cfg.CreatedAt, &cfg.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	cfg.Enabled = intToBool(enabled)
	cfg.ThreadID = normalizeThreadID(cfg.ThreadID)
	return &cfg, nil
}

func scanRouletteRound(row scanner) (*RouletteRound, error) {
	var round RouletteRound
	var cooldown sql.NullTime
	var startedAt sql.NullTime
	var endedAt sql.NullTime
	err := row.Scan(
		&round.ID, &round.TenantID, &round.ConfigID, &round.Status, &round.ChamberSize,
		&round.BulletPosition, &round.PullCount, &round.CurrentPlayerOrder, &round.StartedByID,
		&round.StartedByLabel, &round.WinnerUserID, &round.WinnerLabel, &cooldown,
		&round.CreatedAt, &startedAt, &round.UpdatedAt, &endedAt,
	)
	if err != nil {
		return nil, err
	}
	round.CooldownUntil = nullTimePtr(cooldown)
	round.StartedAt = nullTimePtr(startedAt)
	round.EndedAt = nullTimePtr(endedAt)
	return &round, nil
}

func scanRoulettePlayer(row scanner) (*RoulettePlayer, error) {
	var player RoulettePlayer
	var isAlive int
	var eliminatedAt sql.NullTime
	err := row.Scan(
		&player.ID, &player.TenantID, &player.RoundID, &player.UserID, &player.UserLabel,
		&player.SeatOrder, &player.SafePulls, &isAlive, &eliminatedAt, &player.CreatedAt, &player.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	player.IsAlive = intToBool(isAlive)
	player.EliminatedAt = nullTimePtr(eliminatedAt)
	return &player, nil
}

func scanRouletteStat(row scanner) (*RouletteStat, error) {
	var stat RouletteStat
	var lastWinAt sql.NullTime
	err := row.Scan(
		&stat.ID, &stat.TenantID, &stat.ConfigID, &stat.UserID, &stat.UserLabel,
		&stat.GamesPlayed, &stat.Wins, &stat.Losses, &stat.SafePulls, &lastWinAt,
		&stat.CreatedAt, &stat.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	stat.LastWinAt = nullTimePtr(lastWinAt)
	return &stat, nil
}
