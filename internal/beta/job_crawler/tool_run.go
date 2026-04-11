package jobcrawler

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type runCrawlerTool struct {
	feature *JobCrawlerFeature
}

func (t *runCrawlerTool) Name() string { return "job_crawler_run" }

func (t *runCrawlerTool) Description() string {
	return "Run the remote job crawler now for a specific config or the current Telegram topic."
}

func (t *runCrawlerTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *runCrawlerTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":       map[string]any{"type": "string", "description": "Optional config key. Defaults to the current topic config if available."},
			"channel":   map[string]any{"type": "string", "description": "Optional channel override."},
			"chat_id":   map[string]any{"type": "string", "description": "Optional chat override."},
			"thread_id": map[string]any{"type": "integer", "description": "Optional topic/thread override."},
		},
	}
}

func (t *runCrawlerTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("job crawler feature is unavailable")
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
	result, err := t.feature.runCrawler(ctx, cfg, triggerKindManual)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(result)
}
