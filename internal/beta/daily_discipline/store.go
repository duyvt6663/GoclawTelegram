package dailydiscipline

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	errSurveyConfigNotFound = errors.New("daily discipline config not found")
	errSurveyPostNotFound   = errors.New("daily discipline post not found")
)

// SurveyConfig stores one channel-specific daily discipline setup.
type SurveyConfig struct {
	ID                 string    `json:"id"`
	TenantID           string    `json:"tenant_id,omitempty"`
	Key                string    `json:"key"`
	Name               string    `json:"name"`
	Channel            string    `json:"channel"`
	ChatID             string    `json:"chat_id"`
	ThreadID           int       `json:"thread_id"`
	Timezone           string    `json:"timezone"`
	SurveyWindowStart  string    `json:"survey_window_start"`
	SurveyWindowEnd    string    `json:"survey_window_end"`
	SummaryTime        string    `json:"summary_time"`
	TargetWakeTime     string    `json:"target_wake_time"`
	WakeQuestion       string    `json:"wake_question,omitempty"`
	DisciplineQuestion string    `json:"discipline_question,omitempty"`
	ActivityQuestion   string    `json:"activity_question,omitempty"`
	NamedResults       bool      `json:"named_results"`
	StreaksEnabled     bool      `json:"streaks_enabled"`
	DMDetailsEnabled   bool      `json:"dm_details_enabled"`
	Enabled            bool      `json:"enabled"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func (cfg SurveyConfig) withDefaults() SurveyConfig {
	cfg.Key = normalizeConfigKey(cfg.Key)
	if cfg.Name == "" {
		cfg.Name = cfg.Key
	}
	cfg.Channel = strings.TrimSpace(cfg.Channel)
	cfg.ChatID = strings.TrimSpace(cfg.ChatID)
	if cfg.Timezone == "" {
		cfg.Timezone = "UTC"
	}
	if cfg.SurveyWindowStart == "" {
		cfg.SurveyWindowStart = "05:00"
	}
	if cfg.SurveyWindowEnd == "" {
		cfg.SurveyWindowEnd = "07:00"
	}
	if cfg.SummaryTime == "" {
		cfg.SummaryTime = "12:00"
	}
	if cfg.TargetWakeTime == "" {
		cfg.TargetWakeTime = "05:00"
	}
	if cfg.WakeQuestion == "" {
		cfg.WakeQuestion = fmt.Sprintf("Woke up before %s?", cfg.TargetWakeTime)
	}
	if cfg.DisciplineQuestion == "" {
		cfg.DisciplineQuestion = "Completed your discipline today?"
	}
	if cfg.ActivityQuestion == "" {
		cfg.ActivityQuestion = "Did physical activity today?"
	}
	return cfg
}

func (cfg SurveyConfig) localNow(now time.Time) time.Time {
	return now.In(loadLocation(cfg.Timezone))
}

func (cfg SurveyConfig) localDate(now time.Time) string {
	return cfg.localNow(now).Format("2006-01-02")
}

type SurveyPost struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id,omitempty"`
	ConfigID  string    `json:"config_id"`
	LocalDate string    `json:"local_date"`
	PostKind  string    `json:"post_kind"`
	PollID    string    `json:"poll_id,omitempty"`
	MessageID int       `json:"message_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type DailyResponse struct {
	ID               string    `json:"id"`
	TenantID         string    `json:"tenant_id,omitempty"`
	ConfigID         string    `json:"config_id"`
	LocalDate        string    `json:"local_date"`
	UserID           string    `json:"user_id"`
	UserLabel        string    `json:"user_label"`
	WakeStatus       string    `json:"wake_status,omitempty"`
	DisciplineStatus string    `json:"discipline_status,omitempty"`
	ActivityStatus   string    `json:"activity_status,omitempty"`
	Note             string    `json:"note,omitempty"`
	Source           string    `json:"source,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type responsePatch struct {
	UserID           string
	UserLabel        string
	WakeStatus       *string
	DisciplineStatus *string
	ActivityStatus   *string
	Note             *string
	Source           string
}

type featureStore struct {
	db *sql.DB
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_daily_discipline_configs (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_key TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			thread_id INTEGER NOT NULL DEFAULT 0,
			timezone TEXT NOT NULL DEFAULT 'UTC',
			survey_window_start TEXT NOT NULL DEFAULT '05:00',
			survey_window_end TEXT NOT NULL DEFAULT '07:00',
			summary_time TEXT NOT NULL DEFAULT '12:00',
			target_wake_time TEXT NOT NULL DEFAULT '05:00',
			wake_question TEXT NOT NULL DEFAULT '',
			discipline_question TEXT NOT NULL DEFAULT '',
			activity_question TEXT NOT NULL DEFAULT '',
			named_results INTEGER NOT NULL DEFAULT 0,
			streaks_enabled INTEGER NOT NULL DEFAULT 0,
			dm_details_enabled INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, config_key),
			UNIQUE (tenant_id, channel, chat_id, thread_id)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_daily_discipline_posts (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_id TEXT NOT NULL,
			local_date TEXT NOT NULL,
			post_kind TEXT NOT NULL,
			poll_id TEXT NOT NULL DEFAULT '',
			message_id INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, config_id, local_date, post_kind)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_daily_discipline_responses (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_id TEXT NOT NULL,
			local_date TEXT NOT NULL,
			user_id TEXT NOT NULL,
			user_label TEXT NOT NULL DEFAULT '',
			wake_status TEXT NOT NULL DEFAULT '',
			discipline_status TEXT NOT NULL DEFAULT '',
			activity_status TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, config_id, local_date, user_id)
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_daily_discipline_configs_enabled ON beta_daily_discipline_configs(tenant_id, enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_daily_discipline_posts_poll_id ON beta_daily_discipline_posts(tenant_id, poll_id)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_daily_discipline_posts_date ON beta_daily_discipline_posts(tenant_id, config_id, local_date)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_daily_discipline_responses_date ON beta_daily_discipline_responses(tenant_id, config_id, local_date)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_daily_discipline_responses_user ON beta_daily_discipline_responses(tenant_id, config_id, user_id, local_date)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) upsertConfig(cfg *SurveyConfig) (*SurveyConfig, error) {
	cfgValue := cfg.withDefaults()
	now := time.Now().UTC()

	existing, err := s.getConfigByKey(cfgValue.TenantID, cfgValue.Key)
	switch {
	case err == nil:
		cfgValue.ID = existing.ID
		cfgValue.CreatedAt = existing.CreatedAt
		cfgValue.UpdatedAt = now
		_, err = s.db.Exec(`
			UPDATE beta_daily_discipline_configs
			SET name=$3, channel=$4, chat_id=$5, thread_id=$6, timezone=$7,
			    survey_window_start=$8, survey_window_end=$9, summary_time=$10,
			    target_wake_time=$11, wake_question=$12, discipline_question=$13,
			    activity_question=$14, named_results=$15, streaks_enabled=$16,
			    dm_details_enabled=$17, enabled=$18, updated_at=$19
			WHERE id=$1 AND tenant_id=$2`,
			cfgValue.ID, cfgValue.TenantID, cfgValue.Name, cfgValue.Channel, cfgValue.ChatID, cfgValue.ThreadID,
			cfgValue.Timezone, cfgValue.SurveyWindowStart, cfgValue.SurveyWindowEnd, cfgValue.SummaryTime,
			cfgValue.TargetWakeTime, cfgValue.WakeQuestion, cfgValue.DisciplineQuestion, cfgValue.ActivityQuestion,
			boolToInt(cfgValue.NamedResults), boolToInt(cfgValue.StreaksEnabled), boolToInt(cfgValue.DMDetailsEnabled),
			boolToInt(cfgValue.Enabled), cfgValue.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		return s.getConfigByKey(cfgValue.TenantID, cfgValue.Key)
	case errors.Is(err, errSurveyConfigNotFound):
		if cfgValue.ID == "" {
			cfgValue.ID = uuidString()
		}
		cfgValue.CreatedAt = now
		cfgValue.UpdatedAt = now
		_, err = s.db.Exec(`
			INSERT INTO beta_daily_discipline_configs (
				id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
				survey_window_start, survey_window_end, summary_time, target_wake_time,
				wake_question, discipline_question, activity_question, named_results,
				streaks_enabled, dm_details_enabled, enabled, created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`,
			cfgValue.ID, cfgValue.TenantID, cfgValue.Key, cfgValue.Name, cfgValue.Channel, cfgValue.ChatID, cfgValue.ThreadID,
			cfgValue.Timezone, cfgValue.SurveyWindowStart, cfgValue.SurveyWindowEnd, cfgValue.SummaryTime,
			cfgValue.TargetWakeTime, cfgValue.WakeQuestion, cfgValue.DisciplineQuestion, cfgValue.ActivityQuestion,
			boolToInt(cfgValue.NamedResults), boolToInt(cfgValue.StreaksEnabled), boolToInt(cfgValue.DMDetailsEnabled),
			boolToInt(cfgValue.Enabled), cfgValue.CreatedAt, cfgValue.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		return s.getConfigByKey(cfgValue.TenantID, cfgValue.Key)
	default:
		return nil, err
	}
}

func (s *featureStore) getConfigByKey(tenantID, key string) (*SurveyConfig, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       survey_window_start, survey_window_end, summary_time, target_wake_time,
		       wake_question, discipline_question, activity_question, named_results,
		       streaks_enabled, dm_details_enabled, enabled, created_at, updated_at
		FROM beta_daily_discipline_configs
		WHERE tenant_id=$1 AND config_key=$2`,
		tenantID, normalizeConfigKey(key),
	)
	return scanSurveyConfig(row)
}

func (s *featureStore) getConfigByTarget(tenantID, channel, chatID string, threadID int) (*SurveyConfig, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       survey_window_start, survey_window_end, summary_time, target_wake_time,
		       wake_question, discipline_question, activity_question, named_results,
		       streaks_enabled, dm_details_enabled, enabled, created_at, updated_at
		FROM beta_daily_discipline_configs
		WHERE tenant_id=$1 AND channel=$2 AND chat_id=$3 AND thread_id=$4`,
		tenantID, strings.TrimSpace(channel), strings.TrimSpace(chatID), threadID,
	)
	return scanSurveyConfig(row)
}

func (s *featureStore) listConfigs(tenantID string) ([]SurveyConfig, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       survey_window_start, survey_window_end, summary_time, target_wake_time,
		       wake_question, discipline_question, activity_question, named_results,
		       streaks_enabled, dm_details_enabled, enabled, created_at, updated_at
		FROM beta_daily_discipline_configs
		WHERE tenant_id=$1
		ORDER BY config_key ASC`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []SurveyConfig
	for rows.Next() {
		cfg, err := scanSurveyConfig(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, *cfg)
	}
	return configs, rows.Err()
}

func (s *featureStore) listEnabledConfigs() ([]SurveyConfig, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       survey_window_start, survey_window_end, summary_time, target_wake_time,
		       wake_question, discipline_question, activity_question, named_results,
		       streaks_enabled, dm_details_enabled, enabled, created_at, updated_at
		FROM beta_daily_discipline_configs
		WHERE enabled=1
		ORDER BY tenant_id ASC, config_key ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []SurveyConfig
	for rows.Next() {
		cfg, err := scanSurveyConfig(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, *cfg)
	}
	return configs, rows.Err()
}

func (s *featureStore) hasPost(tenantID, configID, localDate, postKind string) (bool, error) {
	row := s.db.QueryRow(`
		SELECT 1
		FROM beta_daily_discipline_posts
		WHERE tenant_id=$1 AND config_id=$2 AND local_date=$3 AND post_kind=$4`,
		tenantID, configID, localDate, postKind,
	)
	var one int
	if err := row.Scan(&one); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *featureStore) recordPost(post *SurveyPost) error {
	if post.ID == "" {
		post.ID = uuidString()
	}
	if post.CreatedAt.IsZero() {
		post.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO beta_daily_discipline_posts (id, tenant_id, config_id, local_date, post_kind, poll_id, message_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		post.ID, post.TenantID, post.ConfigID, post.LocalDate, post.PostKind, post.PollID, post.MessageID, post.CreatedAt,
	)
	return err
}

func (s *featureStore) getPostByPollID(tenantID, pollID string) (*SurveyPost, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_id, local_date, post_kind, poll_id, message_id, created_at
		FROM beta_daily_discipline_posts
		WHERE tenant_id=$1 AND poll_id=$2`,
		tenantID, strings.TrimSpace(pollID),
	)
	return scanSurveyPost(row)
}

func (s *featureStore) listPostsForDate(tenantID, configID, localDate string) ([]SurveyPost, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, config_id, local_date, post_kind, poll_id, message_id, created_at
		FROM beta_daily_discipline_posts
		WHERE tenant_id=$1 AND config_id=$2 AND local_date=$3
		ORDER BY created_at ASC`,
		tenantID, configID, localDate,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var posts []SurveyPost
	for rows.Next() {
		post, err := scanSurveyPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, *post)
	}
	return posts, rows.Err()
}

func (s *featureStore) upsertResponse(tenantID, configID, localDate string, patch responsePatch) (*DailyResponse, error) {
	existing, err := s.getResponse(tenantID, configID, localDate, patch.UserID)
	switch {
	case err == nil:
		if patch.UserLabel != "" {
			existing.UserLabel = patch.UserLabel
		}
		if patch.WakeStatus != nil {
			existing.WakeStatus = *patch.WakeStatus
		}
		if patch.DisciplineStatus != nil {
			existing.DisciplineStatus = *patch.DisciplineStatus
		}
		if patch.ActivityStatus != nil {
			existing.ActivityStatus = *patch.ActivityStatus
		}
		if patch.Note != nil {
			existing.Note = strings.TrimSpace(*patch.Note)
		}
		if patch.Source != "" {
			existing.Source = patch.Source
		}
		existing.UpdatedAt = time.Now().UTC()
		_, err = s.db.Exec(`
			UPDATE beta_daily_discipline_responses
			SET user_label=$5, wake_status=$6, discipline_status=$7, activity_status=$8,
			    note=$9, source=$10, updated_at=$11
			WHERE tenant_id=$1 AND config_id=$2 AND local_date=$3 AND user_id=$4`,
			tenantID, configID, localDate, patch.UserID, existing.UserLabel, existing.WakeStatus,
			existing.DisciplineStatus, existing.ActivityStatus, existing.Note, existing.Source, existing.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		return s.getResponse(tenantID, configID, localDate, patch.UserID)
	case errors.Is(err, sql.ErrNoRows):
		response := &DailyResponse{
			ID:               uuidString(),
			TenantID:         tenantID,
			ConfigID:         configID,
			LocalDate:        localDate,
			UserID:           patch.UserID,
			UserLabel:        patch.UserLabel,
			Source:           patch.Source,
			CreatedAt:        time.Now().UTC(),
			UpdatedAt:        time.Now().UTC(),
			WakeStatus:       "",
			DisciplineStatus: "",
			ActivityStatus:   "",
		}
		if patch.WakeStatus != nil {
			response.WakeStatus = *patch.WakeStatus
		}
		if patch.DisciplineStatus != nil {
			response.DisciplineStatus = *patch.DisciplineStatus
		}
		if patch.ActivityStatus != nil {
			response.ActivityStatus = *patch.ActivityStatus
		}
		if patch.Note != nil {
			response.Note = strings.TrimSpace(*patch.Note)
		}
		_, err = s.db.Exec(`
			INSERT INTO beta_daily_discipline_responses (
				id, tenant_id, config_id, local_date, user_id, user_label,
				wake_status, discipline_status, activity_status, note, source, created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			response.ID, response.TenantID, response.ConfigID, response.LocalDate, response.UserID, response.UserLabel,
			response.WakeStatus, response.DisciplineStatus, response.ActivityStatus, response.Note, response.Source,
			response.CreatedAt, response.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		return response, nil
	default:
		return nil, err
	}
}

func (s *featureStore) getResponse(tenantID, configID, localDate, userID string) (*DailyResponse, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_id, local_date, user_id, user_label,
		       wake_status, discipline_status, activity_status, note, source,
		       created_at, updated_at
		FROM beta_daily_discipline_responses
		WHERE tenant_id=$1 AND config_id=$2 AND local_date=$3 AND user_id=$4`,
		tenantID, configID, localDate, userID,
	)
	return scanDailyResponse(row)
}

func (s *featureStore) listResponses(tenantID, configID, localDate string) ([]DailyResponse, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, config_id, local_date, user_id, user_label,
		       wake_status, discipline_status, activity_status, note, source,
		       created_at, updated_at
		FROM beta_daily_discipline_responses
		WHERE tenant_id=$1 AND config_id=$2 AND local_date=$3
		ORDER BY user_label ASC, user_id ASC`,
		tenantID, configID, localDate,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var responses []DailyResponse
	for rows.Next() {
		response, err := scanDailyResponse(rows)
		if err != nil {
			return nil, err
		}
		responses = append(responses, *response)
	}
	return responses, rows.Err()
}

func (s *featureStore) listDisciplineDays(tenantID, configID, userID string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT local_date
		FROM beta_daily_discipline_responses
		WHERE tenant_id=$1 AND config_id=$2 AND user_id=$3 AND discipline_status=$4
		ORDER BY local_date DESC`,
		tenantID, configID, userID, statusYes,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dates []string
	for rows.Next() {
		var localDate string
		if err := rows.Scan(&localDate); err != nil {
			return nil, err
		}
		dates = append(dates, localDate)
	}
	return dates, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanSurveyConfig(row scanner) (*SurveyConfig, error) {
	var cfg SurveyConfig
	var namedResults int
	var streaksEnabled int
	var dmDetailsEnabled int
	var enabled int
	var createdAt flexibleTime
	var updatedAt flexibleTime
	err := row.Scan(
		&cfg.ID, &cfg.TenantID, &cfg.Key, &cfg.Name, &cfg.Channel, &cfg.ChatID, &cfg.ThreadID,
		&cfg.Timezone, &cfg.SurveyWindowStart, &cfg.SurveyWindowEnd, &cfg.SummaryTime,
		&cfg.TargetWakeTime, &cfg.WakeQuestion, &cfg.DisciplineQuestion, &cfg.ActivityQuestion,
		&namedResults, &streaksEnabled, &dmDetailsEnabled, &enabled, &createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, errSurveyConfigNotFound
		}
		return nil, err
	}
	cfg.NamedResults = intToBool(namedResults)
	cfg.StreaksEnabled = intToBool(streaksEnabled)
	cfg.DMDetailsEnabled = intToBool(dmDetailsEnabled)
	cfg.Enabled = intToBool(enabled)
	cfg.CreatedAt = createdAt.Time
	cfg.UpdatedAt = updatedAt.Time
	cfg = cfg.withDefaults()
	return &cfg, nil
}

func scanSurveyPost(row scanner) (*SurveyPost, error) {
	var post SurveyPost
	var createdAt flexibleTime
	err := row.Scan(&post.ID, &post.TenantID, &post.ConfigID, &post.LocalDate, &post.PostKind, &post.PollID, &post.MessageID, &createdAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, errSurveyPostNotFound
		}
		return nil, err
	}
	post.CreatedAt = createdAt.Time
	return &post, nil
}

func scanDailyResponse(row scanner) (*DailyResponse, error) {
	var response DailyResponse
	var createdAt flexibleTime
	var updatedAt flexibleTime
	err := row.Scan(
		&response.ID, &response.TenantID, &response.ConfigID, &response.LocalDate, &response.UserID, &response.UserLabel,
		&response.WakeStatus, &response.DisciplineStatus, &response.ActivityStatus, &response.Note, &response.Source,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	response.CreatedAt = createdAt.Time
	response.UpdatedAt = updatedAt.Time
	return &response, nil
}

type flexibleTime struct {
	time.Time
}

func (ft *flexibleTime) Scan(src any) error {
	if src == nil {
		ft.Time = time.Time{}
		return nil
	}
	switch v := src.(type) {
	case time.Time:
		ft.Time = v
		return nil
	case string:
		return ft.parse(v)
	case []byte:
		return ft.parse(string(v))
	default:
		return fmt.Errorf("daily discipline time: unsupported type %T", src)
	}
}

func (ft *flexibleTime) parse(raw string) error {
	value := strings.TrimSpace(raw)
	if idx := strings.Index(value, " m="); idx > 0 {
		value = value[:idx]
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			ft.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("daily discipline time: cannot parse %q", raw)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func intToBool(v int) bool { return v != 0 }

func uuidString() string { return uuid.NewString() }
