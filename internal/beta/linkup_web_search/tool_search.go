package linkupwebsearch

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type searchTool struct {
	feature *LinkupWebSearchFeature
}

func (t *searchTool) Name() string { return "linkup_web_search" }

func (t *searchTool) Description() string {
	return "Search the web with Linkup and return a concise factual answer plus source links. Use deep mode for slower, broader coverage."
}

func (t *searchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Factual or time-sensitive question to search on the web.",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{searchModeFast, searchModeDeep},
				"description": "Search mode. `fast` keeps the current low-latency behavior. `deep` takes longer but improves coverage.",
			},
			"top_k_sources": map[string]any{
				"type":        "integer",
				"description": "Optional maximum number of sources to return. Defaults to 6.",
				"minimum":     1,
				"maximum":     maxTopKSources,
			},
		},
		"required": []string{"query"},
	}
}

func (t *searchTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("linkup web search feature is unavailable")
	}

	payload, err := t.feature.search(ctx, tenantKeyFromCtx(ctx), SearchRequest{
		Query:       stringArg(args, "query"),
		Mode:        stringArg(args, "mode"),
		TopKSources: intArg(args, "top_k_sources"),
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	return tools.NewResult(formatToolPayload(payload))
}
