package jobcrawler

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type runDynamicCrawlerTool struct {
	feature *JobCrawlerFeature
}

func (t *runDynamicCrawlerTool) Name() string { return "job_crawler_run_dynamic" }

func (t *runDynamicCrawlerTool) Description() string {
	return "Run the remote job crawler with a natural-language ranking query for the current Telegram topic or a specific config."
}

func (t *runDynamicCrawlerTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *runDynamicCrawlerTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":     map[string]any{"type": "string", "description": "Natural-language ranking intent, for example: prioritize MLOps in Asia."},
			"limit":     map[string]any{"type": "integer", "description": "Optional per-run result limit override."},
			"key":       map[string]any{"type": "string", "description": "Optional config key. Defaults to the current topic config if available."},
			"channel":   map[string]any{"type": "string", "description": "Optional channel override."},
			"chat_id":   map[string]any{"type": "string", "description": "Optional chat override."},
			"thread_id": map[string]any{"type": "integer", "description": "Optional topic/thread override."},
		},
		"required": []string{"query"},
	}
}

func (t *runDynamicCrawlerTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("job crawler feature is unavailable")
	}

	query := stringArg(args, "query")
	if query == "" {
		return tools.ErrorResult("query is required")
	}

	channel := stringArg(args, "channel")
	if channel == "" {
		channel = tools.ToolChannelFromCtx(ctx)
	}
	chatID, threadID := chatTargetFromToolContext(ctx, stringArg(args, "chat_id"), 0)
	if explicitThreadID, ok := intArg(args, "thread_id"); ok {
		threadID = explicitThreadID
	}

	cfg, err := t.feature.resolveRunConfig(tenantKeyFromCtx(ctx), stringArg(args, "key"), channel, chatID, threadID)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	limit := 0
	if value, ok := intArg(args, "limit"); ok {
		limit = value
	}
	result, err := t.feature.runDynamicCrawler(ctx, cfg, query, limit)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(result)
}
