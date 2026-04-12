package linkupwebsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
)

const (
	featureName       = "linkup_web_search"
	searchCacheTTL    = 10 * time.Minute
	searchStatusOK    = "success"
	searchStatusFail  = "failed"
	searchModeFast    = "fast"
	searchModeDeep    = "deep"
	defaultSearchMode = searchModeFast
	maxTopKSources    = 10
)

// LinkupWebSearchFeature adds a Linkup-backed web search tool for concise,
// source-grounded factual answers.
//
// Plan:
// 1. Normalize query, mode, and top_k_sources so fast mode stays backward compatible while deep mode maps to Linkup depth=deep.
// 2. Reuse the existing cache/run tables but key cached lookups by normalized request identity so fast and deep results stay isolated.
// 3. Expose the upgraded search through the beta tool, RPC method, and HTTP route with the same feature wiring.
type LinkupWebSearchFeature struct {
	store  *featureStore
	client *linkupClient
}

type SearchRequest struct {
	Query       string `json:"query"`
	Mode        string `json:"mode,omitempty"`
	TopKSources int    `json:"top_k_sources,omitempty"`
}

type resolvedSearchRequest struct {
	Query       string
	LookupKey   string
	Mode        string
	Depth       string
	TopKSources int
}

type SearchPayload struct {
	Query       string         `json:"query"`
	Answer      string         `json:"answer"`
	Sources     []SearchSource `json:"sources,omitempty"`
	Provider    string         `json:"provider"`
	Mode        string         `json:"mode"`
	Depth       string         `json:"depth"`
	TopKSources int            `json:"top_k_sources"`
	OutputType  string         `json:"output_type"`
	Cached      bool           `json:"cached"`
	RetrievedAt time.Time      `json:"retrieved_at"`
}

type SearchSource struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Favicon string `json:"favicon,omitempty"`
}

func (f *LinkupWebSearchFeature) Name() string { return featureName }

func (f *LinkupWebSearchFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	apiKey := strings.TrimSpace(os.Getenv("LINKUP_API_KEY"))
	if apiKey == "" {
		return fmt.Errorf("%s requires LINKUP_API_KEY", featureName)
	}

	f.store = &featureStore{db: deps.Stores.DB}
	f.client = newLinkupClient(apiKey)

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}
	topicrouting.RegisterTopicFeatureTools(featureName, (&searchTool{}).Name())

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&searchTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	slog.Info("beta linkup web search initialized")
	return nil
}

func (f *LinkupWebSearchFeature) Shutdown(_ context.Context) error {
	topicrouting.UnregisterTopicFeatureTools(featureName)
	return nil
}

func (f *LinkupWebSearchFeature) search(ctx context.Context, tenantID string, request SearchRequest) (*SearchPayload, error) {
	if f == nil || f.store == nil || f.client == nil {
		return nil, fmt.Errorf("%s is unavailable", featureName)
	}

	resolved, err := normalizeSearchRequest(request)
	if err != nil {
		return nil, err
	}

	cachedRecord, err := f.store.getCachedSearch(tenantID, resolved.LookupKey)
	if err != nil {
		return nil, err
	}
	if cachedRecord != nil {
		var payload SearchPayload
		if err := json.Unmarshal(cachedRecord.Response, &payload); err == nil {
			payload.Cached = true
			if payload.Query == "" {
				payload.Query = resolved.Query
			}
			if payload.Mode == "" {
				payload.Mode = resolved.Mode
			}
			if payload.Depth == "" {
				payload.Depth = resolved.Depth
			}
			if payload.TopKSources <= 0 {
				payload.TopKSources = resolved.TopKSources
			}
			if payload.OutputType == "" {
				payload.OutputType = defaultSearchOutputType
			}
			f.persistRunBestEffort(&searchRunRecord{
				TenantID:    tenantID,
				Query:       resolved.Query,
				LookupKey:   resolved.LookupKey,
				CacheHit:    true,
				Status:      searchStatusOK,
				SourceCount: len(payload.Sources),
				Response:    mustJSON(payload),
			})
			return &payload, nil
		} else {
			slog.Warn("beta linkup web search cache decode failed", "error", err)
		}
	}

	response, err := f.client.search(ctx, resolved)
	if err != nil {
		f.persistRunBestEffort(&searchRunRecord{
			TenantID:     tenantID,
			Query:        resolved.Query,
			LookupKey:    resolved.LookupKey,
			CacheHit:     false,
			Status:       searchStatusFail,
			ErrorMessage: err.Error(),
		})
		return nil, err
	}

	payload := buildSearchPayload(resolved, response)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	f.persistCacheBestEffort(&cachedSearchRecord{
		TenantID:    tenantID,
		Query:       resolved.Query,
		LookupKey:   resolved.LookupKey,
		Response:    payloadJSON,
		SourceCount: len(payload.Sources),
		FetchedAt:   payload.RetrievedAt,
		ExpiresAt:   payload.RetrievedAt.Add(searchCacheTTL),
	})
	f.persistRunBestEffort(&searchRunRecord{
		TenantID:    tenantID,
		Query:       resolved.Query,
		LookupKey:   resolved.LookupKey,
		CacheHit:    false,
		Status:      searchStatusOK,
		SourceCount: len(payload.Sources),
		Response:    payloadJSON,
	})

	return payload, nil
}

func buildSearchPayload(request resolvedSearchRequest, response *linkupSearchResponse) *SearchPayload {
	payload := &SearchPayload{
		Query:       request.Query,
		Provider:    "linkup",
		Mode:        request.Mode,
		Depth:       request.Depth,
		TopKSources: request.TopKSources,
		OutputType:  defaultSearchOutputType,
		RetrievedAt: time.Now().UTC(),
	}
	if response == nil {
		return payload
	}

	payload.Answer = trimRunes(normalizeQuery(response.Answer), 1600)
	sourceMap := make(map[string]struct{})
	for _, source := range response.Sources {
		if len(payload.Sources) >= request.TopKSources {
			break
		}
		appendSource(payload, sourceMap, source)
	}
	for _, source := range response.Results {
		if len(payload.Sources) >= request.TopKSources {
			break
		}
		appendSource(payload, sourceMap, source)
	}

	if payload.Answer == "" {
		var fallback []string
		for _, source := range payload.Sources {
			if source.Snippet == "" {
				continue
			}
			fallback = append(fallback, trimRunes(source.Snippet, 220))
			if len(fallback) == 2 {
				break
			}
		}
		payload.Answer = strings.Join(fallback, " ")
	}

	return payload
}

func appendSource(payload *SearchPayload, seen map[string]struct{}, source linkupAPISource) {
	if payload == nil {
		return
	}
	url := strings.TrimSpace(source.URL)
	if url == "" {
		return
	}
	if _, ok := seen[url]; ok {
		return
	}
	seen[url] = struct{}{}
	payload.Sources = append(payload.Sources, SearchSource{
		Title:   strings.TrimSpace(source.Name),
		URL:     url,
		Snippet: trimRunes(normalizeQuery(firstNonEmpty(source.Snippet, source.Content)), 500),
		Favicon: strings.TrimSpace(source.Favicon),
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (f *LinkupWebSearchFeature) persistCacheBestEffort(record *cachedSearchRecord) {
	if err := f.store.upsertCachedSearch(record); err != nil {
		slog.Warn("beta linkup web search cache persist failed", "error", err)
	}
}

func (f *LinkupWebSearchFeature) persistRunBestEffort(record *searchRunRecord) {
	if err := f.store.insertRun(record); err != nil {
		slog.Warn("beta linkup web search run persist failed", "error", err)
	}
}
