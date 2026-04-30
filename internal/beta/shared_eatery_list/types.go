package sharedeaterylist

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type EateryLocation struct {
	Address string `json:"address,omitempty"`
	MapLink string `json:"map_link,omitempty"`
}

type EateryContributor struct {
	ID    string `json:"id,omitempty"`
	Label string `json:"label,omitempty"`
}

type EateryEntry struct {
	ID            string            `json:"id"`
	TenantID      string            `json:"tenant_id,omitempty"`
	Name          string            `json:"name"`
	Location      EateryLocation    `json:"location"`
	District      string            `json:"district,omitempty"`
	Category      string            `json:"category,omitempty"`
	MustTryDishes []string          `json:"must_try_dishes,omitempty"`
	Contributor   EateryContributor `json:"contributor,omitempty"`
	Notes         string            `json:"notes,omitempty"`
	PriceRange    string            `json:"price_range,omitempty"`
	ImageURLs     []string          `json:"image_urls,omitempty"`
	SourceChannel string            `json:"source_channel,omitempty"`
	SourceChatID  string            `json:"source_chat_id,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

type EateryInput struct {
	Name             string         `json:"name"`
	Location         EateryLocation `json:"location,omitempty"`
	Address          string         `json:"address,omitempty"`
	MapLink          string         `json:"map_link,omitempty"`
	District         string         `json:"district,omitempty"`
	Category         string         `json:"category,omitempty"`
	MustTryDishes    stringList     `json:"must_try_dishes,omitempty"`
	Contributor      string         `json:"contributor,omitempty"`
	ContributorID    string         `json:"contributor_id,omitempty"`
	ContributorLabel string         `json:"contributor_label,omitempty"`
	Notes            string         `json:"notes,omitempty"`
	PriceRange       string         `json:"price_range,omitempty"`
	ImageURLs        stringList     `json:"image_urls,omitempty"`
	SourceChannel    string         `json:"source_channel,omitempty"`
	SourceChatID     string         `json:"source_chat_id,omitempty"`
}

type EateryFilter struct {
	District   string `json:"district,omitempty"`
	Category   string `json:"category,omitempty"`
	PriceRange string `json:"price_range,omitempty"`
	Search     string `json:"search,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type AddEateryResult struct {
	Entry             *EateryEntry `json:"entry,omitempty"`
	Created           bool         `json:"created"`
	DuplicateDetected bool         `json:"duplicate_detected,omitempty"`
}

type ListEateriesResult struct {
	Entries []EateryEntry `json:"entries"`
	Count   int           `json:"count"`
	Filters EateryFilter  `json:"filters"`
}

type RandomEateryResult struct {
	Entry   *EateryEntry `json:"entry,omitempty"`
	Filters EateryFilter `json:"filters"`
}

type sourceMeta struct {
	Channel          string
	ChatID           string
	ContributorID    string
	ContributorLabel string
}

type stringList []string

func (l *stringList) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*l = nil
		return nil
	}

	var values []string
	if err := json.Unmarshal(data, &values); err == nil {
		*l = normalizeStringList(values)
		return nil
	}

	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*l = splitStringList(single)
		return nil
	}

	return fmt.Errorf("must be a string or array of strings")
}

func (in EateryInput) normalizedLocation() EateryLocation {
	location := in.Location
	if strings.TrimSpace(location.Address) == "" {
		location.Address = in.Address
	}
	if strings.TrimSpace(location.MapLink) == "" {
		location.MapLink = in.MapLink
	}
	location.Address = cleanText(location.Address)
	location.MapLink = strings.TrimSpace(location.MapLink)
	return location
}
