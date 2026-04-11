package jobcrawler

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type listConfigsTool struct {
	feature *JobCrawlerFeature
}

func (t *listConfigsTool) Name() string { return "job_crawler_list_configs" }

func (t *listConfigsTool) Description() string {
	return "List job crawler configs and last-run status for the current tenant."
}

func (t *listConfigsTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *listConfigsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":          map[string]any{"type": "string", "description": "Optional config key to inspect."},
			"current_only": map[string]any{"type": "boolean", "description": "When true, resolve the current Telegram chat/topic config."},
		},
	}
}

func (t *listConfigsTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("job crawler feature is unavailable")
	}

	if key := stringArg(args, "key"); key != "" {
		cfg, err := t.feature.store.getConfigByKey(tenantKeyFromCtx(ctx), key)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		status, err := t.feature.statusForConfig(cfg)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		return toolJSONResult(status)
	}

	if value, ok := boolArg(args, "current_only"); ok && *value {
		channel := tools.ToolChannelFromCtx(ctx)
		chatID, threadID := chatTargetFromToolContext(ctx, "", 0)
		cfg, err := t.feature.resolveRunConfig(tenantKeyFromCtx(ctx), "", channel, chatID, threadID)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		status, err := t.feature.statusForConfig(cfg)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		return toolJSONResult(status)
	}

	statuses, err := t.feature.listStatuses(tenantKeyFromCtx(ctx))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(map[string]any{"configs": statuses})
}
