package russianroulette

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type leaderboardTool struct {
	feature *RussianRouletteFeature
}

func (t *leaderboardTool) Name() string { return "roulette_leaderboard" }

func (t *leaderboardTool) Description() string {
	return "Show the Russian roulette leaderboard for the current Telegram group/topic or a named config."
}

func (t *leaderboardTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":   map[string]any{"type": "string", "description": "Optional roulette config key."},
			"limit": map[string]any{"type": "integer", "description": "Optional leaderboard size."},
		},
	}
}

func (t *leaderboardTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("roulette feature is unavailable")
	}

	cfg, err := t.feature.resolveToolConfig(ctx, stringArg(args, "key"))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	limit := leaderboardDefaultLimit
	if value, ok := intArg(args, "limit"); ok {
		limit = value
	}
	stats, err := t.feature.leaderboardForConfig(cfg, limit)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(map[string]any{
		"config":      cfg,
		"leaderboard": stats,
	})
}
