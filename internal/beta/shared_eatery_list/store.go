package sharedeaterylist

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var errEateryNotFound = errors.New("eatery not found")

type storedEatery struct {
	ID               string
	TenantID         string
	Name             string
	NameKey          string
	Location         EateryLocation
	LocationKey      string
	District         string
	DistrictKey      string
	Category         string
	CategoryKey      string
	MustTryDishes    []string
	ContributorID    string
	ContributorLabel string
	Notes            string
	PriceRange       string
	PriceRangeKey    string
	ImageURLs        []string
	SourceChannel    string
	SourceChatID     string
	SearchKey        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
		CREATE TABLE IF NOT EXISTS beta_shared_eatery_list_entries (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			name_key TEXT NOT NULL,
			address TEXT NOT NULL DEFAULT '',
			map_link TEXT NOT NULL DEFAULT '',
			location_key TEXT NOT NULL,
			district TEXT NOT NULL DEFAULT '',
			district_key TEXT NOT NULL DEFAULT '',
			category TEXT NOT NULL DEFAULT '',
			category_key TEXT NOT NULL DEFAULT '',
			must_try_dishes_json TEXT NOT NULL DEFAULT '[]',
			contributor_id TEXT NOT NULL DEFAULT '',
			contributor_label TEXT NOT NULL DEFAULT '',
			notes TEXT NOT NULL DEFAULT '',
			price_range TEXT NOT NULL DEFAULT '',
			price_range_key TEXT NOT NULL DEFAULT '',
			image_urls_json TEXT NOT NULL DEFAULT '[]',
			source_channel TEXT NOT NULL DEFAULT '',
			source_chat_id TEXT NOT NULL DEFAULT '',
			search_key TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (tenant_id, name_key, location_key)
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_beta_shared_eatery_list_filter ON beta_shared_eatery_list_entries(tenant_id, district_key, category_key, price_range_key)`,
		`CREATE INDEX IF NOT EXISTS idx_beta_shared_eatery_list_created ON beta_shared_eatery_list_entries(tenant_id, created_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *featureStore) addEatery(tenantID string, input storedEatery) (*EateryEntry, bool, error) {
	tenantID = strings.TrimSpace(tenantID)
	input.TenantID = tenantID

	if duplicate, err := s.findDuplicate(tenantID, input.NameKey, input.LocationKey); err != nil {
		return nil, false, err
	} else if duplicate != nil {
		return duplicate, false, nil
	}

	now := time.Now().UTC()
	input.ID = uuid.NewString()
	input.CreatedAt = now
	input.UpdatedAt = now

	mustTryJSON, err := json.Marshal(input.MustTryDishes)
	if err != nil {
		return nil, false, err
	}
	imageURLsJSON, err := json.Marshal(input.ImageURLs)
	if err != nil {
		return nil, false, err
	}

	_, err = s.db.Exec(`
		INSERT INTO beta_shared_eatery_list_entries (
			id, tenant_id, name, name_key, address, map_link, location_key,
			district, district_key, category, category_key, must_try_dishes_json,
			contributor_id, contributor_label, notes, price_range, price_range_key,
			image_urls_json, source_channel, source_chat_id, search_key, created_at, updated_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17,
			$18, $19, $20, $21, $22, $23
		)`,
		input.ID,
		input.TenantID,
		input.Name,
		input.NameKey,
		input.Location.Address,
		input.Location.MapLink,
		input.LocationKey,
		input.District,
		input.DistrictKey,
		input.Category,
		input.CategoryKey,
		string(mustTryJSON),
		input.ContributorID,
		input.ContributorLabel,
		input.Notes,
		input.PriceRange,
		input.PriceRangeKey,
		string(imageURLsJSON),
		input.SourceChannel,
		input.SourceChatID,
		input.SearchKey,
		input.CreatedAt,
		input.UpdatedAt,
	)
	if err != nil {
		if duplicate, dupErr := s.findDuplicate(tenantID, input.NameKey, input.LocationKey); dupErr == nil && duplicate != nil {
			return duplicate, false, nil
		}
		return nil, false, err
	}

	entry := input.toEntry()
	return &entry, true, nil
}

func (s *featureStore) getEatery(tenantID, id string) (*EateryEntry, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, name, name_key, address, map_link, location_key,
		       district, district_key, category, category_key, must_try_dishes_json,
		       contributor_id, contributor_label, notes, price_range, price_range_key,
		       image_urls_json, source_channel, source_chat_id, search_key, created_at, updated_at
		FROM beta_shared_eatery_list_entries
		WHERE tenant_id=$1 AND id=$2`,
		strings.TrimSpace(tenantID),
		strings.TrimSpace(id),
	)
	return scanEatery(row)
}

func (s *featureStore) findDuplicate(tenantID, nameKey, locationKey string) (*EateryEntry, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, name, name_key, address, map_link, location_key,
		       district, district_key, category, category_key, must_try_dishes_json,
		       contributor_id, contributor_label, notes, price_range, price_range_key,
		       image_urls_json, source_channel, source_chat_id, search_key, created_at, updated_at
		FROM beta_shared_eatery_list_entries
		WHERE tenant_id=$1 AND name_key=$2 AND location_key=$3
		LIMIT 1`,
		strings.TrimSpace(tenantID),
		strings.TrimSpace(nameKey),
		strings.TrimSpace(locationKey),
	)
	entry, err := scanEatery(row)
	if err == errEateryNotFound {
		return nil, nil
	}
	return entry, err
}

func (s *featureStore) listEateries(tenantID string, filter EateryFilter) ([]EateryEntry, error) {
	filter = normalizeFilter(filter)
	query, args := buildFilterQuery(`
		SELECT id, tenant_id, name, name_key, address, map_link, location_key,
		       district, district_key, category, category_key, must_try_dishes_json,
		       contributor_id, contributor_label, notes, price_range, price_range_key,
		       image_urls_json, source_channel, source_chat_id, search_key, created_at, updated_at
		FROM beta_shared_eatery_list_entries`,
		tenantID,
		filter,
	)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args)+1)
	args = append(args, filter.Limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]EateryEntry, 0)
	for rows.Next() {
		entry, err := scanEatery(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, *entry)
	}
	return entries, rows.Err()
}

func (s *featureStore) randomEatery(tenantID string, filter EateryFilter) (*EateryEntry, error) {
	filter = normalizeFilter(filter)
	query, args := buildFilterQuery(`
		SELECT id, tenant_id, name, name_key, address, map_link, location_key,
		       district, district_key, category, category_key, must_try_dishes_json,
		       contributor_id, contributor_label, notes, price_range, price_range_key,
		       image_urls_json, source_channel, source_chat_id, search_key, created_at, updated_at
		FROM beta_shared_eatery_list_entries`,
		tenantID,
		filter,
	)
	query += " ORDER BY RANDOM() LIMIT 1"

	entry, err := scanEatery(s.db.QueryRow(query, args...))
	if err == errEateryNotFound {
		return nil, nil
	}
	return entry, err
}

func buildFilterQuery(base, tenantID string, filter EateryFilter) (string, []any) {
	args := []any{strings.TrimSpace(tenantID)}
	clauses := []string{"tenant_id=$1"}

	if key := normalizeComparableText(filter.District); key != "" {
		args = append(args, key)
		clauses = append(clauses, fmt.Sprintf("district_key=$%d", len(args)))
	}
	if key := normalizeComparableText(filter.Category); key != "" {
		args = append(args, key)
		clauses = append(clauses, fmt.Sprintf("category_key=$%d", len(args)))
	}
	if key := normalizeComparableText(filter.PriceRange); key != "" {
		args = append(args, key)
		clauses = append(clauses, fmt.Sprintf("price_range_key=$%d", len(args)))
	}
	if key := normalizeComparableText(filter.Search); key != "" {
		args = append(args, "%"+key+"%")
		clauses = append(clauses, fmt.Sprintf("search_key LIKE $%d", len(args)))
	}

	return base + " WHERE " + strings.Join(clauses, " AND "), args
}

func scanEatery(row scanner) (*EateryEntry, error) {
	var record storedEatery
	var mustTryJSON string
	var imageURLsJSON string
	err := row.Scan(
		&record.ID,
		&record.TenantID,
		&record.Name,
		&record.NameKey,
		&record.Location.Address,
		&record.Location.MapLink,
		&record.LocationKey,
		&record.District,
		&record.DistrictKey,
		&record.Category,
		&record.CategoryKey,
		&mustTryJSON,
		&record.ContributorID,
		&record.ContributorLabel,
		&record.Notes,
		&record.PriceRange,
		&record.PriceRangeKey,
		&imageURLsJSON,
		&record.SourceChannel,
		&record.SourceChatID,
		&record.SearchKey,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errEateryNotFound
		}
		return nil, err
	}
	record.MustTryDishes = decodeStringListJSON(mustTryJSON)
	record.ImageURLs = decodeStringListJSON(imageURLsJSON)
	entry := record.toEntry()
	return &entry, nil
}

func decodeStringListJSON(value string) []string {
	var out []string
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		return nil
	}
	return normalizeStringList(out)
}

func (e storedEatery) toEntry() EateryEntry {
	return EateryEntry{
		ID:            e.ID,
		TenantID:      e.TenantID,
		Name:          e.Name,
		Location:      e.Location,
		District:      e.District,
		Category:      e.Category,
		MustTryDishes: e.MustTryDishes,
		Contributor: EateryContributor{
			ID:    e.ContributorID,
			Label: e.ContributorLabel,
		},
		Notes:         e.Notes,
		PriceRange:    e.PriceRange,
		ImageURLs:     e.ImageURLs,
		SourceChannel: e.SourceChannel,
		SourceChatID:  e.SourceChatID,
		CreatedAt:     e.CreatedAt,
		UpdatedAt:     e.UpdatedAt,
	}
}
