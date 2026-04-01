package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	defaultMemeSearchCount = 6
	memeHTMLProbeMaxBytes  = 512 * 1024
	maxMemeBytes           = 10 * 1024 * 1024
)

var (
	memeQueryKeywords    = []string{"meme", "reaction", "macro", "template", "caption"}
	memeGoodHints        = []string{"meme", "reaction", "funny", "caption", "imgflip", "knowyourmeme", "memedroid"}
	memeBadHints         = []string{"logo", "avatar", "icon", "sprite", "emoji", "badge", "favicon", "thumb"}
	preferredMemeDomains = map[string]int{
		"imgflip.com":      30,
		"knowyourmeme.com": 24,
		"memedroid.com":    18,
		"imgur.com":        12,
	}
	blockedMemeDomains = []string{
		"tenor.com",
		"giphy.com",
		"gifdb.com",
		"tiktok.com",
		"instagram.com",
		"facebook.com",
		"x.com",
		"twitter.com",
	}
	supportedMemeMIMEs = map[string]string{
		"image/jpeg": "jpg",
		"image/png":  "png",
	}
	supportedMemeExts = map[string]string{
		".jpeg": "image/jpeg",
		".jpg":  "image/jpeg",
		".png":  "image/png",
	}
)

type memeCandidate struct {
	SourceTitle   string
	SourcePageURL string
	ImageURL      string
}

type memePageProbe struct {
	FinalURL    string
	ContentType string
	HTML        []byte
}

// FindAndPostMemeTool searches the web for an existing meme image, downloads it,
// and returns it as media for the current reply.
type FindAndPostMemeTool struct {
	search      *WebSearchTool
	fetch       *WebFetchTool
	validateURL func(string) error
}

func NewFindAndPostMemeTool(search *WebSearchTool, fetch *WebFetchTool) *FindAndPostMemeTool {
	if search == nil {
		return nil
	}
	return &FindAndPostMemeTool{
		search: search,
		fetch:  fetch,
	}
}

func (t *FindAndPostMemeTool) Name() string { return "find_and_post_meme" }

func (t *FindAndPostMemeTool) Description() string {
	return "Find an online meme or reaction image on the web, download it to the workspace, and attach it to your next reply. Query for the reaction you want to send back, not a literal description of the incoming meme. Use this only when local saved stickers or local meme files are not a good fit."
}

func (t *FindAndPostMemeTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Short reaction, comeback, or joke beat to search for as an online meme image. Query for the reply you want to send, not a literal description of the incoming meme. Prefer 1-3 broad keywords instead of full sentences. Example: 'facepalm reaction', 'not impressed', 'caught in 4k'. Use this after local reaction media options have been considered.",
			},
			"filename_hint": map[string]any{
				"type":        "string",
				"description": "Optional short filename hint for the downloaded meme file.",
			},
		},
		"required": []string{"query"},
	}
}

func (t *FindAndPostMemeTool) Execute(ctx context.Context, args map[string]any) *Result {
	query := strings.TrimSpace(GetParamString(args, "query", ""))
	if query == "" {
		return ErrorResult("query is required")
	}
	if ReactionMediaModeFromCtx(ctx) {
		query = normalizeReactionMediaQuery(query)
	}
	if t.search == nil || len(t.search.providers) == 0 {
		return ErrorResult("meme search is unavailable because no web search providers are configured")
	}

	filenameHint := strings.TrimSpace(GetParamString(args, "filename_hint", ""))
	if filenameHint == "" {
		filenameHint = query
	}

	var lastErr error
	for _, searchQuery := range buildMemeSearchQueries(query) {
		results, providerName, err := t.searchResults(ctx, searchQuery)
		if err != nil {
			lastErr = err
			continue
		}
		if result := t.trySearchResults(ctx, query, searchQuery, providerName, filenameHint, results); result != nil {
			return result
		}

		lastErr = fmt.Errorf("no downloadable meme found in %s results for %q", providerName, searchQuery)
	}

	for _, directQuery := range buildImgflipSearchQueries(query) {
		results, err := t.searchImgflip(ctx, directQuery)
		if err != nil {
			lastErr = err
			continue
		}
		if result := t.trySearchResults(ctx, query, directQuery, "imgflip-direct", filenameHint, results); result != nil {
			return result
		}
		lastErr = fmt.Errorf("no downloadable meme found in imgflip-direct results for %q", directQuery)
	}

	if lastErr != nil {
		if suggestion := broaderMemeSuggestion(query); suggestion != "" && suggestion != query {
			return ErrorResult(fmt.Sprintf("could not find a downloadable meme for %q: %v. Retry with a broader 1-3 word query such as %q.", query, lastErr, suggestion))
		}
		return ErrorResult(fmt.Sprintf("could not find a downloadable meme for %q: %v", query, lastErr))
	}
	return ErrorResult(fmt.Sprintf("could not find a downloadable meme for %q", query))
}

func buildMemeSearchQueries(query string) []string {
	base := strings.TrimSpace(query)
	if base == "" {
		return nil
	}

	var queries []string
	seen := make(map[string]bool)
	add := func(q string) {
		q = strings.TrimSpace(q)
		if q == "" || seen[q] {
			return
		}
		seen[q] = true
		queries = append(queries, q)
	}

	lower := strings.ToLower(base)
	add(base)
	add("site:imgflip.com " + base)
	add("site:knowyourmeme.com " + base)
	if !containsAny(lower, memeQueryKeywords) {
		add(base + " meme")
		add("site:imgflip.com " + base + " meme")
		add(base + " reaction image")
	}
	add(base + " image macro")

	return queries
}

func buildImgflipSearchQueries(query string) []string {
	base := strings.TrimSpace(query)
	if base == "" {
		return nil
	}

	var queries []string
	seen := make(map[string]bool)
	add := func(q string) {
		q = strings.TrimSpace(q)
		if q == "" || seen[q] {
			return
		}
		seen[q] = true
		queries = append(queries, q)
	}

	add(base)
	terms := simplifiedMemeTerms(base)
	if len(terms) >= 2 {
		add(strings.Join(terms[:2], " "))
	}
	if len(terms) >= 3 {
		add(strings.Join(terms[:3], " "))
	}
	for _, term := range terms {
		add(term)
	}
	if !containsAny(strings.ToLower(base), memeQueryKeywords) {
		add(base + " meme")
		if len(terms) > 0 {
			add(terms[0] + " meme")
		}
	}

	return queries
}

func (t *FindAndPostMemeTool) searchResults(ctx context.Context, query string) ([]searchResult, string, error) {
	params := searchParams{
		Query: query,
		Count: defaultMemeSearchCount,
	}

	var lastErr error
	for _, provider := range t.search.providers {
		results, err := provider.Search(ctx, params)
		if err != nil {
			lastErr = err
			continue
		}
		if len(results) == 0 {
			continue
		}
		prioritizeMemeSearchResults(results)
		return results, provider.Name(), nil
	}

	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", fmt.Errorf("no search results")
}

func (t *FindAndPostMemeTool) searchImgflip(ctx context.Context, query string) ([]searchResult, error) {
	rawURL := "https://imgflip.com/memesearch?q=" + url.QueryEscape(query)
	if err := t.validateRemoteURL(rawURL); err != nil {
		return nil, err
	}

	client := t.newHTTPClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, memeHTMLProbeMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	results := extractImgflipSearchResults(body)
	if len(results) == 0 {
		return nil, fmt.Errorf("no imgflip search results")
	}
	return results, nil
}

func extractImgflipSearchResults(body []byte) []searchResult {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}

	var results []searchResult
	seen := make(map[string]bool)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.A {
			href := strings.TrimSpace(getAttr(n, "href"))
			if strings.HasPrefix(href, "/meme/") {
				fullURL := "https://imgflip.com" + href
				if !seen[fullURL] {
					title := strings.TrimSpace(getAttr(n, "title"))
					if title == "" {
						title = strings.TrimSpace(htmlNodeText(n))
					}
					results = append(results, searchResult{Title: title, URL: fullURL})
					seen[fullURL] = true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if len(results) > defaultMemeSearchCount {
		results = results[:defaultMemeSearchCount]
	}
	return results
}

func htmlNodeText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return cleanTextOutput(sb.String())
}

func simplifiedMemeTerms(query string) []string {
	stopwords := map[string]bool{
		"a": true, "an": true, "and": true, "about": true, "find": true,
		"for": true, "funny": true, "image": true, "meme": true, "memes": true,
		"no": true, "of": true, "on": true, "only": true, "post": true,
		"reaction": true, "the": true, "to": true, "with": true,
	}

	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})

	var terms []string
	seen := make(map[string]bool)
	for _, field := range fields {
		if len(field) < 3 || stopwords[field] || seen[field] {
			continue
		}
		seen[field] = true
		terms = append(terms, field)
		if len(terms) >= 4 {
			break
		}
	}
	return terms
}

func broaderMemeSuggestion(query string) string {
	terms := simplifiedMemeTerms(query)
	if len(terms) == 0 {
		return ""
	}
	switch terms[0] {
	case "deploy", "devops":
		return "deployment"
	default:
		return terms[0]
	}
}

func (t *FindAndPostMemeTool) trySearchResults(ctx context.Context, query, searchQuery, providerName, filenameHint string, results []searchResult) *Result {
	for _, result := range results {
		candidate, err := t.resolveCandidate(ctx, result)
		if err != nil {
			slog.Debug("find_and_post_meme: candidate rejected",
				"provider", providerName,
				"result_url", result.URL,
				"error", err,
			)
			continue
		}

		filePath, mimeType, err := t.downloadMeme(ctx, candidate.ImageURL, filenameHint)
		if err != nil {
			slog.Debug("find_and_post_meme: download failed",
				"provider", providerName,
				"image_url", candidate.ImageURL,
				"error", err,
			)
			continue
		}

		var llm strings.Builder
		llm.WriteString(fmt.Sprintf("Attached a meme for %q.\n", query))
		llm.WriteString(fmt.Sprintf("Search query: %s\n", searchQuery))
		llm.WriteString(fmt.Sprintf("Search provider: %s\n", providerName))
		if candidate.SourceTitle != "" {
			llm.WriteString(fmt.Sprintf("Source title: %s\n", candidate.SourceTitle))
		}
		llm.WriteString(fmt.Sprintf("Source page: %s\n", candidate.SourcePageURL))
		llm.WriteString(fmt.Sprintf("Image URL: %s\n", candidate.ImageURL))
		llm.WriteString(fmt.Sprintf("Saved file: %s\n", filepath.Base(filePath)))
		llm.WriteString("The attachment will be delivered with your next reply. Write only the caption or context you want the user to see.")

		return &Result{
			ForLLM: llm.String(),
			Media: []bus.MediaFile{{
				Path:     filePath,
				MimeType: mimeType,
			}},
			Deliverable: fmt.Sprintf("[Found meme: %s]\nQuery: %s\nSource: %s", filepath.Base(filePath), query, candidate.SourcePageURL),
		}
	}

	return nil
}

func (t *FindAndPostMemeTool) resolveCandidate(ctx context.Context, result searchResult) (memeCandidate, error) {
	rawURL := strings.TrimSpace(result.URL)
	if rawURL == "" {
		return memeCandidate{}, fmt.Errorf("search result URL is empty")
	}
	if host := hostnameForURL(rawURL); isBlockedMemeDomain(host) {
		return memeCandidate{}, fmt.Errorf("skipping unsupported meme source %q", host)
	}
	if err := t.validateRemoteURL(rawURL); err != nil {
		return memeCandidate{}, err
	}

	if isDirectMemeImageURL(rawURL) {
		return memeCandidate{
			SourceTitle:   strings.TrimSpace(result.Title),
			SourcePageURL: rawURL,
			ImageURL:      rawURL,
		}, nil
	}

	probe, err := t.probePage(ctx, rawURL)
	if err != nil {
		return memeCandidate{}, err
	}
	if isSupportedMemeMIME(probe.ContentType) {
		return memeCandidate{
			SourceTitle:   strings.TrimSpace(result.Title),
			SourcePageURL: probe.FinalURL,
			ImageURL:      probe.FinalURL,
		}, nil
	}

	imageURL, err := extractMemeImageURL(probe.FinalURL, probe.HTML)
	if err != nil {
		return memeCandidate{}, err
	}
	if err := t.validateRemoteURL(imageURL); err != nil {
		return memeCandidate{}, err
	}

	return memeCandidate{
		SourceTitle:   strings.TrimSpace(result.Title),
		SourcePageURL: probe.FinalURL,
		ImageURL:      imageURL,
	}, nil
}

func (t *FindAndPostMemeTool) probePage(ctx context.Context, rawURL string) (memePageProbe, error) {
	client := t.newHTTPClient()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return memePageProbe{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,image/*;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return memePageProbe{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return memePageProbe{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	finalURL := resp.Request.URL.String()
	contentType := canonicalMemeContentType(resp.Header.Get("Content-Type"))
	if isSupportedMemeMIME(contentType) {
		return memePageProbe{
			FinalURL:    finalURL,
			ContentType: contentType,
		}, nil
	}

	if contentType != "" && !strings.Contains(contentType, "html") && !strings.Contains(contentType, "xml") {
		return memePageProbe{}, fmt.Errorf("unsupported page content type %q", contentType)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, memeHTMLProbeMaxBytes))
	if err != nil {
		return memePageProbe{}, fmt.Errorf("read response: %w", err)
	}
	if len(body) == 0 {
		return memePageProbe{}, fmt.Errorf("page body is empty")
	}

	return memePageProbe{
		FinalURL:    finalURL,
		ContentType: contentType,
		HTML:        body,
	}, nil
}

func extractMemeImageURL(baseURL string, body []byte) (string, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("parse HTML: %w", err)
	}

	metaKeys := map[string]bool{
		"og:image":            true,
		"og:image:url":        true,
		"og:image:secure_url": true,
		"twitter:image":       true,
		"twitter:image:src":   true,
	}

	type scoredCandidate struct {
		URL   string
		Score int
	}

	var best scoredCandidate
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Meta:
				key := strings.ToLower(strings.TrimSpace(getAttr(n, "property")))
				if key == "" {
					key = strings.ToLower(strings.TrimSpace(getAttr(n, "name")))
				}
				if metaKeys[key] {
					if resolved, ok := resolveMemeURL(baseURL, getAttr(n, "content")); ok {
						best = scoredCandidate{URL: resolved, Score: 100}
						return
					}
				}
			case atom.Img:
				rawSrc := imageNodeSource(n)
				resolved, ok := resolveMemeURL(baseURL, rawSrc)
				if !ok {
					break
				}
				score := scoreMemeImageCandidate(n, resolved)
				if score > best.Score {
					best = scoredCandidate{URL: resolved, Score: score}
				}
			}
		}
		if best.Score >= 100 {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
			if best.Score >= 100 {
				return
			}
		}
	}
	walk(doc)

	if best.URL == "" {
		return "", fmt.Errorf("no image candidate found on the page")
	}
	return best.URL, nil
}

func imageNodeSource(n *html.Node) string {
	for _, key := range []string{"src", "data-src", "data-lazy-src", "data-original"} {
		if v := strings.TrimSpace(getAttr(n, key)); v != "" {
			return v
		}
	}
	if srcset := strings.TrimSpace(getAttr(n, "srcset")); srcset != "" {
		first := strings.TrimSpace(strings.Split(srcset, ",")[0])
		if first != "" {
			parts := strings.Fields(first)
			if len(parts) > 0 {
				return parts[0]
			}
		}
	}
	return ""
}

func scoreMemeImageCandidate(n *html.Node, resolvedURL string) int {
	score := 1
	lower := strings.ToLower(strings.Join([]string{
		resolvedURL,
		getAttr(n, "alt"),
		getAttr(n, "title"),
		getAttr(n, "class"),
		getAttr(n, "id"),
	}, " "))

	for _, hint := range memeGoodHints {
		if strings.Contains(lower, hint) {
			score += 4
		}
	}
	for _, hint := range memeBadHints {
		if strings.Contains(lower, hint) {
			score -= 6
		}
	}

	if w := parseHTMLDimension(getAttr(n, "width")); w >= 200 {
		score += 2
	} else if w > 0 && w < 96 {
		score -= 3
	}
	if h := parseHTMLDimension(getAttr(n, "height")); h >= 200 {
		score += 2
	} else if h > 0 && h < 96 {
		score -= 3
	}

	return score
}

func parseHTMLDimension(raw string) int {
	raw = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(raw), "px"))
	v, _ := strconv.Atoi(raw)
	return v
}

func resolveMemeURL(baseURL, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}

	base, err := url.Parse(baseURL)
	if err != nil {
		return "", false
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", false
	}

	resolved := base.ResolveReference(ref)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false
	}
	return resolved.String(), true
}

func (t *FindAndPostMemeTool) downloadMeme(ctx context.Context, rawURL, filenameHint string) (string, string, error) {
	if err := t.validateRemoteURL(rawURL); err != nil {
		return "", "", err
	}

	client := t.newHTTPClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	req.Header.Set("Accept", "image/png,image/jpeg;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	lr := &io.LimitedReader{R: resp.Body, N: maxMemeBytes + 1}
	data, err := io.ReadAll(lr)
	if err != nil {
		return "", "", fmt.Errorf("read image: %w", err)
	}
	if int64(len(data)) > maxMemeBytes {
		return "", "", fmt.Errorf("image exceeds %d bytes", maxMemeBytes)
	}
	if len(data) == 0 {
		return "", "", fmt.Errorf("image body is empty")
	}

	mimeType := detectMemeMIME(resp.Request.URL.String(), resp.Header.Get("Content-Type"), data)
	if !isSupportedMemeMIME(mimeType) {
		return "", "", fmt.Errorf("unsupported image content type %q", mimeType)
	}

	ext, ok := supportedMemeMIMEs[mimeType]
	if !ok {
		return "", "", fmt.Errorf("unsupported image content type %q", mimeType)
	}

	dateDir := time.Now().Format("2006-01-02")
	fileName := mediaFileName(ctx, "meme", filenameHint, ext)
	for _, outDir := range candidateMemeOutputDirs(ctx) {
		outDir = filepath.Join(outDir, "downloads", "memes", dateDir)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			slog.Warn("find_and_post_meme: output dir unavailable, trying fallback",
				"dir", outDir,
				"error", err,
			)
			continue
		}

		outPath := filepath.Join(outDir, fileName)
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			slog.Warn("find_and_post_meme: output file unavailable, trying fallback",
				"path", outPath,
				"error", err,
			)
			continue
		}
		return outPath, mimeType, nil
	}

	return "", "", fmt.Errorf("failed to save meme image to workspace or temp directory")
}

func detectMemeMIME(rawURL, headerType string, data []byte) string {
	if ct := canonicalMemeContentType(headerType); isSupportedMemeMIME(ct) {
		return ct
	}
	if sniffed := canonicalMemeContentType(http.DetectContentType(data)); isSupportedMemeMIME(sniffed) {
		return sniffed
	}
	if u, err := url.Parse(rawURL); err == nil {
		if ct, ok := supportedMemeExts[strings.ToLower(filepath.Ext(u.Path))]; ok {
			return ct
		}
	}
	return canonicalMemeContentType(headerType)
}

func canonicalMemeContentType(raw string) string {
	ct := strings.ToLower(strings.TrimSpace(raw))
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = strings.TrimSpace(ct[:idx])
	}
	if ct == "image/jpg" {
		return "image/jpeg"
	}
	return ct
}

func isSupportedMemeMIME(ct string) bool {
	_, ok := supportedMemeMIMEs[ct]
	return ok
}

func isDirectMemeImageURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	_, ok := supportedMemeExts[strings.ToLower(filepath.Ext(u.Path))]
	return ok
}

func (t *FindAndPostMemeTool) currentFetchPolicy() string {
	if t.fetch == nil {
		return "allow_all"
	}
	t.fetch.mu.RLock()
	defer t.fetch.mu.RUnlock()
	if t.fetch.policy == "" {
		return "allow_all"
	}
	return t.fetch.policy
}

func prioritizeMemeSearchResults(results []searchResult) {
	sort.SliceStable(results, func(i, j int) bool {
		return memeDomainScore(results[i].URL) > memeDomainScore(results[j].URL)
	})
}

func memeDomainScore(rawURL string) int {
	host := hostnameForURL(rawURL)
	if host == "" {
		return 0
	}
	if isBlockedMemeDomain(host) {
		return -100
	}
	for domain, score := range preferredMemeDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return score
		}
	}
	return 0
}

func hostnameForURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func isBlockedMemeDomain(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, domain := range blockedMemeDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func (t *FindAndPostMemeTool) validateRemoteURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("missing hostname")
	}

	if t.validateURL != nil {
		if err := t.validateURL(rawURL); err != nil {
			return err
		}
	} else if err := CheckSSRF(rawURL); err != nil {
		return fmt.Errorf("SSRF protection: %w", err)
	}

	if t.fetch != nil {
		host := parsed.Hostname()
		if t.fetch.isDomainBlocked(host) {
			return fmt.Errorf("domain %q is blocked by policy", host)
		}
		if t.currentFetchPolicy() == "allowlist" && !t.fetch.isDomainAllowed(host) {
			return fmt.Errorf("domain %q is not in the allowed domains list", host)
		}
	}

	return nil
}

func (t *FindAndPostMemeTool) newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: time.Duration(fetchTimeoutSeconds) * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 15 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= defaultFetchMaxRedirect {
				return fmt.Errorf("stopped after %d redirects", defaultFetchMaxRedirect)
			}
			return t.validateRemoteURL(req.URL.String())
		},
	}
}

func candidateMemeOutputDirs(ctx context.Context) []string {
	var dirs []string
	seen := make(map[string]bool)
	add := func(dir string) {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}

	add(ToolWorkspaceFromCtx(ctx))
	add(os.TempDir())
	return dirs
}

func containsAny(s string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
