package topictoolrouting

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var errTopicRoutingConfigNotFound = errors.New("topic tool routing config not found")

type TopicRoutingConfig struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenant_id,omitempty"`
	Key             string    `json:"key"`
	Name            string    `json:"name"`
	Channel         string    `json:"channel"`
	ChatID          string    `json:"chat_id"`
	ThreadID        int       `json:"thread_id"`
	EnabledFeatures []string  `json:"enabled_features,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (cfg TopicRoutingConfig) withDefaults() TopicRoutingConfig {
	cfg.Key = normalizeConfigKey(cfg.Key)
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.Channel = strings.TrimSpace(cfg.Channel)
	cfg.ChatID = strings.TrimSpace(cfg.ChatID)
	cfg.ThreadID = normalizeThreadID(cfg.ThreadID)
	cfg.EnabledFeatures = normalizeFeatureNames(cfg.EnabledFeatures)
	if cfg.Key == "" && cfg.Channel != "" && cfg.ChatID != "" {
		cfg.Key = defaultConfigKey(cfg.Channel, cfg.ChatID, cfg.ThreadID)
	}
	if cfg.Name == "" {
		cfg.Name = cfg.Key
	}
	return cfg
}

type scanner interface {
	Scan(dest ...any) error
}

type featureStore struct {
	db *sql.DB
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_topic_tool_routing_configs (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			config_key TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			thread_id INTEGER NOT NULL DEFAULT 0,
			enabled_features TEXT NOT NULL DEFAULT '[]',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, config_key),
			UNIQUE (tenant_id, channel, chat_id, thread_id)
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_topic_tool_routing_scope ON beta_topic_tool_routing_configs(tenant_id, channel, chat_id, thread_id)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) upsertConfig(cfg *TopicRoutingConfig) (*TopicRoutingConfig, error) {
	value := cfg.withDefaults()
	now := nowUTC()

	existing, err := s.getConfigByKey(value.TenantID, value.Key)
	switch {
	case err == nil:
		value.ID = existing.ID
		value.CreatedAt = existing.CreatedAt
		value.UpdatedAt = now
		_, err = s.db.Exec(`
			UPDATE beta_topic_tool_routing_configs
			SET name=$3, channel=$4, chat_id=$5, thread_id=$6, enabled_features=$7, updated_at=$8
			WHERE id=$1 AND tenant_id=$2`,
			value.ID, value.TenantID, value.Name, value.Channel, value.ChatID, value.ThreadID,
			encodeStringSlice(value.EnabledFeatures), value.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		return s.getConfigByKey(value.TenantID, value.Key)
	case errors.Is(err, errTopicRoutingConfigNotFound):
		if value.ID == "" {
			value.ID = uuid.NewString()
		}
		value.CreatedAt = now
		value.UpdatedAt = now
		_, err = s.db.Exec(`
			INSERT INTO beta_topic_tool_routing_configs (
				id, tenant_id, config_key, name, channel, chat_id, thread_id,
				enabled_features, created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			value.ID, value.TenantID, value.Key, value.Name, value.Channel, value.ChatID, value.ThreadID,
			encodeStringSlice(value.EnabledFeatures), value.CreatedAt, value.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		return s.getConfigByKey(value.TenantID, value.Key)
	default:
		return nil, err
	}
}

func (s *featureStore) getConfigByKey(tenantID, key string) (*TopicRoutingConfig, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, enabled_features, created_at, updated_at
		FROM beta_topic_tool_routing_configs
		WHERE tenant_id=$1 AND config_key=$2`,
		tenantID, normalizeConfigKey(key),
	)
	return scanTopicRoutingConfig(row)
}

func (s *featureStore) getConfigByTarget(tenantID, channel, chatID string, threadID int) (*TopicRoutingConfig, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, enabled_features, created_at, updated_at
		FROM beta_topic_tool_routing_configs
		WHERE tenant_id=$1 AND channel=$2 AND chat_id=$3 AND thread_id=$4`,
		tenantID, strings.TrimSpace(channel), strings.TrimSpace(chatID), normalizeThreadID(threadID),
	)
	return scanTopicRoutingConfig(row)
}

func (s *featureStore) resolveConfigByTarget(tenantID, channel, chatID string, threadID int) (*TopicRoutingConfig, string, error) {
	cfg, err := s.getConfigByTarget(tenantID, channel, chatID, threadID)
	if err == nil {
		return cfg, matchKindExact, nil
	}
	if !errors.Is(err, errTopicRoutingConfigNotFound) {
		return nil, matchKindNone, err
	}
	if normalizeThreadID(threadID) == 0 {
		return nil, matchKindNone, errTopicRoutingConfigNotFound
	}

	cfg, err = s.getConfigByTarget(tenantID, channel, chatID, 0)
	if err == nil {
		return cfg, matchKindChatDefault, nil
	}
	if !errors.Is(err, errTopicRoutingConfigNotFound) {
		return nil, matchKindNone, err
	}
	return nil, matchKindNone, errTopicRoutingConfigNotFound
}

func (s *featureStore) listConfigs(tenantID string) ([]TopicRoutingConfig, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, config_key, name, channel, chat_id, thread_id, enabled_features, created_at, updated_at
		FROM beta_topic_tool_routing_configs
		WHERE tenant_id=$1
		ORDER BY config_key ASC`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []TopicRoutingConfig
	for rows.Next() {
		cfg, err := scanTopicRoutingConfig(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, *cfg)
	}
	return configs, rows.Err()
}

func scanTopicRoutingConfig(row scanner) (*TopicRoutingConfig, error) {
	var (
		cfg             TopicRoutingConfig
		enabledFeatures string
	)
	if err := row.Scan(
		&cfg.ID,
		&cfg.TenantID,
		&cfg.Key,
		&cfg.Name,
		&cfg.Channel,
		&cfg.ChatID,
		&cfg.ThreadID,
		&enabledFeatures,
		&cfg.CreatedAt,
		&cfg.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errTopicRoutingConfigNotFound
		}
		return nil, err
	}

	if enabledFeatures != "" {
		_ = json.Unmarshal([]byte(enabledFeatures), &cfg.EnabledFeatures)
	}
	cfg.ThreadID = normalizeThreadID(cfg.ThreadID)
	cfg.EnabledFeatures = normalizeFeatureNames(cfg.EnabledFeatures)
	return &cfg, nil
}

func encodeStringSlice(values []string) string {
	encoded, err := json.Marshal(normalizeFeatureNames(values))
	if err != nil {
		return "[]"
	}
	return string(encoded)
}
