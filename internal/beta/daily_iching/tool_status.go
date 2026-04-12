package dailyiching

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type statusTool struct {
	feature *DailyIChingFeature
}

func (t *statusTool) Name() string { return "daily_iching_status" }

func (t *statusTool) Description() string {
	return "Inspect daily I Ching configs, progression state, and today's posting status."
}

func (t *statusTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":  map[string]any{"type": "string", "description": "Optional config key. Defaults to the current group config when possible."},
			"date": map[string]any{"type": "string", "description": "Optional local date in YYYY-MM-DD"},
		},
	}
}

func (t *statusTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("daily i ching feature is unavailable")
	}

	key := stringArg(args, "key")
	if key != "" {
		cfg, err := t.feature.resolveToolConfig(ctx, key)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		localDate, err := resolveLocalDate(cfg, stringArg(args, "date"))
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		status, err := t.feature.statusForConfig(cfg, localDate)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		return toolJSONResult(status)
	}

	configs, err := t.feature.store.listConfigs(tenantKeyFromCtx(ctx))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	statuses := make([]ConfigStatus, 0, len(configs))
	for i := range configs {
		localDate, err := resolveLocalDate(&configs[i], stringArg(args, "date"))
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		status, err := t.feature.statusForConfig(&configs[i], localDate)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		statuses = append(statuses, status)
	}
	return toolJSONResult(map[string]any{"configs": statuses})
}
