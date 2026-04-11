package jobcrawler

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

type crawl4aiClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func newCrawl4AIClient(baseURL, token string) *crawl4aiClient {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil
	}
	return &crawl4aiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   strings.TrimSpace(token),
		client: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

func (c *crawl4aiClient) FetchHTML(ctx context.Context, rawURL, waitFor string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("crawl4ai is not configured")
	}

	payload := map[string]any{
		"urls": []string{strings.TrimSpace(rawURL)},
		"browser_config": map[string]any{
			"type":   "BrowserConfig",
			"params": map[string]any{"headless": true},
		},
		"crawler_config": map[string]any{
			"type": "CrawlerRunConfig",
			"params": map[string]any{
				"stream":     false,
				"cache_mode": "bypass",
			},
		},
	}
	if waitFor != "" {
		payload["crawler_config"].(map[string]any)["params"].(map[string]any)["wait_for"] = waitFor
	}

	raw, err := c.postJSON(ctx, "/crawl", payload)
	if err != nil {
		return "", err
	}

	if html := extractFirstString(raw, "cleaned_html", "html", "markdown"); html != "" {
		return html, nil
	}
	if result := extractNested(raw, "results"); result != nil {
		if items, ok := result.([]any); ok {
			for _, item := range items {
				if html := extractFirstString(item, "cleaned_html", "html", "markdown"); html != "" {
					return html, nil
				}
			}
		}
	}
	if result := extractNested(raw, "result"); result != nil {
		if html := extractFirstString(result, "cleaned_html", "html", "markdown"); html != "" {
			return html, nil
		}
	}

	return "", fmt.Errorf("crawl4ai returned no HTML")
}

func (c *crawl4aiClient) SummarizeURL(ctx context.Context, rawURL string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("crawl4ai is not configured")
	}

	payload := map[string]any{
		"url": strings.TrimSpace(rawURL),
		"f":   "llm",
		"q":   "Summarize this remote job in one short sentence focused on the role, location constraints, and key skills. No markdown.",
	}

	raw, err := c.postJSON(ctx, "/md", payload)
	if err != nil {
		return "", err
	}

	text := cleanText(extractFirstString(raw, "summary", "content", "result", "markdown", "text"))
	if text == "" {
		if body, ok := raw.(string); ok {
			text = cleanText(body)
		}
	}
	if text == "" {
		return "", fmt.Errorf("crawl4ai returned no summary")
	}
	return trimText(text, 220), nil
}

func (c *crawl4aiClient) postJSON(ctx context.Context, path string, payload any) (any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &providers.HTTPError{Status: resp.StatusCode, Body: trimText(string(respBody), 500)}
	}

	var out any
	if err := json.Unmarshal(respBody, &out); err == nil {
		return out, nil
	}
	return string(respBody), nil
}

func extractNested(value any, key string) any {
	if value == nil {
		return nil
	}
	switch current := value.(type) {
	case map[string]any:
		return current[key]
	default:
		return nil
	}
}

func extractFirstString(value any, keys ...string) string {
	if value == nil {
		return ""
	}
	switch current := value.(type) {
	case string:
		return current
	case map[string]any:
		for _, key := range keys {
			if text, ok := current[key].(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
		for _, key := range keys {
			if nested, ok := current[key].(map[string]any); ok {
				if text := extractFirstString(nested, keys...); text != "" {
					return text
				}
			}
		}
	case []any:
		for _, item := range current {
			if text := extractFirstString(item, keys...); text != "" {
				return text
			}
		}
	}
	return ""
}
