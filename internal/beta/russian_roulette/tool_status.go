package russianroulette

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type statusTool struct {
	feature *RussianRouletteFeature
}

func (t *statusTool) Name() string { return "roulette_status" }

func (t *statusTool) Description() string {
	return "Show Russian roulette status for the current Telegram group/topic or a named config."
}

func (t *statusTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":               map[string]any{"type": "string", "description": "Optional roulette config key."},
			"leaderboard_limit": map[string]any{"type": "integer", "description": "Optional leaderboard size to include."},
		},
	}
}

func (t *statusTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("roulette feature is unavailable")
	}

	limit := leaderboardDefaultLimit
	if value, ok := intArg(args, "leaderboard_limit"); ok {
		limit = value
	}

	key := stringArg(args, "key")
	cfg, err := t.feature.resolveToolConfig(ctx, key)
	if err == nil {
		status, err := t.feature.statusForConfig(cfg, limit)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		return toolJSONResult(status)
	}
	if key != "" {
		return tools.ErrorResult(err.Error())
	}

	statuses, listErr := t.feature.listStatuses(tenantKeyFromCtx(ctx), limit)
	if listErr != nil {
		return tools.ErrorResult(listErr.Error())
	}
	if len(statuses) != 1 {
		return toolJSONResult(map[string]any{"configs": statuses})
	}
	return toolJSONResult(statuses[0])
}
