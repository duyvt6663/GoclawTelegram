package eaterychat

import "time"

type EateryEntry struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id,omitempty"`
	SharedEntryID string    `json:"shared_entry_id,omitempty"`
	Name          string    `json:"name"`
	Address       string    `json:"address,omitempty"`
	MapLink       string    `json:"map_link,omitempty"`
	District      string    `json:"district,omitempty"`
	Category      string    `json:"category,omitempty"`
	PriceHint     string    `json:"price_hint,omitempty"`
	BudgetMin     int       `json:"budget_min,omitempty"`
	BudgetMax     int       `json:"budget_max,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	Notes         string    `json:"notes,omitempty"`
	SourceText    string    `json:"source_text,omitempty"`
	Confidence    float64   `json:"confidence"`
	CreatedBy     string    `json:"created_by,omitempty"`
	SourceChannel string    `json:"source_channel,omitempty"`
	SourceChatID  string    `json:"source_chat_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ParsedEatery struct {
	Name       string   `json:"name,omitempty"`
	Address    string   `json:"address,omitempty"`
	MapLink    string   `json:"map_link,omitempty"`
	District   string   `json:"district,omitempty"`
	Category   string   `json:"category,omitempty"`
	PriceHint  string   `json:"price_hint,omitempty"`
	BudgetMin  int      `json:"budget_min,omitempty"`
	BudgetMax  int      `json:"budget_max,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Notes      string   `json:"notes,omitempty"`
	SourceText string   `json:"source_text,omitempty"`
	Confidence float64  `json:"confidence"`
	Reasons    []string `json:"reasons,omitempty"`
}

type PendingSuggestion struct {
	ID        string       `json:"id"`
	TenantID  string       `json:"tenant_id,omitempty"`
	Parsed    ParsedEatery `json:"parsed"`
	CreatedBy string       `json:"created_by,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	ExpiresAt time.Time    `json:"expires_at"`
}

type IngestRequest struct {
	Text        string   `json:"text"`
	Confirm     bool     `json:"confirm,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Contributor string   `json:"contributor,omitempty"`
}

type IngestResult struct {
	Entry              *EateryEntry       `json:"entry,omitempty"`
	Created            bool               `json:"created"`
	DuplicateDetected  bool               `json:"duplicate_detected,omitempty"`
	RequiresConfirm    bool               `json:"requires_confirmation,omitempty"`
	SuggestionID        string             `json:"suggestion_id,omitempty"`
	Suggestion          *PendingSuggestion `json:"suggestion,omitempty"`
	Parsed             *ParsedEatery      `json:"parsed,omitempty"`
	ConfidenceThreshold float64            `json:"confidence_threshold"`
	Message             string             `json:"message,omitempty"`
}

type EateryOverrides struct {
	Name      string   `json:"name,omitempty"`
	Address   string   `json:"address,omitempty"`
	MapLink   string   `json:"map_link,omitempty"`
	District  string   `json:"district,omitempty"`
	Category  string   `json:"category,omitempty"`
	PriceHint string   `json:"price_hint,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Notes     string   `json:"notes,omitempty"`
}

type ConfirmRequest struct {
	SuggestionID string          `json:"suggestion_id"`
	Overrides    EateryOverrides `json:"overrides,omitempty"`
}

type RecommendationConstraints struct {
	Prompt    string   `json:"prompt,omitempty"`
	District  string   `json:"district,omitempty"`
	Category  string   `json:"category,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	MaxBudget int      `json:"max_budget,omitempty"`
	GroupSize int      `json:"group_size,omitempty"`
	Search    string   `json:"search,omitempty"`
	Limit     int      `json:"limit"`
}

type RecommendRequest struct {
	Prompt    string   `json:"prompt"`
	District  string   `json:"district,omitempty"`
	Category  string   `json:"category,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	MaxBudget int      `json:"max_budget,omitempty"`
	GroupSize int      `json:"group_size,omitempty"`
	Search    string   `json:"search,omitempty"`
	Limit     int      `json:"limit,omitempty"`
}

type Recommendation struct {
	Entry   EateryEntry `json:"entry"`
	Score   int         `json:"score"`
	Reasons []string    `json:"reasons,omitempty"`
}

type RecommendResult struct {
	Constraints RecommendationConstraints `json:"constraints"`
	Suggestions []Recommendation          `json:"suggestions"`
	Count       int                       `json:"count"`
}

type ListRequest struct {
	District  string `json:"district,omitempty"`
	Category  string `json:"category,omitempty"`
	Tag       string `json:"tag,omitempty"`
	Search    string `json:"search,omitempty"`
	MaxBudget int    `json:"max_budget,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type ListResult struct {
	Entries []EateryEntry `json:"entries"`
	Count   int           `json:"count"`
	Filters ListRequest   `json:"filters"`
}

type sourceMeta struct {
	Channel          string
	ChatID           string
	ContributorID    string
	ContributorLabel string
}
