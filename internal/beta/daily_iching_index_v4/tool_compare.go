package dailyichingindexv4

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type compareTool struct {
	feature *DailyIChingIndexV4Feature
}

func (t *compareTool) Name() string { return "daily_iching_index_v4_compare" }

func (t *compareTool) Description() string {
	return "Build or rebuild daily_iching index_v4, then compare v2/v3/v4 retrieval on sample queries."
}

func (t *compareTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"queries": map[string]any{
				"type":        "array",
				"description": "Optional sample queries. Defaults to the built-in inspection set.",
				"items":       map[string]any{"type": "string"},
			},
			"rebuild": map[string]any{
				"type":        "boolean",
				"description": "Force a fresh v4 rebuild instead of reusing the cache.",
			},
		},
	}
}

func (t *compareTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("daily iching index_v4 feature is unavailable")
	}
	payload, err := t.feature.comparePayload(ctx, stringSliceArg(args, "queries"), boolArg(args, "rebuild"))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(payload)
}
