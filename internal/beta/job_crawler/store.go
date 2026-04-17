package jobcrawler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	errCrawlerConfigNotFound = errors.New("job crawler config not found")
	errCrawlerRunNotFound    = errors.New("job crawler run not found")
)

type JobCrawlerConfig struct {
	ID                        string    `json:"id"`
	TenantID                  string    `json:"tenant_id,omitempty"`
	Key                       string    `json:"key"`
	Name                      string    `json:"name"`
	Channel                   string    `json:"channel"`
	ChatID                    string    `json:"chat_id"`
	ThreadID                  int       `json:"thread_id"`
	Timezone                  string    `json:"timezone"`
	KeywordsInclude           []string  `json:"keywords_include,omitempty"`
	KeywordsExclude           []string  `json:"keywords_exclude,omitempty"`
	AllowedRoles              []string  `json:"allowed_roles,omitempty"`
	MaxSeniorityLevel         string    `json:"max_seniority_level,omitempty"`
	RemoteOnly                bool      `json:"remote_only"`
	LocationMode              string    `json:"location_mode"`
	RemotePriority            float64   `json:"remote_priority"`
	VietnamPriority           float64   `json:"vietnam_priority"`
	Sources                   []string  `json:"sources,omitempty"`
	PostTime                  string    `json:"post_time"`
	MaxResults                int       `json:"max_results"`
	DedupeWindowDays          int       `json:"dedupe_window_days"`
	IncludeAISummary          bool      `json:"include_ai_summary"`
	EnableLinkedInProxySource bool      `json:"enable_linkedin_proxy_source"`
	HardTitleFilter           bool      `json:"hard_title_filter"`
	EnableLLMRerank           bool      `json:"enable_llm_rerank"`
	LLMRerankTopN             int       `json:"llm_rerank_top_n"`
	Enabled                   bool      `json:"enabled"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

func (cfg JobCrawlerConfig) withDefaults() JobCrawlerConfig {
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
	cfg.KeywordsInclude = normalizeKeywords(cfg.KeywordsInclude)
	cfg.KeywordsExclude = normalizeKeywords(cfg.KeywordsExclude)
	cfg.AllowedRoles = normalizeAllowedRoles(cfg.AllowedRoles)
	if cfg.MaxSeniorityLevel == "" {
		cfg.MaxSeniorityLevel = defaultMaxSeniorityLevel
	} else {
		cfg.MaxSeniorityLevel = normalizeSeniorityLevel(cfg.MaxSeniorityLevel)
	}
	cfg.Sources = normalizeSources(cfg.Sources)
	if cfg.LocationMode == "" {
		cfg.LocationMode = defaultLocationMode
	}
	if cfg.PostTime == "" {
		cfg.PostTime = defaultPostTime
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = defaultMaxResults
	}
	if cfg.MaxResults > maxMaxResults {
		cfg.MaxResults = maxMaxResults
	}
	if cfg.DedupeWindowDays <= 0 {
		cfg.DedupeWindowDays = defaultDedupeWindowDays
	}
	if cfg.DedupeWindowDays > maxDedupeWindowDays {
		cfg.DedupeWindowDays = maxDedupeWindowDays
	}
	if cfg.LLMRerankTopN <= 0 {
		cfg.LLMRerankTopN = defaultLLMRerankTopN
	}
	if cfg.LLMRerankTopN > maxLLMRerankTopN {
		cfg.LLMRerankTopN = maxLLMRerankTopN
	}
	if cfg.RemotePriority <= 0 || cfg.VietnamPriority <= 0 {
		remotePriority, vietnamPriority := defaultLocationPriorities(cfg.LocationMode)
		if cfg.RemotePriority <= 0 {
			cfg.RemotePriority = remotePriority
		}
		if cfg.VietnamPriority <= 0 {
			cfg.VietnamPriority = vietnamPriority
		}
	}
	return cfg
}

func (cfg JobCrawlerConfig) localNow(now time.Time) time.Time {
	return now.In(loadLocation(cfg.Timezone))
}

func (cfg JobCrawlerConfig) localDate(now time.Time) string {
	return cfg.localNow(now).Format("2006-01-02")
}

type JobCrawlerRun struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenant_id,omitempty"`
	ConfigID      string     `json:"config_id"`
	LocalDate     string     `json:"local_date"`
	TriggerKind   string     `json:"trigger_kind"`
	Status        string     `json:"status"`
	TotalFetched  int        `json:"total_fetched"`
	TotalFiltered int        `json:"total_filtered"`
	TotalPosted   int        `json:"total_posted"`
	ErrorText     string     `json:"error_text,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

type SeenJobSnapshot struct {
	JobHash            string
	Source             string
	Title              string
	Company            string
	URL                string
	NormalizedTitle    string
	SeniorityLevel     string
	ContentTokens      []string
	NormalizedLocation string
	IsRemote           bool
	IsVietnam          bool
	Score              float64
	Summary            string
}

type RecentSeenJob struct {
	JobHash         string
	Company         string
	Title           string
	NormalizedTitle string
	SeniorityLevel  string
	ContentTokens   []string
	LastPostedAt    time.Time
}

type featureStore struct {
	db *sql.DB
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_job_crawler_configs (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_key TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			thread_id INTEGER NOT NULL DEFAULT 0,
			timezone TEXT NOT NULL DEFAULT '` + defaultTimezone + `',
			keywords_include TEXT NOT NULL DEFAULT '[]',
			keywords_exclude TEXT NOT NULL DEFAULT '[]',
			allowed_roles TEXT NOT NULL DEFAULT '[]',
			max_seniority_level TEXT NOT NULL DEFAULT '` + defaultMaxSeniorityLevel + `',
			remote_only INTEGER NOT NULL DEFAULT 0,
			location_mode TEXT NOT NULL DEFAULT '` + defaultLocationMode + `',
			remote_priority REAL NOT NULL DEFAULT 0,
			vietnam_priority REAL NOT NULL DEFAULT 0,
			sources TEXT NOT NULL DEFAULT '[]',
			post_time TEXT NOT NULL DEFAULT '` + defaultPostTime + `',
			max_results INTEGER NOT NULL DEFAULT ` + fmt.Sprintf("%d", defaultMaxResults) + `,
			dedupe_window_days INTEGER NOT NULL DEFAULT ` + fmt.Sprintf("%d", defaultDedupeWindowDays) + `,
			include_ai_summary INTEGER NOT NULL DEFAULT 0,
			enable_linkedin_proxy_source INTEGER NOT NULL DEFAULT 0,
			hard_title_filter INTEGER NOT NULL DEFAULT 0,
			enable_llm_rerank INTEGER NOT NULL DEFAULT 0,
			llm_rerank_top_n INTEGER NOT NULL DEFAULT ` + fmt.Sprintf("%d", defaultLLMRerankTopN) + `,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, config_key),
			UNIQUE (tenant_id, channel, chat_id, thread_id)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_job_crawler_seen_jobs (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_id TEXT NOT NULL,
			job_hash TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			company TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL DEFAULT '',
			normalized_title TEXT NOT NULL DEFAULT '',
			seniority_level TEXT NOT NULL DEFAULT '',
			content_tokens TEXT NOT NULL DEFAULT '[]',
			normalized_location TEXT NOT NULL DEFAULT '',
			is_remote INTEGER NOT NULL DEFAULT 0,
			is_vietnam INTEGER NOT NULL DEFAULT 0,
			last_score REAL NOT NULL DEFAULT 0,
			last_summary TEXT NOT NULL DEFAULT '',
			first_seen_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_seen_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_posted_at TIMESTAMP NULL,
			UNIQUE (tenant_id, config_id, job_hash)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_job_crawler_runs (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_id TEXT NOT NULL,
			local_date TEXT NOT NULL,
			trigger_kind TEXT NOT NULL DEFAULT '` + triggerKindScheduled + `',
			status TEXT NOT NULL DEFAULT '` + runStatusRunning + `',
			total_fetched INTEGER NOT NULL DEFAULT 0,
			total_filtered INTEGER NOT NULL DEFAULT 0,
			total_posted INTEGER NOT NULL DEFAULT 0,
			error_text TEXT NOT NULL DEFAULT '',
			started_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			finished_at TIMESTAMP NULL
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_job_crawler_embeddings (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			embedding_kind TEXT NOT NULL DEFAULT '',
			subject_hash TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '',
			provider_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			vector_json TEXT NOT NULL DEFAULT '[]',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, embedding_kind, subject_hash, provider_name, model)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS beta_job_crawler_run_traces (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			run_id TEXT NOT NULL,
			config_id TEXT NOT NULL,
			trace_id TEXT NOT NULL DEFAULT '',
			job_hash TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			company TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL DEFAULT '',
			final_outcome TEXT NOT NULL DEFAULT '',
			score REAL NOT NULL DEFAULT 0,
			trace_json TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, run_id, trace_id)
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_job_crawler_configs_enabled ON beta_job_crawler_configs(tenant_id, enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_job_crawler_runs_lookup ON beta_job_crawler_runs(tenant_id, config_id, local_date, trigger_kind, started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_job_crawler_seen_jobs_posted ON beta_job_crawler_seen_jobs(tenant_id, config_id, last_posted_at)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_job_crawler_embeddings_lookup ON beta_job_crawler_embeddings(tenant_id, embedding_kind, subject_hash, provider_name, model)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_job_crawler_run_traces_lookup ON beta_job_crawler_run_traces(tenant_id, run_id, created_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		`ALTER TABLE beta_job_crawler_configs ADD COLUMN allowed_roles TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE beta_job_crawler_configs ADD COLUMN max_seniority_level TEXT NOT NULL DEFAULT '` + defaultMaxSeniorityLevel + `'`,
		`ALTER TABLE beta_job_crawler_configs ADD COLUMN enable_linkedin_proxy_source INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE beta_job_crawler_configs ADD COLUMN hard_title_filter INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE beta_job_crawler_configs ADD COLUMN enable_llm_rerank INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE beta_job_crawler_configs ADD COLUMN llm_rerank_top_n INTEGER NOT NULL DEFAULT ` + fmt.Sprintf("%d", defaultLLMRerankTopN),
		`ALTER TABLE beta_job_crawler_seen_jobs ADD COLUMN normalized_title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE beta_job_crawler_seen_jobs ADD COLUMN seniority_level TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE beta_job_crawler_seen_jobs ADD COLUMN content_tokens TEXT NOT NULL DEFAULT '[]'`,
	} {
		if _, err := s.db.Exec(stmt); err != nil && !isDuplicateColumnErr(err) {
			return err
		}
	}
	return nil
}

func (s *featureStore) upsertConfig(cfg *JobCrawlerConfig) (*JobCrawlerConfig, error) {
	cfgValue := cfg.withDefaults()
	now := time.Now().UTC()

	existing, err := s.getConfigByKey(cfgValue.TenantID, cfgValue.Key)
	if errors.Is(err, errCrawlerConfigNotFound) {
		if targetExisting, targetErr := s.getConfigByTarget(cfgValue.TenantID, cfgValue.Channel, cfgValue.ChatID, cfgValue.ThreadID); targetErr == nil {
			existing = targetExisting
			err = nil
		}
	}

	switch {
	case err == nil:
		cfgValue.ID = existing.ID
		cfgValue.CreatedAt = existing.CreatedAt
		cfgValue.UpdatedAt = now
		_, err = s.db.Exec(`
			UPDATE beta_job_crawler_configs
			SET config_key=$3, name=$4, channel=$5, chat_id=$6, thread_id=$7, timezone=$8,
			    keywords_include=$9, keywords_exclude=$10, allowed_roles=$11, max_seniority_level=$12,
			    remote_only=$13, location_mode=$14, remote_priority=$15, vietnam_priority=$16,
			    sources=$17, post_time=$18, max_results=$19, dedupe_window_days=$20,
			    include_ai_summary=$21, enable_linkedin_proxy_source=$22, hard_title_filter=$23,
			    enable_llm_rerank=$24, llm_rerank_top_n=$25, enabled=$26, updated_at=$27
			WHERE id=$1 AND tenant_id=$2`,
			cfgValue.ID, cfgValue.TenantID, cfgValue.Key, cfgValue.Name, cfgValue.Channel, cfgValue.ChatID,
			cfgValue.ThreadID, cfgValue.Timezone, encodeStringSlice(cfgValue.KeywordsInclude),
			encodeStringSlice(cfgValue.KeywordsExclude), encodeStringSlice(cfgValue.AllowedRoles), cfgValue.MaxSeniorityLevel,
			boolToInt(cfgValue.RemoteOnly), cfgValue.LocationMode, cfgValue.RemotePriority, cfgValue.VietnamPriority,
			encodeStringSlice(cfgValue.Sources), cfgValue.PostTime, cfgValue.MaxResults, cfgValue.DedupeWindowDays,
			boolToInt(cfgValue.IncludeAISummary), boolToInt(cfgValue.EnableLinkedInProxySource), boolToInt(cfgValue.HardTitleFilter),
			boolToInt(cfgValue.EnableLLMRerank), cfgValue.LLMRerankTopN,
			boolToInt(cfgValue.Enabled), cfgValue.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		return s.getConfigByKey(cfgValue.TenantID, cfgValue.Key)
	case errors.Is(err, errCrawlerConfigNotFound):
		if cfgValue.ID == "" {
			cfgValue.ID = uuid.NewString()
		}
		cfgValue.CreatedAt = now
		cfgValue.UpdatedAt = now
		_, err = s.db.Exec(`
			INSERT INTO beta_job_crawler_configs (
				id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
				keywords_include, keywords_exclude, allowed_roles, max_seniority_level,
				remote_only, location_mode, remote_priority, vietnam_priority, sources,
				post_time, max_results, dedupe_window_days, include_ai_summary,
				enable_linkedin_proxy_source, hard_title_filter, enable_llm_rerank,
				llm_rerank_top_n, enabled, created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28)`,
			cfgValue.ID, cfgValue.TenantID, cfgValue.Key, cfgValue.Name, cfgValue.Channel, cfgValue.ChatID, cfgValue.ThreadID,
			cfgValue.Timezone, encodeStringSlice(cfgValue.KeywordsInclude), encodeStringSlice(cfgValue.KeywordsExclude),
			encodeStringSlice(cfgValue.AllowedRoles), cfgValue.MaxSeniorityLevel, boolToInt(cfgValue.RemoteOnly),
			cfgValue.LocationMode, cfgValue.RemotePriority, cfgValue.VietnamPriority, encodeStringSlice(cfgValue.Sources),
			cfgValue.PostTime, cfgValue.MaxResults, cfgValue.DedupeWindowDays, boolToInt(cfgValue.IncludeAISummary),
			boolToInt(cfgValue.EnableLinkedInProxySource), boolToInt(cfgValue.HardTitleFilter),
			boolToInt(cfgValue.EnableLLMRerank), cfgValue.LLMRerankTopN, boolToInt(cfgValue.Enabled),
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

func (s *featureStore) getConfigByKey(tenantID, key string) (*JobCrawlerConfig, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       keywords_include, keywords_exclude, allowed_roles, max_seniority_level, remote_only, location_mode,
		       remote_priority, vietnam_priority, sources, post_time, max_results,
		       dedupe_window_days, include_ai_summary, enable_linkedin_proxy_source, hard_title_filter,
		       enable_llm_rerank, llm_rerank_top_n,
		       enabled, created_at, updated_at
		FROM beta_job_crawler_configs
		WHERE tenant_id=$1 AND config_key=$2`,
		tenantID, normalizeConfigKey(key),
	)
	return scanJobCrawlerConfig(row)
}

func (s *featureStore) getConfigByID(tenantID, configID string) (*JobCrawlerConfig, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       keywords_include, keywords_exclude, allowed_roles, max_seniority_level, remote_only, location_mode,
		       remote_priority, vietnam_priority, sources, post_time, max_results,
		       dedupe_window_days, include_ai_summary, enable_linkedin_proxy_source, hard_title_filter,
		       enable_llm_rerank, llm_rerank_top_n,
		       enabled, created_at, updated_at
		FROM beta_job_crawler_configs
		WHERE tenant_id=$1 AND id=$2`,
		tenantID, strings.TrimSpace(configID),
	)
	return scanJobCrawlerConfig(row)
}

func (s *featureStore) getConfigByTarget(tenantID, channel, chatID string, threadID int) (*JobCrawlerConfig, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       keywords_include, keywords_exclude, allowed_roles, max_seniority_level, remote_only, location_mode,
		       remote_priority, vietnam_priority, sources, post_time, max_results,
		       dedupe_window_days, include_ai_summary, enable_linkedin_proxy_source, hard_title_filter,
		       enable_llm_rerank, llm_rerank_top_n,
		       enabled, created_at, updated_at
		FROM beta_job_crawler_configs
		WHERE tenant_id=$1 AND channel=$2 AND chat_id=$3 AND thread_id=$4`,
		tenantID, channel, chatID, normalizeThreadID(threadID),
	)
	return scanJobCrawlerConfig(row)
}

func (s *featureStore) listConfigs(tenantID string) ([]JobCrawlerConfig, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       keywords_include, keywords_exclude, allowed_roles, max_seniority_level, remote_only, location_mode,
		       remote_priority, vietnam_priority, sources, post_time, max_results,
		       dedupe_window_days, include_ai_summary, enable_linkedin_proxy_source, hard_title_filter,
		       enable_llm_rerank, llm_rerank_top_n,
		       enabled, created_at, updated_at
		FROM beta_job_crawler_configs
		WHERE tenant_id=$1
		ORDER BY channel, chat_id, thread_id, config_key`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []JobCrawlerConfig
	for rows.Next() {
		cfg, err := scanJobCrawlerConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *featureStore) listEnabledConfigs() ([]JobCrawlerConfig, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, timezone,
		       keywords_include, keywords_exclude, allowed_roles, max_seniority_level, remote_only, location_mode,
		       remote_priority, vietnam_priority, sources, post_time, max_results,
		       dedupe_window_days, include_ai_summary, enable_linkedin_proxy_source, hard_title_filter,
		       enable_llm_rerank, llm_rerank_top_n,
		       enabled, created_at, updated_at
		FROM beta_job_crawler_configs
		WHERE enabled=1
		ORDER BY tenant_id, channel, chat_id, thread_id, config_key`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []JobCrawlerConfig
	for rows.Next() {
		cfg, err := scanJobCrawlerConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *featureStore) createRun(run *JobCrawlerRun) error {
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	if run.Status == "" {
		run.Status = runStatusRunning
	}
	_, err := s.db.Exec(`
		INSERT INTO beta_job_crawler_runs (
			id, tenant_id, config_id, local_date, trigger_kind, status,
			total_fetched, total_filtered, total_posted, error_text, started_at, finished_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		run.ID, run.TenantID, run.ConfigID, run.LocalDate, run.TriggerKind, run.Status,
		run.TotalFetched, run.TotalFiltered, run.TotalPosted, run.ErrorText, run.StartedAt, dbTime(run.FinishedAt),
	)
	return err
}

func (s *featureStore) finishRun(run *JobCrawlerRun) error {
	if run.FinishedAt == nil {
		now := time.Now().UTC()
		run.FinishedAt = &now
	}
	res, err := s.db.Exec(`
		UPDATE beta_job_crawler_runs
		SET status=$3, total_fetched=$4, total_filtered=$5, total_posted=$6,
		    error_text=$7, finished_at=$8
		WHERE id=$1 AND tenant_id=$2`,
		run.ID, run.TenantID, run.Status, run.TotalFetched, run.TotalFiltered, run.TotalPosted,
		run.ErrorText, *run.FinishedAt,
	)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return errCrawlerRunNotFound
	}
	return nil
}

func (s *featureStore) hasCompletedScheduledRun(tenantID, configID, localDate string) (bool, error) {
	var count int
	if err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM beta_job_crawler_runs
		WHERE tenant_id=$1 AND config_id=$2 AND local_date=$3 AND trigger_kind=$4 AND status IN ($5, $6)`,
		tenantID, configID, localDate, triggerKindScheduled, runStatusSuccess, runStatusNoResults,
	).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *featureStore) lastRunByConfig(tenantID, configID string) (*JobCrawlerRun, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_id, local_date, trigger_kind, status,
		       total_fetched, total_filtered, total_posted, error_text, started_at, finished_at
		FROM beta_job_crawler_runs
		WHERE tenant_id=$1 AND config_id=$2
		ORDER BY started_at DESC
		LIMIT 1`,
		tenantID, configID,
	)
	return scanJobCrawlerRun(row)
}

func (s *featureStore) getRunByID(tenantID, runID string) (*JobCrawlerRun, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_id, local_date, trigger_kind, status,
		       total_fetched, total_filtered, total_posted, error_text, started_at, finished_at
		FROM beta_job_crawler_runs
		WHERE tenant_id=$1 AND id=$2`,
		tenantID, strings.TrimSpace(runID),
	)
	return scanJobCrawlerRun(row)
}

func (s *featureStore) recentlyPostedJobs(tenantID, configID string, since time.Time) ([]RecentSeenJob, error) {
	rows, err := s.db.Query(`
		SELECT job_hash, company, title, normalized_title, seniority_level, content_tokens, last_posted_at
		FROM beta_job_crawler_seen_jobs
		WHERE tenant_id=$1 AND config_id=$2 AND last_posted_at IS NOT NULL AND last_posted_at >= $3`,
		tenantID, configID, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RecentSeenJob
	for rows.Next() {
		var item RecentSeenJob
		var tokensJSON string
		if err := rows.Scan(
			&item.JobHash, &item.Company, &item.Title, &item.NormalizedTitle, &item.SeniorityLevel, &tokensJSON, &item.LastPostedAt,
		); err != nil {
			return nil, err
		}
		item.Company = cleanText(item.Company)
		item.Title = cleanText(item.Title)
		item.NormalizedTitle = cleanText(item.NormalizedTitle)
		item.SeniorityLevel = normalizeSeniorityLevel(item.SeniorityLevel)
		item.ContentTokens = decodeStringSlice(tokensJSON)
		item.LastPostedAt = item.LastPostedAt.UTC()
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *featureStore) countRecentPosts(tenantID, configID string, since time.Time) (int, error) {
	var count int
	if err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM beta_job_crawler_seen_jobs
		WHERE tenant_id=$1 AND config_id=$2 AND last_posted_at IS NOT NULL AND last_posted_at >= $3`,
		tenantID, configID, since,
	).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *featureStore) upsertSeenJob(cfg *JobCrawlerConfig, snapshot SeenJobSnapshot, postedAt *time.Time) error {
	now := time.Now().UTC()
	var existingID string
	err := s.db.QueryRow(`
		SELECT id
		FROM beta_job_crawler_seen_jobs
		WHERE tenant_id=$1 AND config_id=$2 AND job_hash=$3`,
		cfg.TenantID, cfg.ID, snapshot.JobHash,
	).Scan(&existingID)
	switch {
	case err == nil:
		_, err = s.db.Exec(`
			UPDATE beta_job_crawler_seen_jobs
			SET source=$4, title=$5, company=$6, url=$7, normalized_title=$8, seniority_level=$9,
			    content_tokens=$10, normalized_location=$11, is_remote=$12, is_vietnam=$13,
			    last_score=$14, last_summary=$15, last_seen_at=$16,
			    last_posted_at=COALESCE($17, last_posted_at)
			WHERE id=$1 AND tenant_id=$2 AND config_id=$3`,
			existingID, cfg.TenantID, cfg.ID, snapshot.Source, snapshot.Title, snapshot.Company,
			snapshot.URL, snapshot.NormalizedTitle, snapshot.SeniorityLevel, encodeStringSlice(snapshot.ContentTokens),
			snapshot.NormalizedLocation, boolToInt(snapshot.IsRemote), boolToInt(snapshot.IsVietnam),
			snapshot.Score, snapshot.Summary, now, dbTime(postedAt),
		)
		return err
	case errors.Is(err, sql.ErrNoRows):
		id := uuid.NewString()
		_, err = s.db.Exec(`
			INSERT INTO beta_job_crawler_seen_jobs (
				id, tenant_id, config_id, job_hash, source, title, company, url,
				normalized_title, seniority_level, content_tokens, normalized_location,
				is_remote, is_vietnam, last_score, last_summary, first_seen_at,
				last_seen_at, last_posted_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
			id, cfg.TenantID, cfg.ID, snapshot.JobHash, snapshot.Source, snapshot.Title, snapshot.Company,
			snapshot.URL, snapshot.NormalizedTitle, snapshot.SeniorityLevel, encodeStringSlice(snapshot.ContentTokens),
			snapshot.NormalizedLocation, boolToInt(snapshot.IsRemote), boolToInt(snapshot.IsVietnam),
			snapshot.Score, snapshot.Summary, now, now, dbTime(postedAt),
		)
		return err
	default:
		return err
	}
}

func (s *featureStore) replaceRunDecisionTraces(run *JobCrawlerRun, traces []JobDecisionTrace) error {
	if s == nil || s.db == nil || run == nil {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		DELETE FROM beta_job_crawler_run_traces
		WHERE tenant_id=$1 AND run_id=$2`,
		run.TenantID, run.ID,
	); err != nil {
		return err
	}

	now := time.Now().UTC()
	for _, trace := range traces {
		traceJSON, err := json.Marshal(trace)
		if err != nil {
			return err
		}
		traceID := strings.TrimSpace(trace.TraceID)
		if traceID == "" {
			traceID = makeTraceID(trace.Source, trace.Title, trace.Company, trace.URL)
		}
		if _, err := tx.Exec(`
			INSERT INTO beta_job_crawler_run_traces (
				id, tenant_id, run_id, config_id, trace_id, job_hash, source, title, company, url,
				final_outcome, score, trace_json, created_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			uuid.NewString(), run.TenantID, run.ID, run.ConfigID, traceID, trace.JobHash, trace.Source,
			trace.Title, trace.Company, trace.URL, trace.FinalOutcome, trace.Score, string(traceJSON), now,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *featureStore) listRunDecisionTraces(tenantID, runID string) ([]JobDecisionTrace, error) {
	rows, err := s.db.Query(`
		SELECT trace_id, trace_json
		FROM beta_job_crawler_run_traces
		WHERE tenant_id=$1 AND run_id=$2
		ORDER BY created_at ASC, trace_id ASC`,
		tenantID, strings.TrimSpace(runID),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []JobDecisionTrace
	for rows.Next() {
		var traceID string
		var traceJSON string
		if err := rows.Scan(&traceID, &traceJSON); err != nil {
			return nil, err
		}
		var trace JobDecisionTrace
		if err := json.Unmarshal([]byte(traceJSON), &trace); err != nil {
			return nil, err
		}
		if trace.TraceID == "" {
			trace.TraceID = strings.TrimSpace(traceID)
		}
		out = append(out, trace)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *featureStore) cachedEmbedding(tenantID, kind, subjectHash, providerName, model, contentHash string) ([]float32, error) {
	var vectorJSON, storedHash string
	err := s.db.QueryRow(`
		SELECT vector_json, content_hash
		FROM beta_job_crawler_embeddings
		WHERE tenant_id=$1 AND embedding_kind=$2 AND subject_hash=$3 AND provider_name=$4 AND model=$5`,
		tenantID, kind, subjectHash, providerName, model,
	).Scan(&vectorJSON, &storedHash)
	switch {
	case err == nil:
		if contentHash != "" && storedHash != "" && storedHash != contentHash {
			return nil, nil
		}
		return decodeFloat32Slice(vectorJSON), nil
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	default:
		return nil, err
	}
}

func (s *featureStore) upsertEmbedding(tenantID, kind, subjectHash, contentHash, providerName, model string, embedding []float32) error {
	if len(embedding) == 0 {
		return nil
	}

	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO beta_job_crawler_embeddings (
			id, tenant_id, embedding_kind, subject_hash, content_hash,
			provider_name, model, vector_json, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (tenant_id, embedding_kind, subject_hash, provider_name, model)
		DO UPDATE SET
			content_hash=EXCLUDED.content_hash,
			vector_json=EXCLUDED.vector_json,
			updated_at=EXCLUDED.updated_at`,
		uuid.NewString(), tenantID, kind, subjectHash, contentHash,
		providerName, model, encodeFloat32Slice(embedding), now, now,
	)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJobCrawlerConfig(row rowScanner) (*JobCrawlerConfig, error) {
	var cfg JobCrawlerConfig
	var includeJSON, excludeJSON, allowedRolesJSON, sourcesJSON string
	var remoteOnly, includeAISummary, enableLinkedInProxySource, hardTitleFilter, enableLLMRerank, enabled int
	if err := row.Scan(
		&cfg.ID, &cfg.TenantID, &cfg.Key, &cfg.Name, &cfg.Channel, &cfg.ChatID, &cfg.ThreadID, &cfg.Timezone,
		&includeJSON, &excludeJSON, &allowedRolesJSON, &cfg.MaxSeniorityLevel, &remoteOnly, &cfg.LocationMode,
		&cfg.RemotePriority, &cfg.VietnamPriority, &sourcesJSON, &cfg.PostTime, &cfg.MaxResults,
		&cfg.DedupeWindowDays, &includeAISummary, &enableLinkedInProxySource, &hardTitleFilter,
		&enableLLMRerank, &cfg.LLMRerankTopN, &enabled, &cfg.CreatedAt, &cfg.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errCrawlerConfigNotFound
		}
		return nil, err
	}
	cfg.KeywordsInclude = decodeStringSlice(includeJSON)
	cfg.KeywordsExclude = decodeStringSlice(excludeJSON)
	cfg.AllowedRoles = decodeStringSlice(allowedRolesJSON)
	cfg.RemoteOnly = intToBool(remoteOnly)
	cfg.Sources = decodeStringSlice(sourcesJSON)
	cfg.IncludeAISummary = intToBool(includeAISummary)
	cfg.EnableLinkedInProxySource = intToBool(enableLinkedInProxySource)
	cfg.HardTitleFilter = intToBool(hardTitleFilter)
	cfg.EnableLLMRerank = intToBool(enableLLMRerank)
	cfg.Enabled = intToBool(enabled)
	cfg.ThreadID = normalizeThreadID(cfg.ThreadID)
	cfg = cfg.withDefaults()
	return &cfg, nil
}

func scanJobCrawlerRun(row rowScanner) (*JobCrawlerRun, error) {
	var run JobCrawlerRun
	var finishedAt sql.NullTime
	if err := row.Scan(
		&run.ID, &run.TenantID, &run.ConfigID, &run.LocalDate, &run.TriggerKind, &run.Status,
		&run.TotalFetched, &run.TotalFiltered, &run.TotalPosted, &run.ErrorText, &run.StartedAt, &finishedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errCrawlerRunNotFound
		}
		return nil, err
	}
	run.FinishedAt = nullTimePtr(finishedAt)
	return &run, nil
}
