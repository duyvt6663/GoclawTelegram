package russianroulette

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type playTool struct {
	feature *RussianRouletteFeature
}

func (t *playTool) Name() string { return "roulette_play" }

func (t *playTool) Description() string {
	return "Join, leave, start, or pull the trigger in a Russian roulette round for the current Telegram group/topic or a named config."
}

func (t *playTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":          map[string]any{"type": "string", "description": "Optional roulette config key. Defaults to the current Telegram target when possible."},
			"action":       map[string]any{"type": "string", "enum": []string{"join", "leave", "start", "pull"}, "description": "Roulette action to execute."},
			"user_id":      map[string]any{"type": "string", "description": "Optional explicit player id. Defaults to the current sender."},
			"user_label":   map[string]any{"type": "string", "description": "Optional display label for the player."},
			"chamber_size": map[string]any{"type": "integer", "description": "Optional chamber override when action is start."},
		},
		"required": []string{"action"},
	}
}

func (t *playTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("roulette feature is unavailable")
	}

	cfg, err := t.feature.resolveToolConfig(ctx, stringArg(args, "key"))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	actor := resolveToolIdentity(ctx, args)
	if actor.ID == "" {
		return tools.ErrorResult("user_id is required")
	}

	action := stringArg(args, "action")
	var resp RouletteActionResponse
	switch action {
	case "join":
		resp, err = t.feature.joinRound(cfg, actor)
	case "leave":
		resp, err = t.feature.leaveRound(cfg, actor)
	case "start":
		chamberSize, _ := intArg(args, "chamber_size")
		resp, err = t.feature.startRound(cfg, actor, chamberSize)
	case "pull":
		resp, err = t.feature.pullTrigger(cfg, actor)
	default:
		return tools.ErrorResult("action must be one of: join, leave, start, pull")
	}
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if err := t.feature.announceAction(ctx, &resp); err != nil {
		resp.Warning = err.Error()
	}
	return toolJSONResult(resp)
}
