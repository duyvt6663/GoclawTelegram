package dailydiscipline

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type runTool struct {
	feature *DailyDisciplineFeature
}

func (t *runTool) Name() string { return "daily_discipline_run" }

func (t *runTool) Description() string {
	return "Force a daily discipline survey or summary run for a config."
}

func (t *runTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":  map[string]any{"type": "string", "description": "Optional config key. Defaults to the current group config when possible."},
			"mode": map[string]any{"type": "string", "enum": []string{"survey", "summary"}, "description": "Run mode"},
			"date": map[string]any{"type": "string", "description": "Optional local date in YYYY-MM-DD. Mostly useful for summaries."},
		},
		"required": []string{"mode"},
	}
}

func (t *runTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("daily discipline feature is unavailable")
	}

	cfg, err := t.feature.resolveToolConfig(ctx, stringArg(args, "key"))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	mode := stringArg(args, "mode")
	if mode == "" {
		return tools.ErrorResult("mode is required")
	}
	localDate, err := resolveLocalDate(cfg, stringArg(args, "date"))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	switch mode {
	case "survey":
		err = t.feature.ensureSurveyPosted(ctx, cfg, localDate)
	case "summary":
		err = t.feature.ensureSummaryPosted(ctx, cfg, localDate)
	default:
		return tools.ErrorResult("mode must be survey or summary")
	}
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	status, err := t.feature.statusForConfig(cfg, localDate)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(map[string]any{
		"mode":       mode,
		"local_date": localDate,
		"status":     status,
	})
}
