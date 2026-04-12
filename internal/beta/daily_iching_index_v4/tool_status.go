package dailyichingindexv4

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type statusTool struct {
	feature *DailyIChingIndexV4Feature
}

func (t *statusTool) Name() string { return "daily_iching_index_v4_status" }

func (t *statusTool) Description() string {
	return "Inspect the current daily_iching index_v4 snapshot plus the latest validation run."
}

func (t *statusTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *statusTool) Execute(ctx context.Context, _ map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("daily iching index_v4 feature is unavailable")
	}
	payload, err := t.feature.statusPayload(ctx)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(payload)
}
