package dailyiching

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type runTool struct {
	feature *DailyIChingFeature
}

func (t *runTool) Name() string { return "daily_iching_run" }

func (t *runTool) Description() string {
	return "Force the scheduled post, advance to the next hexagram, or post a deeper explanation."
}

func (t *runTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":  map[string]any{"type": "string", "description": "Optional config key. Defaults to the current group config when possible."},
			"mode": map[string]any{"type": "string", "enum": []string{"post", "next", "deeper"}, "description": "Run mode"},
			"date": map[string]any{"type": "string", "description": "Optional local date in YYYY-MM-DD"},
		},
		"required": []string{"mode"},
	}
}

func (t *runTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("daily i ching feature is unavailable")
	}

	cfg, err := t.feature.resolveToolConfig(ctx, stringArg(args, "key"))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	localDate, err := resolveLocalDate(cfg, stringArg(args, "date"))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	mode := stringArg(args, "mode")
	if mode == "" {
		return tools.ErrorResult("mode is required")
	}

	response := map[string]any{
		"mode":       mode,
		"local_date": localDate,
	}
	switch mode {
	case "post":
		delivery, posted, err := t.feature.postNextLesson(ctx, cfg, localDate, triggerKindManual, false)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		response["posted"] = posted
		if delivery != nil {
			response["hexagram"] = delivery.Hexagram
		}
	case "next":
		delivery, posted, err := t.feature.postNextLesson(ctx, cfg, localDate, triggerKindManual, true)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		response["posted"] = posted
		if delivery != nil {
			response["hexagram"] = delivery.Hexagram
		}
	case "deeper":
		delivery, err := t.feature.postDeeperLesson(ctx, cfg, localDate, triggerKindManual)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		response["posted"] = delivery != nil
		if delivery != nil {
			response["hexagram"] = delivery.Hexagram
		}
	default:
		return tools.ErrorResult("mode must be post, next, or deeper")
	}

	status, err := t.feature.statusForConfig(cfg, localDate)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	response["status"] = status
	return toolJSONResult(response)
}
