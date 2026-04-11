package russianroulette

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type configureTool struct {
	feature *RussianRouletteFeature
}

func (t *configureTool) Name() string { return "roulette_configure" }

func (t *configureTool) Description() string {
	return "Create or update a Telegram Russian roulette config for a group or topic."
}

func (t *configureTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":                        map[string]any{"type": "string", "description": "Optional config key. Defaults to the current Telegram target."},
			"name":                       map[string]any{"type": "string", "description": "Display name for this roulette table."},
			"channel":                    map[string]any{"type": "string", "description": "Telegram channel instance name. Defaults to the current channel."},
			"chat_id":                    map[string]any{"type": "string", "description": "Telegram chat ID. Defaults to the current chat."},
			"thread_id":                  map[string]any{"type": "integer", "description": "Optional Telegram topic/thread ID. Defaults to the current topic if present."},
			"chamber_size":               map[string]any{"type": "integer", "description": "Default chamber count for new rounds."},
			"turn_cooldown_seconds":      map[string]any{"type": "integer", "description": "Cooldown between turns in seconds."},
			"penalty_mode":               map[string]any{"type": "string", "description": "Penalty mode: none, mute, or tag."},
			"penalty_duration_seconds":   map[string]any{"type": "integer", "description": "Penalty duration in seconds."},
			"penalty_tag":                map[string]any{"type": "string", "description": "Honorary tag text when penalty_mode is tag."},
			"safe_sticker_file_id":       map[string]any{"type": "string", "description": "Optional Telegram sticker file ID for safe pulls."},
			"eliminated_sticker_file_id": map[string]any{"type": "string", "description": "Optional Telegram sticker file ID for eliminations."},
			"winner_sticker_file_id":     map[string]any{"type": "string", "description": "Optional Telegram sticker file ID for winners."},
			"enabled":                    map[string]any{"type": "boolean", "description": "Whether the config is active."},
		},
	}
}

func (t *configureTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("roulette feature is unavailable")
	}

	channel := stringArg(args, "channel")
	if channel == "" {
		channel = tools.ToolChannelFromCtx(ctx)
	}
	chatID, threadID := chatTargetFromToolContext(ctx, stringArg(args, "chat_id"), 0)
	if explicitThreadID, ok := intArg(args, "thread_id"); ok {
		threadID = explicitThreadID
	}
	enabled := true
	if value, ok := boolArg(args, "enabled"); ok {
		enabled = *value
	}

	cfg := RouletteConfig{
		Key:                     stringArg(args, "key"),
		Name:                    stringArg(args, "name"),
		Channel:                 channel,
		ChatID:                  chatID,
		ThreadID:                threadID,
		PenaltyMode:             stringArg(args, "penalty_mode"),
		PenaltyTag:              stringArg(args, "penalty_tag"),
		SafeStickerFileID:       stringArg(args, "safe_sticker_file_id"),
		EliminatedStickerFileID: stringArg(args, "eliminated_sticker_file_id"),
		WinnerStickerFileID:     stringArg(args, "winner_sticker_file_id"),
		Enabled:                 enabled,
	}
	if value, ok := intArg(args, "chamber_size"); ok {
		cfg.ChamberSize = value
	}
	if value, ok := intArg(args, "turn_cooldown_seconds"); ok {
		cfg.TurnCooldownSeconds = value
	}
	if value, ok := intArg(args, "penalty_duration_seconds"); ok {
		cfg.PenaltyDurationSeconds = value
	}

	created, err := t.feature.upsertConfigForTenant(tenantKeyFromCtx(ctx), cfg)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(map[string]any{"config": created})
}
