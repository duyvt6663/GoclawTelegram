package dailydiscipline

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type configureTool struct {
	feature *DailyDisciplineFeature
}

func (t *configureTool) Name() string { return "daily_discipline_configure" }

func (t *configureTool) Description() string {
	return "Create or update a daily discipline survey config for a Telegram group or topic."
}

func (t *configureTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":                 map[string]any{"type": "string", "description": "Unique config key, e.g. morning-club"},
			"name":                map[string]any{"type": "string", "description": "Display name for the survey"},
			"channel":             map[string]any{"type": "string", "description": "Telegram channel instance name. Defaults to the current channel."},
			"chat_id":             map[string]any{"type": "string", "description": "Telegram chat ID. Defaults to the current chat."},
			"thread_id":           map[string]any{"type": "integer", "description": "Optional Telegram topic/thread ID. Defaults to the current topic if present."},
			"timezone":            map[string]any{"type": "string", "description": "IANA timezone, e.g. Asia/Ho_Chi_Minh"},
			"survey_window_start": map[string]any{"type": "string", "description": "Survey start time in HH:MM"},
			"survey_window_end":   map[string]any{"type": "string", "description": "Survey end time in HH:MM"},
			"summary_time":        map[string]any{"type": "string", "description": "Summary post time in HH:MM"},
			"target_wake_time":    map[string]any{"type": "string", "description": "Wake-up target time in HH:MM"},
			"wake_question":       map[string]any{"type": "string", "description": "Optional override for the wake poll question"},
			"discipline_question": map[string]any{"type": "string", "description": "Optional override for the discipline poll question"},
			"activity_question":   map[string]any{"type": "string", "description": "Optional override for the activity poll question"},
			"named_results":       map[string]any{"type": "boolean", "description": "Whether summaries should list names"},
			"streaks_enabled":     map[string]any{"type": "boolean", "description": "Whether summaries should include discipline streaks"},
			"dm_details_enabled":  map[string]any{"type": "boolean", "description": "Whether DM /discipline submissions are allowed"},
			"enabled":             map[string]any{"type": "boolean", "description": "Whether the config is active"},
		},
		"required": []string{"key"},
	}
}

func (t *configureTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("daily discipline feature is unavailable")
	}

	channel := stringArg(args, "channel")
	if channel == "" {
		channel = tools.ToolChannelFromCtx(ctx)
	}
	chatID, threadID := chatTargetFromToolContext(ctx, stringArg(args, "chat_id"), 0)
	if explicitThreadID, ok := intArg(args, "thread_id"); ok {
		threadID = explicitThreadID
	}

	cfg := SurveyConfig{
		Key:                stringArg(args, "key"),
		Name:               stringArg(args, "name"),
		Channel:            channel,
		ChatID:             chatID,
		ThreadID:           threadID,
		Timezone:           stringArg(args, "timezone"),
		SurveyWindowStart:  stringArg(args, "survey_window_start"),
		SurveyWindowEnd:    stringArg(args, "survey_window_end"),
		SummaryTime:        stringArg(args, "summary_time"),
		TargetWakeTime:     stringArg(args, "target_wake_time"),
		WakeQuestion:       stringArg(args, "wake_question"),
		DisciplineQuestion: stringArg(args, "discipline_question"),
		ActivityQuestion:   stringArg(args, "activity_question"),
		Enabled:            true,
	}
	if value, ok := boolArg(args, "named_results"); ok {
		cfg.NamedResults = *value
	}
	if value, ok := boolArg(args, "streaks_enabled"); ok {
		cfg.StreaksEnabled = *value
	}
	if value, ok := boolArg(args, "dm_details_enabled"); ok {
		cfg.DMDetailsEnabled = *value
	}
	if value, ok := boolArg(args, "enabled"); ok {
		cfg.Enabled = *value
	}

	created, err := t.feature.upsertConfigForTenant(tenantKeyFromCtx(ctx), cfg)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(map[string]any{"config": created})
}
