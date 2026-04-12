package linkupwebsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

const (
	linkupAPIBaseURL           = "https://api.linkup.so/v1"
	linkupUserAgent            = "GoClaw-LinkupWebSearch/1.0"
	linkupSearchDepthStandard  = "standard"
	linkupSearchDepthDeep      = "deep"
	defaultSearchOutputType    = "sourcedAnswer"
	defaultSearchMaxResults    = 6
	defaultSearchTimeout       = 45 * time.Second
	defaultSearchRetryAttempts = 3
)

type linkupClient struct {
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
	Answer  string            `json:"answer"`
	Sources []linkupAPISource `json:"sources"`
	Results []linkupAPISource `json:"results"`
}

type linkupAPISource struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Content string `json:"content"`
	Favicon string `json:"favicon"`
}

func newLinkupClient(apiKey string) *linkupClient {
	retryCfg := providers.DefaultRetryConfig()
	retryCfg.Attempts = defaultSearchRetryAttempts
	retryCfg.MinDelay = 500 * time.Millisecond
	retryCfg.MaxDelay = 8 * time.Second

	return &linkupClient{
		apiKey:     strings.TrimSpace(apiKey),
		baseURL:    linkupAPIBaseURL,
		httpClient: &http.Client{Timeout: defaultSearchTimeout},
		retryCfg:   retryCfg,
	}
}

func (c *linkupClient) search(ctx context.Context, request resolvedSearchRequest) (*linkupSearchResponse, error) {
	apiRequest := linkupSearchRequest{
		Query:                  request.Query,
		Depth:                  request.Depth,
		OutputType:             defaultSearchOutputType,
		IncludeInlineCitations: true,
		MaxResults:             request.TopKSources,
	}

	return providers.RetryDo(ctx, c.retryCfg, func() (*linkupSearchResponse, error) {
		return c.doSearch(ctx, apiRequest)
	})
}

func (c *linkupClient) doSearch(ctx context.Context, request linkupSearchRequest) (*linkupSearchResponse, error) {
	bodyJSON, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal Linkup request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/search", bytes.NewReader(bodyJSON))
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

	if normalizeQuery(parsed.Answer) == "" && len(parsed.Sources) == 0 && len(parsed.Results) == 0 {
		return nil, fmt.Errorf("Linkup returned an empty response")
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

	if message := anyToString(payload["message"]); message != "" {
		return message
	}
	if errVal, ok := payload["error"]; ok {
		switch v := errVal.(type) {
		case string:
			return strings.TrimSpace(v)
		case map[string]any:
			if message := anyToString(v["message"]); message != "" {
				return message
			}
			if message := anyToString(v["details"]); message != "" {
				return message
			}
		}
	}

	return trimmed
}

func anyToString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
