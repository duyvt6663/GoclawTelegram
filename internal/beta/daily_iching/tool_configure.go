package dailyiching

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type configureTool struct {
	feature *DailyIChingFeature
}

func (t *configureTool) Name() string { return "daily_iching_configure" }

func (t *configureTool) Description() string {
	return "Create or update a daily I Ching lesson config for a Telegram group or topic."
}

func (t *configureTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":       map[string]any{"type": "string", "description": "Unique config key, for example kinh-dich-club"},
			"name":      map[string]any{"type": "string", "description": "Display name for the lesson flow"},
			"channel":   map[string]any{"type": "string", "description": "Telegram channel instance name. Defaults to the current channel."},
			"chat_id":   map[string]any{"type": "string", "description": "Telegram chat ID. Defaults to the current chat."},
			"thread_id": map[string]any{"type": "integer", "description": "Optional Telegram topic/thread ID. Defaults to the current topic if present."},
			"timezone":  map[string]any{"type": "string", "description": "IANA timezone, for example Asia/Ho_Chi_Minh"},
			"post_time": map[string]any{"type": "string", "description": "Daily lesson post time in HH:MM"},
			"enabled":   map[string]any{"type": "boolean", "description": "Whether the config is active"},
		},
		"required": []string{"key"},
	}
}

func (t *configureTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("daily i ching feature is unavailable")
	}

	channel := stringArg(args, "channel")
	if channel == "" {
		channel = tools.ToolChannelFromCtx(ctx)
	}
	chatID, threadID := chatTargetFromToolContext(ctx, stringArg(args, "chat_id"), 0)
	if explicitThreadID, ok := intArg(args, "thread_id"); ok {
		threadID = explicitThreadID
	}

	cfg := DailyIChingConfig{
		Key:      stringArg(args, "key"),
		Name:     stringArg(args, "name"),
		Channel:  channel,
		ChatID:   chatID,
		ThreadID: threadID,
		Timezone: stringArg(args, "timezone"),
		PostTime: stringArg(args, "post_time"),
		Enabled:  true,
	}
	if value, ok := boolArg(args, "enabled"); ok {
		cfg.Enabled = *value
	}

	saved, err := t.feature.upsertConfigForTenant(tenantKeyFromCtx(ctx), cfg)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(map[string]any{"config": saved})
}
