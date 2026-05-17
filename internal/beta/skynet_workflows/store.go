package skynetworkflows

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	kindBacklog    = "backlog"
	kindExperiment = "experiment"
	kindQA         = "qa"
	kindPR         = "pr"
	kindChangeReq  = "change_request"

	statusPending    = "pending"
	statusInProgress = "in_progress"
	statusReview     = "review"
	statusRefinement = "needs_refinement"
	statusDone       = "done"
	statusFailed     = "failed"
	statusDropped    = "dropped"
)

const (
	backlogPriorityDefault         = 50
	backlogPriorityMin             = 0
	backlogPriorityMax             = 100
	backlogPriorityAgingInterval   = 24 * time.Hour
	backlogPriorityAgingStep       = 5
	backlogPriorityPersistentBoost = 50
	backlogPriorityAgeBoostMax     = 75
)

var validKinds = map[string]bool{
	kindBacklog:    true,
	kindExperiment: true,
	kindQA:         true,
	kindPR:         true,
	kindChangeReq:  true,
}

type workflowItem struct {
	ID        string            `json:"id"`
	TenantID  string            `json:"tenant_id,omitempty"`
	Kind      string            `json:"kind"`
	Status    string            `json:"status"`
	Body      string            `json:"body"`
	Source    string            `json:"source,omitempty"`
	Channel   string            `json:"channel,omitempty"`
	ChatID    string            `json:"chat_id,omitempty"`
	LocalKey  string            `json:"local_key,omitempty"`
	ClaimedBy string            `json:"claimed_by,omitempty"`
	AgentKey  string            `json:"agent_key,omitempty"`
	Result    string            `json:"result,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type queueCounts struct {
	Kind       string `json:"kind"`
	Pending    int    `json:"pending"`
	InProgress int    `json:"in_progress"`
	Review     int    `json:"review"`
	Refinement int    `json:"needs_refinement"`
	Done       int    `json:"done"`
	Failed     int    `json:"failed"`
	Dropped    int    `json:"dropped"`
}

type featureStore struct {
	db *sql.DB
	mu sync.Mutex
}

type itemScanner interface {
	Scan(dest ...any) error
}

func (s *featureStore) migrate() error {
	stmts := []string{
		`
		CREATE TABLE IF NOT EXISTS beta_skynet_workflow_items (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			body TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			local_key TEXT NOT NULL DEFAULT '',
			claimed_by TEXT NOT NULL DEFAULT '',
			agent_key TEXT NOT NULL DEFAULT '',
			result TEXT NOT NULL DEFAULT '',
			metadata TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_skynet_items_lookup ON beta_skynet_workflow_items(tenant_id, kind, status, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_skynet_items_updated ON beta_skynet_workflow_items(tenant_id, updated_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		`ALTER TABLE beta_skynet_workflow_items ADD COLUMN local_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE beta_skynet_workflow_items ADD COLUMN claimed_by TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE beta_skynet_workflow_items ADD COLUMN agent_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE beta_skynet_workflow_items ADD COLUMN result TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE beta_skynet_workflow_items ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
	} {
		if _, err := s.db.Exec(stmt); err != nil && !isDuplicateColumnErr(err) {
			return err
		}
	}
	return nil
}

func (s *featureStore) addItems(tenantID, kind string, bodies []string, origin workflowOrigin, source string, metadata map[string]string) ([]workflowItem, error) {
	if !validKinds[kind] {
		return nil, fmt.Errorf("unsupported queue kind %q", kind)
	}
	bodies = uniqueNonEmpty(bodies)
	if len(bodies) == 0 {
		return nil, fmt.Errorf("no queue items to add")
	}
	tenantID = strings.TrimSpace(tenantID)
	source = strings.TrimSpace(source)
	if source == "" {
		source = "skynet"
	}
	if metadata == nil {
		metadata = map[string]string{}
	}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	items := make([]workflowItem, 0, len(bodies))
	for _, body := range bodies {
		item := workflowItem{
			ID:        uuid.NewString(),
			TenantID:  tenantID,
			Kind:      kind,
			Status:    statusPending,
			Body:      strings.TrimSpace(body),
			Source:    source,
			Channel:   origin.Channel,
			ChatID:    origin.ChatID,
			LocalKey:  origin.LocalKey,
			Metadata:  metadata,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if _, err := s.db.Exec(`
			INSERT INTO beta_skynet_workflow_items (
				id, tenant_id, kind, status, body, source, channel, chat_id, local_key,
				claimed_by, agent_key, result, metadata, created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '', '', '', $10, $11, $12)`,
			item.ID, item.TenantID, item.Kind, item.Status, item.Body, item.Source,
			item.Channel, item.ChatID, item.LocalKey, string(metaJSON), item.CreatedAt, item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *featureStore) knownSources(tenantID, kind string, sources []string) (map[string]bool, error) {
	known := make(map[string]bool)
	if !validKinds[kind] {
		return known, fmt.Errorf("unsupported queue kind %q", kind)
	}
	for _, source := range uniqueNonEmpty(sources) {
		var exists int
		err := s.db.QueryRow(`
			SELECT 1
			FROM beta_skynet_workflow_items
			WHERE tenant_id=$1 AND kind=$2 AND source=$3
			LIMIT 1`, strings.TrimSpace(tenantID), kind, source).Scan(&exists)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return known, err
		}
		known[source] = true
	}
	return known, nil
}

func (s *featureStore) claimNext(tenantID, kind, claimedBy, agentKey string) (*workflowItem, error) {
	if !validKinds[kind] {
		return nil, fmt.Errorf("unsupported queue kind %q", kind)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	item, err := s.firstPending(tenantID, kind)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, nil
	}

	now := time.Now().UTC()
	res, err := s.db.Exec(`
		UPDATE beta_skynet_workflow_items
		SET status=$3, claimed_by=$4, agent_key=$5, updated_at=$6
		WHERE id=$1 AND tenant_id=$2 AND status='pending'`,
		item.ID, strings.TrimSpace(tenantID), statusInProgress, strings.TrimSpace(claimedBy), strings.TrimSpace(agentKey), now,
	)
	if err != nil {
		return nil, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return nil, nil
	}
	return s.getItem(tenantID, item.ID)
}

func (s *featureStore) claimPendingByIDs(tenantID, kind, claimedBy, agentKey string, ids []string) ([]workflowItem, error) {
	if !validKinds[kind] {
		return nil, fmt.Errorf("unsupported queue kind %q", kind)
	}
	ids = uniqueNonEmpty(ids)
	if len(ids) == 0 {
		return nil, fmt.Errorf("item_ids is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	items := make([]workflowItem, 0, len(ids))
	for _, id := range ids {
		res, err := s.db.Exec(`
			UPDATE beta_skynet_workflow_items
			SET status=$4, claimed_by=$5, agent_key=$6, updated_at=$7
			WHERE id=$1 AND tenant_id=$2 AND kind=$3 AND status='pending'`,
			strings.TrimSpace(id), strings.TrimSpace(tenantID), kind, statusInProgress,
			strings.TrimSpace(claimedBy), strings.TrimSpace(agentKey), now,
		)
		if err != nil {
			return nil, err
		}
		if rows, _ := res.RowsAffected(); rows == 0 {
			continue
		}
		item, err := s.getItem(tenantID, id)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, nil
}

func (s *featureStore) relatedPending(tenantID string, primary *workflowItem, limit int) ([]workflowItem, error) {
	if primary == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	candidates, err := s.listItems(tenantID, primary.Kind, statusPending, 100)
	if err != nil {
		return nil, err
	}
	type scoredItem struct {
		item  workflowItem
		score int
	}
	scored := make([]scoredItem, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ID == primary.ID {
			continue
		}
		score := relatedItemScore(primary, &candidate)
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredItem{item: candidate, score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].item.CreatedAt.Before(scored[j].item.CreatedAt)
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	items := make([]workflowItem, 0, len(scored))
	for _, entry := range scored {
		items = append(items, entry.item)
	}
	return items, nil
}

func relatedItemScore(primary, candidate *workflowItem) int {
	if primary == nil || candidate == nil || primary.Kind != candidate.Kind {
		return 0
	}
	score := 0
	if sameNonEmpty(primary.Metadata["parent_item"], candidate.Metadata["parent_item"]) {
		score += 100
	}
	if sameNonEmpty(primary.Metadata["source_path"], candidate.Metadata["source_path"]) {
		score += 60
	}
	if sameNonEmpty(primary.Metadata["parent_source_path"], candidate.Metadata["parent_source_path"]) {
		score += 50
	}
	if sameNonEmpty(primary.Metadata["section"], candidate.Metadata["section"]) {
		score += 25
	}
	if lineDistance := absInt(metadataLine(primary.Metadata) - metadataLine(candidate.Metadata)); lineDistance > 0 && lineDistance <= 10 {
		score += 20 - lineDistance
	}
	return score
}

func sameNonEmpty(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	return a != "" && b != "" && a == b
}

func metadataLine(metadata map[string]string) int {
	for _, key := range []string{"source_line", "parent_source_line"} {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			var n int
			if _, err := fmt.Sscanf(value, "%d", &n); err == nil {
				return n
			}
		}
	}
	return 0
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func (s *featureStore) firstPending(tenantID, kind string) (*workflowItem, error) {
	if kind == kindBacklog {
		return s.firstPendingBacklog(tenantID)
	}
	rows, err := s.db.Query(`
		SELECT id, tenant_id, kind, status, body, source, channel, chat_id, local_key,
		       claimed_by, agent_key, result, metadata, created_at, updated_at
		FROM beta_skynet_workflow_items
		WHERE tenant_id=$1 AND kind=$2 AND status='pending'
		ORDER BY created_at ASC
		LIMIT 1`, strings.TrimSpace(tenantID), kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanWorkflowItem(rows)
}

func (s *featureStore) firstPendingBacklog(tenantID string) (*workflowItem, error) {
	now := time.Now().UTC()
	if err := s.boostBottomBacklogPrioritiesLocked(tenantID, now); err != nil {
		return nil, err
	}
	items, err := s.listPendingBacklog(tenantID, 1, now)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &items[0], nil
}

func (s *featureStore) getItem(tenantID, id string) (*workflowItem, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, kind, status, body, source, channel, chat_id, local_key,
		       claimed_by, agent_key, result, metadata, created_at, updated_at
		FROM beta_skynet_workflow_items
		WHERE tenant_id=$1 AND id=$2`, strings.TrimSpace(tenantID), strings.TrimSpace(id))
	item, err := scanWorkflowItem(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("workflow item not found")
	}
	return item, err
}

func (s *featureStore) listItems(tenantID, kind, status string, limit int) ([]workflowItem, error) {
	if !validKinds[kind] {
		return nil, fmt.Errorf("unsupported queue kind %q", kind)
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	status = strings.TrimSpace(status)
	if kind == kindBacklog && status == statusPending {
		return s.listPendingBacklog(tenantID, limit, time.Now().UTC())
	}

	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = s.db.Query(`
			SELECT id, tenant_id, kind, status, body, source, channel, chat_id, local_key,
			       claimed_by, agent_key, result, metadata, created_at, updated_at
			FROM beta_skynet_workflow_items
			WHERE tenant_id=$1 AND kind=$2
			ORDER BY created_at ASC
			LIMIT $3`, strings.TrimSpace(tenantID), kind, limit)
	} else {
		orderDirection := "DESC"
		if status == statusPending || status == statusInProgress || status == statusReview || status == statusRefinement {
			orderDirection = "ASC"
		}
		query := `
			SELECT id, tenant_id, kind, status, body, source, channel, chat_id, local_key,
			       claimed_by, agent_key, result, metadata, created_at, updated_at
			FROM beta_skynet_workflow_items
			WHERE tenant_id=$1 AND kind=$2 AND status=$3
			ORDER BY created_at ` + orderDirection + `
			LIMIT $4`
		rows, err = s.db.Query(query, strings.TrimSpace(tenantID), kind, status, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]workflowItem, 0)
	for rows.Next() {
		item, err := scanWorkflowItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (s *featureStore) listPendingBacklog(tenantID string, limit int, now time.Time) ([]workflowItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	items, err := s.pendingBacklogItems(tenantID)
	if err != nil {
		return nil, err
	}
	sortBacklogByPriority(items, now)
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *featureStore) pendingBacklogItems(tenantID string) ([]workflowItem, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, kind, status, body, source, channel, chat_id, local_key,
		       claimed_by, agent_key, result, metadata, created_at, updated_at
		FROM beta_skynet_workflow_items
		WHERE tenant_id=$1 AND kind=$2 AND status='pending'`,
		strings.TrimSpace(tenantID), kindBacklog)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]workflowItem, 0)
	for rows.Next() {
		item, err := scanWorkflowItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (s *featureStore) listRepoReferenceItems(tenantID string) ([]workflowItem, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, kind, status, body, source, channel, chat_id, local_key,
		       claimed_by, agent_key, result, metadata, created_at, updated_at
		FROM beta_skynet_workflow_items
		WHERE tenant_id=$1
		  AND status IN ('pending', 'in_progress', 'review', 'needs_refinement', 'failed')
		ORDER BY
		  CASE kind
		    WHEN 'change_request' THEN 1
		    WHEN 'backlog' THEN 2
		    WHEN 'experiment' THEN 3
		    WHEN 'qa' THEN 4
		    WHEN 'pr' THEN 5
		    ELSE 9
		  END,
		  CASE status
		    WHEN 'pending' THEN 1
		    WHEN 'in_progress' THEN 2
		    WHEN 'review' THEN 3
		    WHEN 'needs_refinement' THEN 4
		    WHEN 'failed' THEN 5
		    ELSE 9
		  END,
		  created_at ASC`, strings.TrimSpace(tenantID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]workflowItem, 0)
	for rows.Next() {
		item, err := scanWorkflowItem(rows)
		if err != nil {
			return nil, err
		}
		if !shouldMirrorRepoReference(*item) {
			continue
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (s *featureStore) updateStatus(tenantID, id, status, result string, metadata map[string]string) (*workflowItem, error) {
	existing, err := s.getItem(tenantID, id)
	if err != nil {
		return nil, err
	}
	merged := cloneStringMap(existing.Metadata)
	for key, value := range metadata {
		merged[key] = value
	}
	metaJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(`
		UPDATE beta_skynet_workflow_items
		SET status=$3, result=$4, metadata=$5, updated_at=$6
		WHERE tenant_id=$1 AND id=$2`,
		strings.TrimSpace(tenantID), strings.TrimSpace(id), strings.TrimSpace(status), strings.TrimSpace(result), string(metaJSON), now,
	)
	if err != nil {
		return nil, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return nil, fmt.Errorf("workflow item not found")
	}
	return s.getItem(tenantID, id)
}

func (s *featureStore) updateMetadata(tenantID, id string, metadata map[string]string) (*workflowItem, error) {
	existing, err := s.getItem(tenantID, id)
	if err != nil {
		return nil, err
	}
	merged := cloneStringMap(existing.Metadata)
	for key, value := range metadata {
		merged[key] = value
	}
	metaJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(`
		UPDATE beta_skynet_workflow_items
		SET metadata=$3, updated_at=$4
		WHERE tenant_id=$1 AND id=$2`,
		strings.TrimSpace(tenantID), strings.TrimSpace(id), string(metaJSON), now,
	)
	if err != nil {
		return nil, err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return nil, fmt.Errorf("workflow item not found")
	}
	return s.getItem(tenantID, id)
}

func (s *featureStore) counts(tenantID string) ([]queueCounts, error) {
	rows, err := s.db.Query(`
		SELECT kind, status, COUNT(*)
		FROM beta_skynet_workflow_items
		WHERE tenant_id=$1
		GROUP BY kind, status`, strings.TrimSpace(tenantID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byKind := map[string]*queueCounts{
		kindBacklog:    {Kind: kindBacklog},
		kindExperiment: {Kind: kindExperiment},
		kindQA:         {Kind: kindQA},
		kindPR:         {Kind: kindPR},
		kindChangeReq:  {Kind: kindChangeReq},
	}
	for rows.Next() {
		var kind, status string
		var count int
		if err := rows.Scan(&kind, &status, &count); err != nil {
			return nil, err
		}
		entry := byKind[kind]
		if entry == nil {
			entry = &queueCounts{Kind: kind}
			byKind[kind] = entry
		}
		switch status {
		case statusPending:
			entry.Pending = count
		case statusInProgress:
			entry.InProgress = count
		case statusReview:
			entry.Review = count
		case statusRefinement:
			entry.Refinement = count
		case statusDone:
			entry.Done = count
		case statusFailed:
			entry.Failed = count
		case statusDropped:
			entry.Dropped = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return []queueCounts{*byKind[kindChangeReq], *byKind[kindBacklog], *byKind[kindExperiment], *byKind[kindQA], *byKind[kindPR]}, nil
}

func sortBacklogByPriority(items []workflowItem, now time.Time) {
	sort.SliceStable(items, func(i, j int) bool {
		left := backlogPriorityScore(items[i], now)
		right := backlogPriorityScore(items[j], now)
		if left == right {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return left > right
	})
}

func (s *featureStore) boostBottomBacklogPrioritiesLocked(tenantID string, now time.Time) error {
	items, err := s.pendingBacklogItems(tenantID)
	if err != nil || len(items) < 2 {
		return err
	}
	sort.SliceStable(items, func(i, j int) bool {
		left := backlogPriorityScore(items[i], now)
		right := backlogPriorityScore(items[j], now)
		if left == right {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return left < right
	})

	boostCount := len(items) / 5
	if boostCount < 1 {
		boostCount = 1
	}
	for _, item := range items[:boostCount] {
		if now.Sub(item.CreatedAt) < backlogPriorityAgingInterval {
			continue
		}
		if boostedAt, ok := parsePriorityBoostedAt(item.Metadata["priority_boosted_at"]); ok && now.Sub(boostedAt) < backlogPriorityAgingInterval {
			continue
		}
		currentBoost := backlogPriorityBoost(item.Metadata)
		if currentBoost >= backlogPriorityPersistentBoost {
			continue
		}
		nextBoost := currentBoost + backlogPriorityAgingStep
		if nextBoost > backlogPriorityPersistentBoost {
			nextBoost = backlogPriorityPersistentBoost
		}
		metadata := cloneStringMap(item.Metadata)
		metadata["priority_boost"] = strconv.Itoa(nextBoost)
		metadata["priority_boosted_at"] = now.Format(time.RFC3339)
		metadata["priority_last_boost_reason"] = "bottom_queue_aging"
		if _, err := s.updateMetadata(tenantID, item.ID, metadata); err != nil {
			return err
		}
	}
	return nil
}

func backlogPriorityScore(item workflowItem, now time.Time) int {
	return backlogPriorityBase(item.Metadata) + backlogPriorityBoost(item.Metadata) + backlogPriorityAgeBoost(item, now)
}

func backlogPriorityBase(metadata map[string]string) int {
	if priority, ok := parseBacklogPriority(metadata["priority"]); ok {
		return priority
	}
	return backlogPriorityDefault
}

func backlogPriorityBoost(metadata map[string]string) int {
	boost, err := strconv.Atoi(strings.TrimSpace(metadata["priority_boost"]))
	if err != nil {
		return 0
	}
	if boost < 0 {
		return 0
	}
	if boost > backlogPriorityPersistentBoost {
		return backlogPriorityPersistentBoost
	}
	return boost
}

func backlogPriorityAgeBoost(item workflowItem, now time.Time) int {
	if item.CreatedAt.IsZero() || !now.After(item.CreatedAt) {
		return 0
	}
	boost := int(now.Sub(item.CreatedAt) / backlogPriorityAgingInterval * backlogPriorityAgingStep)
	if boost < 0 {
		return 0
	}
	if boost > backlogPriorityAgeBoostMax {
		return backlogPriorityAgeBoostMax
	}
	return boost
}

func parseBacklogPriority(value string) (int, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return 0, false
	}
	switch value {
	case "critical", "urgent", "highest":
		return 100, true
	case "high":
		return 80, true
	case "normal", "default", "medium":
		return 50, true
	case "low":
		return 25, true
	case "lowest":
		return 0, true
	}
	if strings.HasPrefix(value, "p") && len(value) == 2 {
		switch value {
		case "p0":
			return 100, true
		case "p1":
			return 80, true
		case "p2":
			return 60, true
		case "p3":
			return 40, true
		case "p4":
			return 20, true
		}
	}
	priority, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	if priority < backlogPriorityMin {
		priority = backlogPriorityMin
	}
	if priority > backlogPriorityMax {
		priority = backlogPriorityMax
	}
	return priority, true
}

func parsePriorityBoostedAt(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts, true
	}
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts, true
	}
	return time.Time{}, false
}

func scanWorkflowItem(row itemScanner) (*workflowItem, error) {
	var item workflowItem
	var metadataJSON string
	var createdAt flexibleTime
	var updatedAt flexibleTime
	err := row.Scan(
		&item.ID, &item.TenantID, &item.Kind, &item.Status, &item.Body, &item.Source,
		&item.Channel, &item.ChatID, &item.LocalKey, &item.ClaimedBy, &item.AgentKey,
		&item.Result, &metadataJSON, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(metadataJSON), &item.Metadata)
	if item.Metadata == nil {
		item.Metadata = map[string]string{}
	}
	item.CreatedAt = createdAt.Time
	item.UpdatedAt = updatedAt.Time
	return &item, nil
}
