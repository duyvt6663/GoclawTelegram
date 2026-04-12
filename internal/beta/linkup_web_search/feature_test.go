package linkupwebsearch

import (
	"slices"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
)

func TestNormalizeSearchRequestDefaults(t *testing.T) {
	request, err := normalizeSearchRequest(SearchRequest{Query: "  Latest AI revenue  "})
	if err != nil {
		t.Fatalf("normalizeSearchRequest returned error: %v", err)
	}

	if request.Query != "Latest AI revenue" {
		t.Fatalf("unexpected normalized query: %q", request.Query)
	}
	if request.Mode != searchModeFast {
		t.Fatalf("expected default mode %q, got %q", searchModeFast, request.Mode)
	}
	if request.Depth != linkupSearchDepthStandard {
		t.Fatalf("expected default depth %q, got %q", linkupSearchDepthStandard, request.Depth)
	}
	if request.TopKSources != defaultSearchMaxResults {
		t.Fatalf("expected default top_k_sources %d, got %d", defaultSearchMaxResults, request.TopKSources)
	}
	expectedLookupKey := buildSearchLookupKey("Latest AI revenue", searchModeFast, defaultSearchMaxResults)
	if request.LookupKey != expectedLookupKey {
		t.Fatalf("expected lookup key %q, got %q", expectedLookupKey, request.LookupKey)
	}
}

func TestNormalizeSearchRequestDeepMode(t *testing.T) {
	request, err := normalizeSearchRequest(SearchRequest{
		Query:       "Why did market breadth weaken last quarter?",
		Mode:        "deep",
		TopKSources: 4,
	})
	if err != nil {
		t.Fatalf("normalizeSearchRequest returned error: %v", err)
	}

	if request.Mode != searchModeDeep {
		t.Fatalf("expected mode %q, got %q", searchModeDeep, request.Mode)
	}
	if request.Depth != linkupSearchDepthDeep {
		t.Fatalf("expected depth %q, got %q", linkupSearchDepthDeep, request.Depth)
	}
	if request.TopKSources != 4 {
		t.Fatalf("expected top_k_sources 4, got %d", request.TopKSources)
	}
}

func TestNormalizeSearchRequestRejectsInvalidMode(t *testing.T) {
	_, err := normalizeSearchRequest(SearchRequest{
		Query: "Latest robotics funding",
		Mode:  "auto",
	})
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if !isSearchInputError(err) {
		t.Fatalf("expected search input error, got %T", err)
	}
}

func TestBuildSearchPayloadHonorsTopKSources(t *testing.T) {
	payload := buildSearchPayload(resolvedSearchRequest{
		Query:       "robotics market updates",
		Mode:        searchModeDeep,
		Depth:       linkupSearchDepthDeep,
		TopKSources: 1,
	}, &linkupSearchResponse{
		Sources: []linkupAPISource{
			{Name: "First", URL: "https://example.com/1", Snippet: "First snippet"},
			{Name: "Second", URL: "https://example.com/2", Snippet: "Second snippet"},
		},
		Results: []linkupAPISource{
			{Name: "Third", URL: "https://example.com/3", Snippet: "Third snippet"},
		},
	})

	if payload.Mode != searchModeDeep {
		t.Fatalf("expected mode %q, got %q", searchModeDeep, payload.Mode)
	}
	if payload.Depth != linkupSearchDepthDeep {
		t.Fatalf("expected depth %q, got %q", linkupSearchDepthDeep, payload.Depth)
	}
	if len(payload.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(payload.Sources))
	}
	if payload.Answer == "" {
		t.Fatal("expected fallback answer from retained source snippets")
	}
}

func TestTopicRoutingRegistryIncludesLinkupSearchTool(t *testing.T) {
	topicrouting.Clear()
	t.Cleanup(topicrouting.Clear)

	topicrouting.RegisterTopicFeatureTools(featureName, (&searchTool{}).Name())

	snapshot := topicrouting.TopicFeatureToolsSnapshot()
	tools, ok := snapshot[featureName]
	if !ok {
		t.Fatalf("expected %q to be present in topic routing snapshot", featureName)
	}
	if !slices.Contains(tools, "linkup_web_search") {
		t.Fatalf("expected topic routing snapshot for %q to include linkup_web_search, got %v", featureName, tools)
	}
}
