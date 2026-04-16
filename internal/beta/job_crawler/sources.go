package jobcrawler

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	linkedinjobsproxy "github.com/nextlevelbuilder/goclaw/internal/beta/linkedin_jobs_proxy"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

type normalizedLocation struct {
	Label     string
	IsRemote  bool
	IsVietnam bool
	IsAsia    bool
}

type JobListing struct {
	Source       string     `json:"source"`
	SourceLabel  string     `json:"source_label"`
	Title        string     `json:"title"`
	Company      string     `json:"company"`
	Location     string     `json:"location"`
	Tags         []string   `json:"tags,omitempty"`
	URL          string     `json:"url"`
	PostedAt     *time.Time `json:"posted_at,omitempty"`
	Description  string     `json:"description,omitempty"`
	AssumeRemote bool       `json:"assume_remote,omitempty"`
}

type RankedJob struct {
	JobListing
	JobHash            string     `json:"job_hash"`
	Score              float64    `json:"score"`
	SemanticScore      float64    `json:"semantic_score"`
	KeywordScore       float64    `json:"keyword_score"`
	LocationWeight     float64    `json:"location_weight"`
	RoleMatch          float64    `json:"role_match"`
	RecencyWeight      float64    `json:"recency_weight"`
	DynamicBoost       float64    `json:"dynamic_boost"`
	PenaltyScore       float64    `json:"penalty_score"`
	RoleType           string     `json:"role_type,omitempty"`
	SeniorityLevel     string     `json:"seniority_level,omitempty"`
	NormalizedLocation string     `json:"normalized_location"`
	IsRemote           bool       `json:"is_remote"`
	IsVietnam          bool       `json:"is_vietnam"`
	IsAsia             bool       `json:"is_asia,omitempty"`
	MatchedKeywords    []string   `json:"matched_keywords,omitempty"`
	ShortSummary       string     `json:"short_summary,omitempty"`
	LastPostedAt       *time.Time `json:"last_posted_at,omitempty"`
	NormalizedTitle    string     `json:"-"`
	ContentTokens      []string   `json:"-"`
}

type cachedSourceResult struct {
	fetchedAt time.Time
	jobs      []JobListing
}

type sourceSpec struct {
	ID         string
	Label      string
	ListingURL string
	FeedURL    string
}

var sourceSpecs = map[string]sourceSpec{
	sourceRemoteOK: {
		ID:         sourceRemoteOK,
		Label:      "RemoteOK",
		ListingURL: "https://remoteok.com/remote-dev-jobs",
		FeedURL:    "https://remoteok.com/api",
	},
	sourceWeWorkRemotely: {
		ID:         sourceWeWorkRemotely,
		Label:      "WeWorkRemotely",
		ListingURL: "https://weworkremotely.com/remote-full-time-jobs",
		FeedURL:    "https://weworkremotely.com/remote-jobs.rss",
	},
	sourceLinkedInProxy: {
		ID:    sourceLinkedInProxy,
		Label: "LinkedIn (Search Proxy)",
	},
}

func (f *JobCrawlerFeature) fetchJobsForSource(ctx context.Context, cfg *JobCrawlerConfig, sourceID string) ([]JobListing, error) {
	spec, ok := sourceSpecs[strings.TrimSpace(strings.ToLower(sourceID))]
	if !ok {
		return nil, fmt.Errorf("unsupported source %q", sourceID)
	}

	if spec.ID != sourceLinkedInProxy {
		if jobs, ok := f.cachedJobs(spec.ID); ok {
			return jobs, nil
		}
	}

	retryCfg := providers.DefaultRetryConfig()
	retryCfg.Attempts = 3
	retryCfg.MinDelay = 500 * time.Millisecond
	retryCfg.MaxDelay = 4 * time.Second

	jobs, err := providers.RetryDo(ctx, retryCfg, func() ([]JobListing, error) {
		switch spec.ID {
		case sourceRemoteOK:
			return f.fetchRemoteOKJobs(ctx, spec)
		case sourceWeWorkRemotely:
			return f.fetchWeWorkRemotelyJobs(ctx, spec)
		case sourceLinkedInProxy:
			return f.fetchLinkedInProxyJobs(ctx, cfg, spec)
		default:
			return nil, fmt.Errorf("unsupported source %q", spec.ID)
		}
	})
	if err != nil {
		return nil, err
	}
	if spec.ID != sourceLinkedInProxy {
		f.storeCachedJobs(spec.ID, jobs)
	}
	return jobs, nil
}

func (f *JobCrawlerFeature) fetchLinkedInProxyJobs(ctx context.Context, cfg *JobCrawlerConfig, spec sourceSpec) ([]JobListing, error) {
	if f == nil || f.linkedinProxy == nil {
		return nil, fmt.Errorf("linkedin jobs proxy is unavailable")
	}
	if cfg == nil {
		return nil, fmt.Errorf("job crawler config is required")
	}

	payload, err := f.linkedinProxy.Search(ctx, cfg.TenantID, linkedinjobsproxy.SearchRequest{
		Query:           buildLinkedInProxyIntent(cfg),
		MaxResults:      resolveLinkedInProxyMaxResults(cfg),
		TopNPerQuery:    8,
		HardTitleFilter: cfg.HardTitleFilter,
		RemoteOnly:      cfg.RemoteOnly || cfg.LocationMode == locationModeRemoteGlobal || cfg.LocationMode == locationModeHybrid,
	})
	if err != nil {
		return nil, err
	}
	if payload == nil || len(payload.Jobs) == 0 {
		return nil, nil
	}

	out := make([]JobListing, 0, len(payload.Jobs))
	for _, preview := range payload.Jobs {
		title := cleanText(preview.Title)
		company := cleanText(preview.Company)
		rawURL := canonicalizeURL(preview.URL)
		if title == "" || company == "" || rawURL == "" {
			continue
		}

		tags := []string{"linkedin"}
		if preview.RoleType != "" {
			tags = append(tags, strings.ReplaceAll(preview.RoleType, "_", " "))
		}
		location := cleanText(preview.Location)
		description := trimText(cleanText(strings.Join([]string{preview.Snippet, preview.Description}, " ")), 2400)
		out = append(out, JobListing{
			Source:       spec.ID,
			SourceLabel:  spec.Label,
			Title:        title,
			Company:      company,
			Location:     location,
			Tags:         normalizeStringSlice(tags),
			URL:          rawURL,
			PostedAt:     preview.PostedAt,
			Description:  description,
			AssumeRemote: strings.Contains(strings.ToLower(location+" "+preview.Snippet), "remote"),
		})
	}
	return out, nil
}

func (f *JobCrawlerFeature) cachedJobs(sourceID string) ([]JobListing, bool) {
	f.cacheMu.Lock()
	defer f.cacheMu.Unlock()
	entry, ok := f.sourceCache[sourceID]
	if !ok || time.Since(entry.fetchedAt) > sourceCacheTTL {
		return nil, false
	}
	return cloneJobListings(entry.jobs), true
}

func (f *JobCrawlerFeature) storeCachedJobs(sourceID string, jobs []JobListing) {
	f.cacheMu.Lock()
	defer f.cacheMu.Unlock()
	f.sourceCache[sourceID] = cachedSourceResult{
		fetchedAt: time.Now().UTC(),
		jobs:      cloneJobListings(jobs),
	}
}

func cloneJobListings(in []JobListing) []JobListing {
	if len(in) == 0 {
		return nil
	}
	out := make([]JobListing, len(in))
	copy(out, in)
	return out
}

func (f *JobCrawlerFeature) fetchRemoteOKJobs(ctx context.Context, spec sourceSpec) ([]JobListing, error) {
	body, err := fetchURL(ctx, spec.FeedURL)
	if err != nil {
		return nil, err
	}

	type remoteOKItem struct {
		ID          string   `json:"id"`
		Date        string   `json:"date"`
		Epoch       int64    `json:"epoch"`
		Company     string   `json:"company"`
		Position    string   `json:"position"`
		Tags        []string `json:"tags"`
		Description string   `json:"description"`
		Location    string   `json:"location"`
		URL         string   `json:"url"`
		ApplyURL    string   `json:"apply_url"`
	}

	var items []remoteOKItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}

	out := make([]JobListing, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Position) == "" {
			continue
		}
		postedAt := parseRFC3339(item.Date)
		if postedAt == nil && item.Epoch > 0 {
			ts := time.Unix(item.Epoch, 0).UTC()
			postedAt = &ts
		}
		rawURL := item.URL
		if rawURL == "" {
			rawURL = item.ApplyURL
		}
		if rawURL == "" {
			continue
		}
		out = append(out, JobListing{
			Source:       spec.ID,
			SourceLabel:  spec.Label,
			Title:        cleanText(item.Position),
			Company:      cleanText(item.Company),
			Location:     cleanText(item.Location),
			Tags:         normalizeStringSlice(item.Tags),
			URL:          canonicalizeURL(rawURL),
			PostedAt:     postedAt,
			Description:  trimText(htmlToText(item.Description), 2400),
			AssumeRemote: true,
		})
	}
	return out, nil
}

func (f *JobCrawlerFeature) fetchWeWorkRemotelyJobs(ctx context.Context, spec sourceSpec) ([]JobListing, error) {
	if f.crawl4ai != nil {
		htmlBody, err := f.crawl4ai.FetchHTML(ctx, spec.ListingURL, ".jobs-container")
		if err == nil {
			jobs, parseErr := parseWWRHTML(spec, htmlBody)
			if parseErr == nil && len(jobs) > 0 {
				return jobs, nil
			}
			if parseErr != nil {
				slog.Warn("beta job crawler: crawl4ai HTML parse failed", "source", spec.ID, "error", parseErr)
			}
		} else {
			slog.Warn("beta job crawler: crawl4ai listing crawl failed", "source", spec.ID, "error", err)
		}
	}
	return fetchWWRRSS(ctx, spec)
}

func fetchWWRRSS(ctx context.Context, spec sourceSpec) ([]JobListing, error) {
	body, err := fetchURL(ctx, spec.FeedURL)
	if err != nil {
		return nil, err
	}

	type item struct {
		Title       string `xml:"title"`
		Region      string `xml:"region"`
		Country     string `xml:"country"`
		State       string `xml:"state"`
		Skills      string `xml:"skills"`
		Category    string `xml:"category"`
		Type        string `xml:"type"`
		Description string `xml:"description"`
		PubDate     string `xml:"pubDate"`
		Link        string `xml:"link"`
	}
	type rss struct {
		Channel struct {
			Items []item `xml:"item"`
		} `xml:"channel"`
	}

	var feed rss
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}

	out := make([]JobListing, 0, len(feed.Channel.Items))
	for _, entry := range feed.Channel.Items {
		if strings.TrimSpace(entry.Link) == "" || strings.TrimSpace(entry.Title) == "" {
			continue
		}
		company := ""
		title := cleanText(entry.Title)
		if before, after, ok := strings.Cut(title, ":"); ok {
			company = cleanText(before)
			title = cleanText(after)
		}
		if company == "" {
			company = "Unknown"
		}
		location := cleanText(strings.TrimSpace(entry.Region))
		if location == "" {
			location = cleanText(strings.TrimSpace(strings.TrimSpace(entry.State + " " + entry.Country)))
		}
		tags := normalizeStringSlice(splitCSVLike(entry.Skills, entry.Category, entry.Type))
		out = append(out, JobListing{
			Source:       spec.ID,
			SourceLabel:  spec.Label,
			Title:        title,
			Company:      company,
			Location:     location,
			Tags:         tags,
			URL:          canonicalizeURL(entry.Link),
			PostedAt:     parseRFC3339(entry.PubDate),
			Description:  trimText(htmlToText(entry.Description), 2400),
			AssumeRemote: true,
		})
	}
	return out, nil
}

func fetchURL(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; GoClaw beta job crawler)")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &providers.HTTPError{Status: resp.StatusCode, Body: trimText(string(body), 500)}
	}
	return body, nil
}

func parseWWRHTML(spec sourceSpec, raw string) ([]JobListing, error) {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return nil, err
	}

	var out []JobListing
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" && hasClass(n, "listing-link--unlocked") {
			if job, ok := extractWWRAnchor(spec, n); ok {
				out = append(out, job)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return out, nil
}

func extractWWRAnchor(spec sourceSpec, anchor *html.Node) (JobListing, bool) {
	href := strings.TrimSpace(attr(anchor, "href"))
	if href == "" {
		return JobListing{}, false
	}

	title := cleanText(findTextByClass(anchor, "new-listing__header__title__text"))
	if title == "" {
		return JobListing{}, false
	}

	company := cleanText(findTextByClass(anchor, "new-listing__company-name"))
	if company == "" {
		company = "Unknown"
	}
	location := cleanText(findTextByClass(anchor, "new-listing__company-headquarters"))
	categories := normalizeStringSlice(findAllTextByClass(anchor, "new-listing__categories__category"))
	dateText := cleanText(findTextByClass(anchor, "new-listing__header__icons__date"))
	postedAt := parseWWRAge(dateText)

	return JobListing{
		Source:       spec.ID,
		SourceLabel:  spec.Label,
		Title:        title,
		Company:      company,
		Location:     location,
		Tags:         categories,
		URL:          canonicalizeURL(resolveRelativeURL(spec.ListingURL, href)),
		PostedAt:     postedAt,
		AssumeRemote: true,
	}, true
}

func parseWWRAge(value string) *time.Time {
	value = strings.ToLower(cleanText(value))
	if value == "" {
		return nil
	}
	now := time.Now().UTC()
	if value == "new" {
		return &now
	}
	if strings.HasSuffix(value, "h") {
		hours, err := strconv.Atoi(strings.TrimSuffix(value, "h"))
		if err == nil {
			ts := now.Add(-time.Duration(hours) * time.Hour)
			return &ts
		}
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err == nil {
			ts := now.AddDate(0, 0, -days)
			return &ts
		}
	}
	return nil
}

func resolveRelativeURL(base, href string) string {
	parsedBase, err := neturl.Parse(base)
	if err != nil {
		return href
	}
	parsedHref, err := neturl.Parse(href)
	if err != nil {
		return href
	}
	return parsedBase.ResolveReference(parsedHref).String()
}

func attr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	classes := strings.Fields(attr(n, "class"))
	for _, item := range classes {
		if item == class {
			return true
		}
	}
	return false
}

func findTextByClass(n *html.Node, class string) string {
	if n == nil {
		return ""
	}
	if n.Type == html.ElementNode && hasClass(n, class) {
		return htmlNodeText(n)
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if text := findTextByClass(child, class); text != "" {
			return text
		}
	}
	return ""
}

func findAllTextByClass(n *html.Node, class string) []string {
	var out []string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil {
			return
		}
		if node.Type == html.ElementNode && hasClass(node, class) {
			if text := cleanText(htmlNodeText(node)); text != "" {
				out = append(out, text)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return out
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

func splitCSVLike(values ...string) []string {
	var out []string
	for _, value := range values {
		for _, item := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == '/' || r == '|'
		}) {
			item = cleanText(item)
			if item != "" {
				out = append(out, item)
			}
		}
	}
	return out
}
