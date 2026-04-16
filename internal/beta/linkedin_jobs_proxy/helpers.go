package linkedinjobsproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	featureName          = "linkedin_jobs_proxy"
	searchProviderLinkup = "linkup"
	searchCacheTTL       = 24 * time.Hour
	previewCacheTTL      = 24 * time.Hour
	searchStatusOK       = "success"
	searchStatusFail     = "failed"
	defaultMaxResults    = 16
	maxMaxResults        = 40
	defaultTopNPerQuery  = 8
	maxTopNPerQuery      = 12
	defaultSearchIntent  = "AI engineer remote"
)

type SearchRequest struct {
	Query           string `json:"query"`
	MaxResults      int    `json:"max_results,omitempty"`
	TopNPerQuery    int    `json:"top_n_per_query,omitempty"`
	HardTitleFilter bool   `json:"hard_title_filter,omitempty"`
	RemoteOnly      bool   `json:"remote_only,omitempty"`
}

type resolvedSearchRequest struct {
	Query           string
	MaxResults      int
	TopNPerQuery    int
	HardTitleFilter bool
	RemoteOnly      bool
	LookupKey       string
	Queries         []string
}

type SearchPayload struct {
	Query       string       `json:"query"`
	Queries     []string     `json:"queries,omitempty"`
	Provider    string       `json:"provider"`
	Cached      bool         `json:"cached"`
	RetrievedAt time.Time    `json:"retrieved_at"`
	Jobs        []JobPreview `json:"jobs,omitempty"`
	Warnings    []string     `json:"warnings,omitempty"`
}

type JobPreview struct {
	URL           string     `json:"url"`
	Title         string     `json:"title"`
	Company       string     `json:"company"`
	Location      string     `json:"location,omitempty"`
	Snippet       string     `json:"snippet,omitempty"`
	Description   string     `json:"description,omitempty"`
	PostedAt      *time.Time `json:"posted_at,omitempty"`
	RoleType      string     `json:"role_type"`
	Score         float64    `json:"score"`
	SemanticScore float64    `json:"semantic_score"`
	RecencyScore  float64    `json:"recency_score"`
}

var (
	hardTitlePhrases = []string{
		"ai", "machine learning", "ml", "llm", "nlp", "computer vision",
	}
	excludedPhrases = []string{
		"frontend", "front end", "backend", "back end", "fullstack", "full stack",
		"devops", "support", "manager", "product", "qa", "quality assurance",
	}
	aiRolePhrases = []string{
		"ai engineer", "applied ai engineer", "applied ai", "llm engineer",
		"generative ai engineer", "genai engineer",
	}
	mlRolePhrases = []string{
		"machine learning engineer", "ml engineer", "nlp engineer", "computer vision engineer",
		"deep learning engineer", "ml scientist",
	}
	intentNoiseTokens = map[string]struct{}{
		"ai": {}, "applied": {}, "computer": {}, "engineer": {}, "engineering": {}, "job": {}, "jobs": {},
		"learning": {}, "llm": {}, "machine": {}, "ml": {}, "nlp": {}, "remote": {}, "role": {}, "vision": {},
	}
)

func tenantKey(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return strings.TrimSpace(id.String())
}

func tenantKeyFromCtx(ctx context.Context) string {
	return tenantKey(storepkg.TenantIDFromContext(ctx))
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return normalizeQuery(value)
}

func intArg(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func boolArg(args map[string]any, key string) (bool, bool) {
	value, ok := args[key].(bool)
	return value, ok
}

func normalizeQuery(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

type searchInputError struct {
	message string
}

func (e *searchInputError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func newSearchInputError(message string) error {
	return &searchInputError{message: strings.TrimSpace(message)}
}

func isSearchInputError(err error) bool {
	var target *searchInputError
	return errors.As(err, &target)
}

func normalizeSearchRequest(request SearchRequest) (resolvedSearchRequest, error) {
	query := normalizeQuery(request.Query)
	if query == "" {
		return resolvedSearchRequest{}, newSearchInputError("query is required")
	}

	maxResults, err := normalizeMaxResults(request.MaxResults)
	if err != nil {
		return resolvedSearchRequest{}, err
	}
	topN, err := normalizeTopNPerQuery(request.TopNPerQuery)
	if err != nil {
		return resolvedSearchRequest{}, err
	}

	remoteOnly := request.RemoteOnly || hasAnyPhrase(normalizeComparableText(query), "remote", "anywhere", "worldwide")
	resolved := resolvedSearchRequest{
		Query:           query,
		MaxResults:      maxResults,
		TopNPerQuery:    topN,
		HardTitleFilter: request.HardTitleFilter,
		RemoteOnly:      remoteOnly,
	}
	resolved.Queries = buildSearchQueries(resolved.Query, resolved.RemoteOnly)
	resolved.LookupKey = fmt.Sprintf(
		"%d|%d|%t|%t|%s",
		resolved.MaxResults,
		resolved.TopNPerQuery,
		resolved.HardTitleFilter,
		resolved.RemoteOnly,
		strings.ToLower(resolved.Query),
	)
	return resolved, nil
}

func normalizeMaxResults(value int) (int, error) {
	switch {
	case value == 0:
		return defaultMaxResults, nil
	case value < 0:
		return 0, newSearchInputError("max_results must be greater than 0")
	case value > maxMaxResults:
		return 0, newSearchInputError(fmt.Sprintf("max_results must be between 1 and %d", maxMaxResults))
	default:
		return value, nil
	}
}

func normalizeTopNPerQuery(value int) (int, error) {
	switch {
	case value == 0:
		return defaultTopNPerQuery, nil
	case value < 0:
		return 0, newSearchInputError("top_n_per_query must be greater than 0")
	case value > maxTopNPerQuery:
		return 0, newSearchInputError(fmt.Sprintf("top_n_per_query must be between 1 and %d", maxTopNPerQuery))
	default:
		return value, nil
	}
}

func buildSearchQueries(query string, remoteOnly bool) []string {
	query = normalizeQuery(query)
	if query == "" {
		query = defaultSearchIntent
	}
	if strings.Contains(strings.ToLower(query), "linkedin.com/jobs") {
		return []string{query}
	}

	tail := strings.Join(selectIntentKeywords(query), " ")
	remoteSuffix := ""
	if remoteOnly {
		remoteSuffix = " remote"
	}

	queries := []string{
		strings.TrimSpace(`site:linkedin.com/jobs ("AI Engineer" OR "Machine Learning Engineer" OR "ML Engineer" OR "LLM Engineer" OR "NLP Engineer" OR "Computer Vision Engineer")` + remoteSuffix + " " + tail),
		strings.TrimSpace(`site:linkedin.com/jobs ("Applied AI Engineer" OR "Generative AI Engineer" OR "Machine Learning Engineer")` + remoteSuffix + " " + tail),
		strings.TrimSpace(`site:linkedin.com/jobs ("AI Engineer" OR "ML Engineer" OR "LLM Engineer" OR "NLP Engineer" OR "Computer Vision Engineer")` + remoteSuffix + " " + tail),
	}
	return dedupeStrings(queries)
}

func selectIntentKeywords(query string) []string {
	tokens := tokenizeComparableText(query)
	if len(tokens) == 0 {
		return nil
	}
	out := make([]string, 0, 4)
	for _, token := range tokens {
		if _, skip := intentNoiseTokens[token]; skip {
			continue
		}
		out = append(out, token)
		if len(out) == 4 {
			break
		}
	}
	return out
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeQuery(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func cleanText(value string) string {
	value = strings.ReplaceAll(value, "\u00a0", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\t", " ")
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func trimRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "..."
}

func normalizeComparableText(value string) string {
	value = strings.ToLower(cleanText(value))
	var b strings.Builder
	lastSpace := true
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func tokenizeComparableText(value string) []string {
	value = normalizeComparableText(value)
	if value == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, token := range strings.Fields(value) {
		if len(token) < 2 {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func containsPhrase(normalizedText, phrase string) bool {
	normalizedText = strings.TrimSpace(normalizedText)
	phrase = normalizeComparableText(phrase)
	if normalizedText == "" || phrase == "" {
		return false
	}
	return strings.Contains(" "+normalizedText+" ", " "+phrase+" ")
}

func hasAnyPhrase(normalizedText string, phrases ...string) bool {
	for _, phrase := range phrases {
		if containsPhrase(normalizedText, phrase) {
			return true
		}
	}
	return false
}

func canonicalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "ref" || lower == "source" || lower == "fbclid" || lower == "gclid" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Host = strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	return parsed.String()
}

func canonicalizeLinkedInURL(raw string) string {
	raw = canonicalizeURL(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func isLinkedInJobURL(raw string) bool {
	parsed, err := url.Parse(canonicalizeLinkedInURL(raw))
	if err != nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")
	path := strings.ToLower(parsed.EscapedPath())
	if !strings.Contains(host, "linkedin.com") {
		return false
	}
	return strings.Contains(path, "/jobs/")
}

func makeURLHash(raw string) string {
	sum := sha256.Sum256([]byte(canonicalizeLinkedInURL(raw)))
	return hex.EncodeToString(sum[:])
}

func buildTitleCompanyKey(title, company string) string {
	return normalizeComparableText(title) + "|" + normalizeComparableText(company)
}

func scoreIntentMatch(query string, job JobPreview) float64 {
	queryTokens := tokenizeComparableText(query)
	if len(queryTokens) == 0 {
		queryTokens = tokenizeComparableText(defaultSearchIntent)
	}
	if len(queryTokens) == 0 {
		return 0.2
	}

	querySet := make(map[string]struct{}, len(queryTokens))
	for _, token := range queryTokens {
		querySet[token] = struct{}{}
	}

	titleTokens := tokenizeComparableText(job.Title)
	bodyTokens := tokenizeComparableText(strings.Join([]string{job.Company, job.Location, job.Snippet, job.Description}, " "))
	titleMatches := 0
	totalMatches := 0
	for _, token := range titleTokens {
		if _, ok := querySet[token]; ok {
			titleMatches++
			totalMatches++
		}
	}
	for _, token := range bodyTokens {
		if _, ok := querySet[token]; ok {
			totalMatches++
		}
	}

	queryNorm := normalizeComparableText(query)
	roleBoost := 0.12
	switch job.RoleType {
	case "ai_engineer":
		if hasAnyPhrase(queryNorm, "ai", "llm", "applied ai", "generative ai") {
			roleBoost = 0.38
		} else {
			roleBoost = 0.24
		}
	case "ml_engineer":
		if hasAnyPhrase(queryNorm, "machine learning", "ml", "nlp", "computer vision") {
			roleBoost = 0.38
		} else {
			roleBoost = 0.24
		}
	}

	locationBoost := 0.0
	if hasAnyPhrase(queryNorm, "remote") && hasAnyPhrase(normalizeComparableText(job.Location+" "+job.Snippet), "remote", "anywhere", "worldwide") {
		locationBoost = 0.18
	}

	overlap := float64(totalMatches) / float64(len(querySet))
	titleStrength := float64(titleMatches) / float64(len(querySet))
	return math.Min(2.25, overlap*1.15+titleStrength*0.95+roleBoost+locationBoost)
}

func computeRecencyScore(now time.Time, postedAt *time.Time) float64 {
	if postedAt == nil {
		return 0.08
	}
	ageHours := now.Sub(*postedAt).Hours()
	switch {
	case ageHours <= 48:
		return 0.42
	case ageHours <= 7*24:
		return 0.28
	case ageHours <= 14*24:
		return 0.16
	default:
		return 0.05
	}
}

func betterJobPreview(left, right JobPreview) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	switch {
	case left.PostedAt == nil && right.PostedAt == nil:
		return left.Title < right.Title
	case left.PostedAt == nil:
		return false
	case right.PostedAt == nil:
		return true
	default:
		return left.PostedAt.After(*right.PostedAt)
	}
}

func dedupePreviewJobs(jobs []JobPreview) []JobPreview {
	if len(jobs) == 0 {
		return nil
	}

	byURL := make(map[string]JobPreview, len(jobs))
	for _, job := range jobs {
		key := makeURLHash(job.URL)
		existing, ok := byURL[key]
		if !ok || betterJobPreview(job, existing) {
			byURL[key] = job
		}
	}

	byTitleCompany := make(map[string]JobPreview, len(byURL))
	for _, job := range byURL {
		key := buildTitleCompanyKey(job.Title, job.Company)
		if key == "|" {
			key = makeURLHash(job.URL)
		}
		existing, ok := byTitleCompany[key]
		if !ok || betterJobPreview(job, existing) {
			byTitleCompany[key] = job
		}
	}

	out := make([]JobPreview, 0, len(byTitleCompany))
	for _, job := range byTitleCompany {
		out = append(out, job)
	}
	return out
}

func humanizeAge(now, then time.Time) string {
	delta := now.Sub(then)
	switch {
	case delta < time.Hour:
		minutes := int(delta.Minutes())
		if minutes < 1 {
			minutes = 1
		}
		return fmt.Sprintf("%dm ago", minutes)
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(delta.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(delta.Hours()/24))
	}
}

func formatToolPayload(payload *SearchPayload) string {
	if payload == nil {
		return tools.WrapExternalContent("No LinkedIn proxy results were returned.", "LinkedIn Jobs Proxy", false)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("LinkedIn proxy results for: %s\n\n", payload.Query))

	meta := []string{fmt.Sprintf("provider=%s", payload.Provider)}
	if payload.Cached {
		meta = append(meta, "cached=true")
	}
	if len(payload.Queries) > 0 {
		meta = append(meta, fmt.Sprintf("queries=%d", len(payload.Queries)))
	}
	sb.WriteString(strings.Join(meta, " | "))
	sb.WriteString("\n\n")

	if len(payload.Jobs) == 0 {
		sb.WriteString("No filtered LinkedIn job previews matched the request.")
		return tools.WrapExternalContent(sb.String(), "LinkedIn Jobs Proxy", false)
	}

	for i, job := range payload.Jobs {
		sb.WriteString(fmt.Sprintf("%d. %s at %s\n", i+1, job.Title, job.Company))
		if job.Location != "" {
			sb.WriteString(fmt.Sprintf("   Location: %s\n", job.Location))
		}
		if job.RoleType != "" {
			sb.WriteString(fmt.Sprintf("   Role: %s\n", job.RoleType))
		}
		if job.PostedAt != nil {
			sb.WriteString(fmt.Sprintf("   Posted: %s\n", humanizeAge(time.Now().UTC(), *job.PostedAt)))
		}
		sb.WriteString(fmt.Sprintf("   %s\n", job.URL))
		if job.Snippet != "" {
			sb.WriteString(fmt.Sprintf("   %s\n", trimRunes(job.Snippet, 220)))
		}
		if i < len(payload.Jobs)-1 {
			sb.WriteByte('\n')
		}
	}
	return tools.WrapExternalContent(sb.String(), "LinkedIn Jobs Proxy", false)
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
