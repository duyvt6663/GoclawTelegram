package dailyichingquery

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type queryTool struct {
	feature *DailyIChingQueryFeature
}

func (t *queryTool) Name() string { return "daily_iching_query" }

func (t *queryTool) Description() string {
	return "Answer arbitrary Kinh Dich questions in Vietnamese using grounded retrieval from the cached daily_iching corpus."
}

func (t *queryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{"type": "string", "description": "The Kinh Dich question to answer."},
		},
		"required": []string{"question"},
	}
}

func (t *queryTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("daily iching query feature is unavailable")
	}
	payload, err := t.feature.answerQuestion(ctx, tenantKeyFromCtx(ctx), stringArg(args, "question"))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(payload)
}
