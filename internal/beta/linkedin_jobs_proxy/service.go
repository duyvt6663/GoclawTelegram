package linkedinjobsproxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"
)

type Service struct {
	store         *featureStore
	client        *searchProxyClient
	previewClient *http.Client
}

type rawSearchCandidate struct {
	SearchQuery string
	SearchTitle string
	URL         string
	Snippet     string
}

type previewMetadata struct {
	Title       string
	Company     string
	Location    string
	Snippet     string
	Description string
	PostedAt    *time.Time
}

func NewService(db *sql.DB) *Service {
	return &Service{
		store:         &featureStore{db: db},
		client:        newSearchProxyClient(),
		previewClient: &http.Client{Timeout: 20 * time.Second},
	}
}

func (s *Service) Available() bool {
	return s != nil && s.client != nil
}

func (s *Service) Migrate() error {
	if s == nil || s.store == nil {
		return fmt.Errorf("%s is unavailable", featureName)
	}
	return s.store.migrate()
}

func (s *Service) Search(ctx context.Context, tenantID string, request SearchRequest) (*SearchPayload, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("%s is unavailable", featureName)
	}

	resolved, err := normalizeSearchRequest(request)
	if err != nil {
		return nil, err
	}

	cachedRecord, err := s.store.getCachedSearch(tenantID, resolved.LookupKey)
	if err != nil {
		return nil, err
	}
	if cachedRecord != nil {
		var payload SearchPayload
		if decodeErr := json.Unmarshal(cachedRecord.Response, &payload); decodeErr == nil {
			payload.Query = firstNonEmpty(payload.Query, resolved.Query)
			payload.Provider = firstNonEmpty(payload.Provider, searchProviderLinkup)
			if len(payload.Queries) == 0 {
				payload.Queries = append([]string(nil), resolved.Queries...)
			}
			payload.Cached = true
			s.persistRunBestEffort(&searchRunRecord{
				TenantID:    tenantID,
				LookupKey:   resolved.LookupKey,
				Query:       resolved.Query,
				CacheHit:    true,
				Status:      searchStatusOK,
				ResultCount: len(payload.Jobs),
				Response:    mustJSON(payload),
			})
			return &payload, nil
		} else {
			slog.Warn("beta linkedin jobs proxy cache decode failed", "error", decodeErr)
		}
	}

	if s.client == nil {
		return nil, fmt.Errorf("%s search proxy is not configured", featureName)
	}

	warnings := make([]string, 0, 2)
	rawCandidates := make([]rawSearchCandidate, 0, resolved.TopNPerQuery*len(resolved.Queries))
	failures := 0
	for _, query := range resolved.Queries {
		results, err := s.client.search(ctx, query, resolved.TopNPerQuery)
		if err != nil {
			failures++
			warnings = append(warnings, fmt.Sprintf("%s: %v", query, err))
			continue
		}
		for _, result := range results {
			if !isLinkedInJobURL(result.URL) {
				continue
			}
			rawCandidates = append(rawCandidates, rawSearchCandidate{
				SearchQuery: query,
				SearchTitle: result.Title,
				URL:         canonicalizeLinkedInURL(result.URL),
				Snippet:     trimRunes(firstNonEmpty(result.Snippet, result.Title), 480),
			})
		}
	}

	if len(rawCandidates) == 0 && failures == len(resolved.Queries) {
		runPayload := &SearchPayload{
			Query:       resolved.Query,
			Queries:     append([]string(nil), resolved.Queries...),
			Provider:    searchProviderLinkup,
			RetrievedAt: time.Now().UTC(),
			Warnings:    warnings,
		}
		s.persistRunBestEffort(&searchRunRecord{
			TenantID:     tenantID,
			LookupKey:    resolved.LookupKey,
			Query:        resolved.Query,
			CacheHit:     false,
			Status:       searchStatusFail,
			ResultCount:  0,
			ErrorMessage: "all search proxy queries failed",
			Response:     mustJSON(runPayload),
		})
		return nil, fmt.Errorf("all search proxy queries failed")
	}

	jobs, previewWarnings := s.buildPreviewJobs(ctx, rawCandidates, resolved)
	warnings = append(warnings, previewWarnings...)
	jobs = dedupePreviewJobs(jobs)
	sort.SliceStable(jobs, func(i, j int) bool {
		return betterJobPreview(jobs[i], jobs[j])
	})
	if len(jobs) > resolved.MaxResults {
		jobs = append([]JobPreview(nil), jobs[:resolved.MaxResults]...)
	}

	payload := &SearchPayload{
		Query:       resolved.Query,
		Queries:     append([]string(nil), resolved.Queries...),
		Provider:    searchProviderLinkup,
		RetrievedAt: time.Now().UTC(),
		Jobs:        jobs,
		Warnings:    warnings,
	}
	payloadJSON := mustJSON(payload)
	s.persistCacheBestEffort(&cachedSearchRecord{
		TenantID:    tenantID,
		LookupKey:   resolved.LookupKey,
		Query:       resolved.Query,
		Response:    payloadJSON,
		ResultCount: len(payload.Jobs),
		FetchedAt:   payload.RetrievedAt,
		ExpiresAt:   payload.RetrievedAt.Add(searchCacheTTL),
	})
	s.persistRunBestEffort(&searchRunRecord{
		TenantID:    tenantID,
		LookupKey:   resolved.LookupKey,
		Query:       resolved.Query,
		CacheHit:    false,
		Status:      searchStatusOK,
		ResultCount: len(payload.Jobs),
		Response:    payloadJSON,
	})
	return payload, nil
}

func (s *Service) buildPreviewJobs(ctx context.Context, items []rawSearchCandidate, request resolvedSearchRequest) ([]JobPreview, []string) {
	if len(items) == 0 {
		return nil, nil
	}

	seenURL := make(map[string]struct{}, len(items))
	warnings := make([]string, 0, 2)
	jobs := make([]JobPreview, 0, len(items))
	now := time.Now().UTC()
	for _, item := range items {
		if item.URL == "" {
			continue
		}
		if _, ok := seenURL[item.URL]; ok {
			continue
		}
		seenURL[item.URL] = struct{}{}

		preview, err := s.resolvePreview(ctx, item)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", item.URL, err))
		}
		job := mergePreview(item, preview)
		if !passesFilters(job, request.HardTitleFilter) {
			continue
		}

		job.SemanticScore = scoreIntentMatch(request.Query, job)
		job.RecencyScore = computeRecencyScore(now, job.PostedAt)
		job.Score = job.SemanticScore + job.RecencyScore
		jobs = append(jobs, job)
	}
	return jobs, warnings
}

func mergePreview(item rawSearchCandidate, preview previewMetadata) JobPreview {
	title := firstNonEmpty(preview.Title, item.SearchTitle)
	company := preview.Company
	location := preview.Location
	if title == "" {
		title = item.SearchTitle
	}
	title, inferredCompany, inferredLocation := inferTitleCompanyLocation(title, firstNonEmpty(preview.Description, item.Snippet))
	if company == "" {
		company = inferredCompany
	}
	if location == "" {
		location = inferredLocation
	}
	snippet := firstNonEmpty(preview.Snippet, item.Snippet)
	description := firstNonEmpty(preview.Description, snippet)
	roleType := classifyRole(title, strings.Join([]string{snippet, description}, " "))
	return JobPreview{
		URL:         item.URL,
		Title:       cleanText(title),
		Company:     cleanText(company),
		Location:    cleanText(location),
		Snippet:     trimRunes(cleanText(snippet), 480),
		Description: trimRunes(cleanText(description), 1400),
		PostedAt:    preview.PostedAt,
		RoleType:    roleType,
	}
}

func passesFilters(job JobPreview, hardTitleFilter bool) bool {
	titleNorm := normalizeComparableText(job.Title)
	if hardTitleFilter && !matchesHardAITitle(job.Title) {
		return false
	}
	if hasExplicitNonAITitle(job.Title) {
		return false
	}
	if classifyRole(job.Title, job.Snippet+" "+job.Description) == "" {
		return false
	}
	if hasAnyPhrase(titleNorm, excludedPhrases...) {
		return false
	}
	return cleanText(job.Title) != "" && cleanText(job.Company) != ""
}

func classifyRole(title, snippet string) string {
	titleNorm := normalizeComparableText(title)
	bodyNorm := normalizeComparableText(snippet)
	switch {
	case hasExplicitNonAITitle(title):
		return ""
	case hasAnyPhrase(titleNorm, aiRolePhrases...):
		return "ai_engineer"
	case hasAnyPhrase(titleNorm, mlRolePhrases...):
		return "ml_engineer"
	case matchesHardAITitle(title) && hasAnyPhrase(titleNorm, "engineer", "developer", "scientist", "researcher"):
		if hasAnyPhrase(titleNorm, "machine learning", "ml", "nlp", "natural language processing", "computer vision", "cv engineer", "deep learning") {
			return "ml_engineer"
		}
		return "ai_engineer"
	case hasAnyPhrase(bodyNorm, aiRolePhrases...):
		return "ai_engineer"
	case hasAnyPhrase(bodyNorm, mlRolePhrases...):
		return "ml_engineer"
	default:
		return ""
	}
}

func (s *Service) resolvePreview(ctx context.Context, item rawSearchCandidate) (previewMetadata, error) {
	urlHash := makeURLHash(item.URL)
	if cached, err := s.store.getPreview(urlHash); err == nil && cached != nil {
		return previewMetadata{
			Title:       cached.Title,
			Company:     cached.Company,
			Location:    cached.Location,
			Snippet:     cached.Snippet,
			Description: cached.Description,
			PostedAt:    cached.PostedAt,
		}, nil
	} else if err != nil {
		slog.Warn("beta linkedin jobs proxy preview cache lookup failed", "url", item.URL, "error", err)
	}

	preview, err := s.fetchPreview(ctx, item.URL)
	if preview.Title == "" {
		preview.Title = item.SearchTitle
	}
	if preview.Snippet == "" {
		preview.Snippet = item.Snippet
	}
	if preview.Description == "" {
		preview.Description = preview.Snippet
	}
	if preview.Company == "" || preview.Location == "" {
		_, company, location := inferTitleCompanyLocation(preview.Title, preview.Description)
		if preview.Company == "" {
			preview.Company = company
		}
		if preview.Location == "" {
			preview.Location = location
		}
	}
	s.persistPreviewBestEffort(&cachedPreviewRecord{
		URLHash:      urlHash,
		CanonicalURL: item.URL,
		Title:        cleanText(preview.Title),
		Company:      cleanText(preview.Company),
		Location:     cleanText(preview.Location),
		Snippet:      trimRunes(cleanText(preview.Snippet), 480),
		Description:  trimRunes(cleanText(preview.Description), 1400),
		PostedAt:     preview.PostedAt,
		FetchedAt:    time.Now().UTC(),
		ExpiresAt:    time.Now().UTC().Add(previewCacheTTL),
	})
	return preview, err
}

func (s *Service) fetchPreview(ctx context.Context, rawURL string) (previewMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return previewMetadata{}, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; GoClaw LinkedIn Jobs Proxy)")

	resp, err := s.previewClient.Do(req)
	if err != nil {
		return previewMetadata{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return previewMetadata{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return previewMetadata{}, fmt.Errorf("preview fetch returned status %d", resp.StatusCode)
	}

	preview := extractPreviewFromHTML(body)
	if cleanText(preview.Title) == "" {
		return preview, fmt.Errorf("preview metadata missing title")
	}
	return preview, nil
}

func extractPreviewFromHTML(body []byte) previewMetadata {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return previewMetadata{}
	}

	meta := make(map[string]string)
	var title string
	var scripts []string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil {
			return
		}
		if node.Type == html.ElementNode {
			switch strings.ToLower(node.Data) {
			case "title":
				if title == "" {
					title = cleanText(htmlNodeText(node))
				}
			case "meta":
				var name, content string
				for _, attr := range node.Attr {
					switch strings.ToLower(attr.Key) {
					case "name", "property":
						name = strings.ToLower(strings.TrimSpace(attr.Val))
					case "content":
						content = cleanText(attr.Val)
					}
				}
				if name != "" && content != "" {
					meta[name] = content
				}
			case "script":
				scriptType := ""
				for _, attr := range node.Attr {
					if strings.EqualFold(attr.Key, "type") {
						scriptType = strings.ToLower(strings.TrimSpace(attr.Val))
						break
					}
				}
				if scriptType == "application/ld+json" {
					scripts = append(scripts, htmlNodeText(node))
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	preview := previewMetadata{
		Title:       cleanText(firstNonEmpty(meta["og:title"], meta["twitter:title"], title)),
		Description: cleanText(firstNonEmpty(meta["description"], meta["og:description"], meta["twitter:description"])),
	}

	for _, script := range scripts {
		if script == "" {
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(script), &decoded); err != nil {
			continue
		}
		payload := findJobPostingPayload(decoded)
		if payload == nil {
			continue
		}
		jobTitle := cleanupLinkedInTitle(cleanText(asString(payload["title"])))
		if shouldPreferJobPostingTitle(preview.Title, jobTitle) {
			preview.Title = jobTitle
		}
		jobDescription := trimRunes(htmlToText(asString(payload["description"])), 1400)
		if shouldPreferJobPostingDescription(preview.Description, jobDescription) {
			preview.Description = jobDescription
		}
		if org, ok := payload["hiringOrganization"].(map[string]any); ok && preview.Company == "" {
			preview.Company = cleanText(asString(org["name"]))
		}
		if preview.Location == "" {
			preview.Location = extractLocation(payload["jobLocation"])
		}
		if preview.Location == "" {
			preview.Location = extractLocation(payload["applicantLocationRequirements"])
		}
		if postedAt := parseDateValue(asString(payload["datePosted"])); postedAt != nil {
			preview.PostedAt = postedAt
		}
	}

	if preview.Title != "" {
		preview.Title = cleanupLinkedInTitle(preview.Title)
	}
	if preview.Description != "" {
		preview.Description = trimRunes(cleanText(preview.Description), 1400)
	}
	if preview.Snippet == "" && preview.Description != "" {
		preview.Snippet = trimRunes(preview.Description, 480)
	}
	return preview
}

func cleanupLinkedInTitle(value string) string {
	value = cleanText(strings.TrimSpace(value))
	for _, suffix := range []string{" | LinkedIn", " - LinkedIn", " | LinkedIn Jobs", " - LinkedIn Jobs"} {
		value = strings.TrimSuffix(value, suffix)
	}
	return cleanText(value)
}

func shouldPreferJobPostingTitle(current, candidate string) bool {
	current = cleanupLinkedInTitle(current)
	candidate = cleanupLinkedInTitle(candidate)
	switch {
	case candidate == "":
		return false
	case current == "":
		return true
	case strings.EqualFold(current, candidate):
		return false
	case strings.Contains(strings.ToLower(current), "linkedin"):
		return true
	default:
		return len(candidate) < len(current)
	}
}

func shouldPreferJobPostingDescription(current, candidate string) bool {
	current = cleanText(current)
	candidate = cleanText(candidate)
	switch {
	case candidate == "":
		return false
	case current == "":
		return true
	case len(current) < 100 && len(candidate) > len(current)+24:
		return true
	default:
		return false
	}
}

func findJobPostingPayload(value any) map[string]any {
	switch decoded := value.(type) {
	case map[string]any:
		typeValue := strings.ToLower(cleanText(asString(decoded["@type"])))
		if typeValue == "jobposting" {
			return decoded
		}
		for _, child := range decoded {
			if found := findJobPostingPayload(child); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range decoded {
			if found := findJobPostingPayload(child); found != nil {
				return found
			}
		}
	}
	return nil
}

func extractLocation(value any) string {
	switch decoded := value.(type) {
	case map[string]any:
		if address, ok := decoded["address"].(map[string]any); ok {
			parts := []string{
				asString(address["addressLocality"]),
				asString(address["addressRegion"]),
				asString(address["addressCountry"]),
			}
			return cleanText(strings.Join(parts, ", "))
		}
		return cleanText(asString(decoded["name"]))
	case []any:
		parts := make([]string, 0, len(decoded))
		for _, child := range decoded {
			if item := extractLocation(child); item != "" {
				parts = append(parts, item)
			}
		}
		return cleanText(strings.Join(parts, " / "))
	default:
		return cleanText(asString(decoded))
	}
}

func asString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func parseDateValue(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02",
		"2006-01-02T15:04:05Z07:00",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

func inferTitleCompanyLocation(title, description string) (string, string, string) {
	title = cleanupLinkedInTitle(title)
	description = cleanText(description)
	if title == "" {
		return "", "", inferLocationFromText(description)
	}

	separators := []string{" - ", " | ", " at "}
	for _, separator := range separators {
		parts := strings.Split(title, separator)
		if len(parts) < 2 {
			continue
		}
		role := cleanText(parts[0])
		company := cleanText(parts[1])
		location := ""
		if len(parts) >= 3 {
			location = cleanText(parts[2])
		}
		if location == "" {
			location = inferLocationFromText(description)
		}
		if role != "" && company != "" {
			return role, company, location
		}
	}
	return title, "", inferLocationFromText(description)
}

func inferLocationFromText(value string) string {
	value = cleanText(value)
	norm := normalizeComparableText(value)
	switch {
	case hasAnyPhrase(norm, "remote", "anywhere", "worldwide", "distributed"):
		return "Remote"
	case hasAnyPhrase(norm, "ho chi minh", "hanoi", "da nang", "vietnam"):
		return "Vietnam"
	default:
		return ""
	}
}

func htmlNodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil {
			return
		}
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
			b.WriteByte(' ')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return cleanText(b.String())
}

func htmlToText(raw string) string {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return cleanText(raw)
	}
	return htmlNodeText(doc)
}

func (s *Service) persistRunBestEffort(record *searchRunRecord) {
	if s == nil || s.store == nil || record == nil {
		return
	}
	if err := s.store.insertRun(record); err != nil {
		slog.Warn("beta linkedin jobs proxy run persist failed", "error", err)
	}
}

func (s *Service) persistCacheBestEffort(record *cachedSearchRecord) {
	if s == nil || s.store == nil || record == nil {
		return
	}
	if err := s.store.upsertCachedSearch(record); err != nil {
		slog.Warn("beta linkedin jobs proxy cache persist failed", "error", err)
	}
}

func (s *Service) persistPreviewBestEffort(record *cachedPreviewRecord) {
	if s == nil || s.store == nil || record == nil {
		return
	}
	if err := s.store.upsertPreview(record); err != nil {
		slog.Warn("beta linkedin jobs proxy preview persist failed", "error", err)
	}
}
