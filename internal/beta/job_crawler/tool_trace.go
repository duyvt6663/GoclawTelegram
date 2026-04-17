package jobcrawler

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type getTraceTool struct {
	feature *JobCrawlerFeature
}

func (t *getTraceTool) Name() string { return "job_crawler_get_traces" }

func (t *getTraceTool) Description() string {
	return "Inspect the latest job crawler decision traces for the current Telegram topic, a specific config, or a specific run."
}

func (t *getTraceTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *getTraceTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":          map[string]any{"type": "string", "description": "Optional config key. Defaults to the current topic config if available."},
			"run_id":       map[string]any{"type": "string", "description": "Optional explicit run ID. When provided it overrides key/current topic lookup."},
			"current_only": map[string]any{"type": "boolean", "description": "When true, resolve the current Telegram chat/topic config."},
			"channel":      map[string]any{"type": "string", "description": "Optional channel override."},
			"chat_id":      map[string]any{"type": "string", "description": "Optional chat override."},
			"thread_id":    map[string]any{"type": "integer", "description": "Optional topic/thread override."},
		},
	}
}

func (t *getTraceTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
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
	if value, ok := boolArg(args, "current_only"); ok && !*value {
		chatID = ""
		threadID = 0
	}

	result, err := t.feature.traceResultForRequest(
		tenantKeyFromCtx(ctx),
		stringArg(args, "key"),
		stringArg(args, "run_id"),
		channel,
		chatID,
		threadID,
	)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(result)
}
