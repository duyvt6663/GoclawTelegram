package linkedinjobsproxy

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type searchTool struct {
	feature *LinkedInJobsProxyFeature
}

func (t *searchTool) Name() string { return "linkedin_jobs_proxy_search" }

func (t *searchTool) Description() string {
	return "Search public LinkedIn job previews indirectly through the configured search proxy with strict AI/ML noise reduction."
}

func (t *searchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Query intent such as `AI engineer remote` or an explicit site:linkedin.com/jobs search query.",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Maximum filtered job previews to return. Defaults to 16.",
				"minimum":     1,
				"maximum":     maxMaxResults,
			},
			"top_n_per_query": map[string]any{
				"type":        "integer",
				"description": "How many search-proxy results to collect per generated query. Defaults to 8.",
				"minimum":     1,
				"maximum":     maxTopNPerQuery,
			},
			"hard_title_filter": map[string]any{
				"type":        "boolean",
				"description": "When true, require the title to contain AI, machine learning, ML, LLM, NLP, or computer vision.",
			},
			"remote_only": map[string]any{
				"type":        "boolean",
				"description": "When true, bias generated search queries toward remote roles.",
			},
		},
		"required": []string{"query"},
	}
}

func (t *searchTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil || t.feature.service == nil {
		return tools.ErrorResult("linkedin jobs proxy feature is unavailable")
	}

	request := SearchRequest{
		Query:        stringArg(args, "query"),
		MaxResults:   intArg(args, "max_results"),
		TopNPerQuery: intArg(args, "top_n_per_query"),
	}
	if value, ok := boolArg(args, "hard_title_filter"); ok {
		request.HardTitleFilter = value
	}
	if value, ok := boolArg(args, "remote_only"); ok {
		request.RemoteOnly = value
	}

	payload, err := t.feature.service.Search(ctx, tenantKeyFromCtx(ctx), request)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(formatToolPayload(payload))
}
