package linkedinjobsproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

const (
	linkupAPIBaseURL          = "https://api.linkup.so/v1"
	linkupUserAgent           = "GoClaw-LinkedInJobsProxy/1.0"
	linkupSearchDepthStandard = "standard"
	linkupOutputType          = "sourcedAnswer"
	linkupSearchTimeout       = 45 * time.Second
)

type searchProxyClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	retryCfg   providers.RetryConfig
}

type linkupSearchRequest struct {
	Query                  string `json:"q"`
	Depth                  string `json:"depth"`
	OutputType             string `json:"outputType"`
	IncludeInlineCitations bool   `json:"includeInlineCitations"`
	MaxResults             int    `json:"maxResults"`
}

type linkupSearchResponse struct {
	Sources []linkupAPISource `json:"sources"`
	Results []linkupAPISource `json:"results"`
}

type linkupAPISource struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Content string `json:"content"`
}

type rawSearchResult struct {
	Title   string
	URL     string
	Snippet string
}

func newSearchProxyClient() *searchProxyClient {
	apiKey := firstNonEmpty(
		os.Getenv("GOCLAW_BETA_LINKEDIN_JOBS_PROXY_LINKUP_API_KEY"),
		os.Getenv("LINKUP_API_KEY"),
	)
	if apiKey == "" {
		return nil
	}

	retryCfg := providers.DefaultRetryConfig()
	retryCfg.Attempts = 3
	retryCfg.MinDelay = 500 * time.Millisecond
	retryCfg.MaxDelay = 8 * time.Second

	return &searchProxyClient{
		apiKey:     apiKey,
		baseURL:    firstNonEmpty(os.Getenv("GOCLAW_BETA_LINKEDIN_JOBS_PROXY_LINKUP_BASE_URL"), linkupAPIBaseURL),
		httpClient: &http.Client{Timeout: linkupSearchTimeout},
		retryCfg:   retryCfg,
	}
}

func (c *searchProxyClient) search(ctx context.Context, query string, maxResults int) ([]rawSearchResult, error) {
	if c == nil {
		return nil, fmt.Errorf("search proxy is not configured")
	}

	apiRequest := linkupSearchRequest{
		Query:                  normalizeQuery(query),
		Depth:                  linkupSearchDepthStandard,
		OutputType:             linkupOutputType,
		IncludeInlineCitations: false,
		MaxResults:             maxResults,
	}

	response, err := providers.RetryDo(ctx, c.retryCfg, func() (*linkupSearchResponse, error) {
		return c.doSearch(ctx, apiRequest)
	})
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	results := make([]rawSearchResult, 0, len(response.Sources)+len(response.Results))
	appendSource := func(source linkupAPISource) {
		rawURL := canonicalizeLinkedInURL(source.URL)
		if rawURL == "" {
			return
		}
		if _, ok := seen[rawURL]; ok {
			return
		}
		seen[rawURL] = struct{}{}
		snippet := firstNonEmpty(cleanText(source.Snippet), cleanText(source.Content))
		results = append(results, rawSearchResult{
			Title:   cleanText(source.Name),
			URL:     rawURL,
			Snippet: trimRunes(snippet, 480),
		})
	}

	for _, source := range response.Sources {
		appendSource(source)
	}
	for _, source := range response.Results {
		appendSource(source)
	}
	return results, nil
}

func (c *searchProxyClient) doSearch(ctx context.Context, request linkupSearchRequest) (*linkupSearchResponse, error) {
	bodyJSON, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal Linkup request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.baseURL, "/")+"/search", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("create Linkup request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", linkupUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Linkup request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read Linkup response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		message := trimRunes(extractLinkupErrorMessage(body), 280)
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, &providers.HTTPError{
			Status:     resp.StatusCode,
			Body:       message,
			RetryAfter: providers.ParseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	var parsed linkupSearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse Linkup response: %w", err)
	}
	if len(parsed.Sources) == 0 && len(parsed.Results) == 0 {
		return nil, fmt.Errorf("Linkup returned no search results")
	}
	return &parsed, nil
}

func extractLinkupErrorMessage(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return trimmed
	}
	if message, _ := payload["message"].(string); strings.TrimSpace(message) != "" {
		return strings.TrimSpace(message)
	}
	if rawError, ok := payload["error"]; ok {
		switch value := rawError.(type) {
		case string:
			return strings.TrimSpace(value)
		case map[string]any:
			if message, _ := value["message"].(string); strings.TrimSpace(message) != "" {
				return strings.TrimSpace(message)
			}
			if details, _ := value["details"].(string); strings.TrimSpace(details) != "" {
				return strings.TrimSpace(details)
			}
		}
	}
	return trimmed
}
