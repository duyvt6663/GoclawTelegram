package featurerequests

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Status constants for feature requests.
const (
	StatusPending   = "pending"   // waiting for approval poll
	StatusApproved  = "approved"  // poll passed, ready to build
	StatusBuilding  = "building"  // codex agent is working
	StatusCompleted = "completed" // build finished
	StatusFailed    = "failed"    // build failed
	StatusRejected  = "rejected"  // poll expired without enough votes
)

// FeatureRequest represents a user-requested beta feature.
type FeatureRequest struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"-"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	RequestedBy string    `json:"requested_by"`
	Channel     string    `json:"channel"`
	ChatID      string    `json:"chat_id"`
	LocalKey    string    `json:"local_key,omitempty"`
	Status      string    `json:"status"`
	PollID      string    `json:"poll_id,omitempty"`
	PollMsgID   int       `json:"poll_msg_id,omitempty"`
	Approvals   int       `json:"approvals"`
	Voters      []string  `json:"voters,omitempty"`
	Plan        string    `json:"plan,omitempty"`
	BuildLog    string    `json:"build_log,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type featureStore struct {
	db *sql.DB
}

func (s *featureStore) migrate() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS beta_feature_requests (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			requested_by TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			local_key TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			poll_id TEXT NOT NULL DEFAULT '',
			poll_msg_id INTEGER NOT NULL DEFAULT 0,
			approvals INTEGER NOT NULL DEFAULT 0,
			voters TEXT NOT NULL DEFAULT '[]',
			plan TEXT NOT NULL DEFAULT '',
			build_log TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}

	for _, stmt := range []string{
		`ALTER TABLE beta_feature_requests ADD COLUMN tenant_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE beta_feature_requests ADD COLUMN local_key TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.Exec(stmt); err != nil && !isDuplicateColumnErr(err) {
			return err
		}
	}

	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_beta_feature_requests_tenant_status ON beta_feature_requests(tenant_id, status)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_beta_feature_requests_tenant_poll_id ON beta_feature_requests(tenant_id, poll_id)`); err != nil {
		return err
	}

	return nil
}

func (s *featureStore) create(req *FeatureRequest) error {
	votersJSON, _ := json.Marshal(req.Voters)
	_, err := s.db.Exec(`
		INSERT INTO beta_feature_requests (id, tenant_id, title, description, requested_by, channel, chat_id, local_key, status, poll_id, poll_msg_id, approvals, voters, plan, build_log, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		req.ID, normalizeTenantID(req.TenantID), req.Title, req.Description, req.RequestedBy, req.Channel, req.ChatID,
		req.LocalKey, req.Status, req.PollID, req.PollMsgID, req.Approvals, string(votersJSON),
		req.Plan, req.BuildLog, req.CreatedAt.UTC(), req.UpdatedAt.UTC(),
	)
	return err
}

func (s *featureStore) update(req *FeatureRequest) error {
	votersJSON, _ := json.Marshal(req.Voters)
	req.UpdatedAt = time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE beta_feature_requests
		SET title=$3, description=$4, requested_by=$5, channel=$6, chat_id=$7, local_key=$8,
		    status=$9, poll_id=$10, poll_msg_id=$11, approvals=$12, voters=$13,
		    plan=$14, build_log=$15, updated_at=$16
		WHERE id=$1 AND tenant_id=$2`,
		req.ID, normalizeTenantID(req.TenantID), req.Title, req.Description, req.RequestedBy, req.Channel, req.ChatID,
		req.LocalKey, req.Status, req.PollID, req.PollMsgID, req.Approvals, string(votersJSON),
		req.Plan, req.BuildLog, req.UpdatedAt,
	)
	return err
}

func (s *featureStore) getByID(tenantID, id string) (*FeatureRequest, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, title, description, requested_by, channel, chat_id, local_key,
		       status, poll_id, poll_msg_id, approvals, voters, plan, build_log, created_at, updated_at
		FROM beta_feature_requests WHERE id=$1 AND tenant_id=$2`, id, normalizeTenantID(tenantID))
	return scanFeatureRequest(row)
}

func (s *featureStore) getByPollID(tenantID, pollID string) (*FeatureRequest, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, title, description, requested_by, channel, chat_id, local_key,
		       status, poll_id, poll_msg_id, approvals, voters, plan, build_log, created_at, updated_at
		FROM beta_feature_requests
		WHERE tenant_id=$1 AND poll_id=$2 AND status IN ('pending', 'rejected')`,
		normalizeTenantID(tenantID), pollID)
	return scanFeatureRequest(row)
}

func (s *featureStore) list(tenantID string) ([]FeatureRequest, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, title, description, requested_by, channel, chat_id, local_key,
		       status, poll_id, poll_msg_id, approvals, voters, plan, build_log, created_at, updated_at
		FROM beta_feature_requests
		WHERE tenant_id=$1
		ORDER BY created_at DESC LIMIT 50`, normalizeTenantID(tenantID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []FeatureRequest
	for rows.Next() {
		req, err := scanFeatureRequestRows(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, *req)
	}
	return results, rows.Err()
}

type scannable interface {
	Scan(dest ...any) error
}

func scanFeatureRequest(row scannable) (*FeatureRequest, error) {
	var req FeatureRequest
	var votersJSON string
	var createdAt flexibleTime
	var updatedAt flexibleTime
	err := row.Scan(
		&req.ID, &req.TenantID, &req.Title, &req.Description, &req.RequestedBy, &req.Channel, &req.ChatID, &req.LocalKey,
		&req.Status, &req.PollID, &req.PollMsgID, &req.Approvals, &votersJSON,
		&req.Plan, &req.BuildLog, &createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("feature request not found")
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(votersJSON), &req.Voters)
	req.CreatedAt = createdAt.Time
	req.UpdatedAt = updatedAt.Time
	return &req, nil
}

func scanFeatureRequestRows(rows *sql.Rows) (*FeatureRequest, error) {
	var req FeatureRequest
	var votersJSON string
	var createdAt flexibleTime
	var updatedAt flexibleTime
	err := rows.Scan(
		&req.ID, &req.TenantID, &req.Title, &req.Description, &req.RequestedBy, &req.Channel, &req.ChatID, &req.LocalKey,
		&req.Status, &req.PollID, &req.PollMsgID, &req.Approvals, &votersJSON,
		&req.Plan, &req.BuildLog, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(votersJSON), &req.Voters)
	req.CreatedAt = createdAt.Time
	req.UpdatedAt = updatedAt.Time
	return &req, nil
}

func normalizeTenantID(tenantID string) string {
	return strings.TrimSpace(tenantID)
}

func isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists")
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
		return fmt.Errorf("feature request time: unsupported type %T", src)
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
	return fmt.Errorf("feature request time: cannot parse %q", raw)
}
