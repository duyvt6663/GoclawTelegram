package dailydiscipline

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type submitResponseTool struct {
	feature *DailyDisciplineFeature
}

func (t *submitResponseTool) Name() string { return "daily_discipline_submit" }

func (t *submitResponseTool) Description() string {
	return "Record or update a user's detailed daily discipline response."
}

func (t *submitResponseTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":        map[string]any{"type": "string", "description": "Optional config key. Defaults to the current group config when possible."},
			"date":       map[string]any{"type": "string", "description": "Optional local date in YYYY-MM-DD"},
			"user_id":    map[string]any{"type": "string", "description": "Optional explicit user id. Defaults to the current sender."},
			"user_label": map[string]any{"type": "string", "description": "Optional display label for named summaries"},
			"wake":       map[string]any{"type": "string", "description": "Optional yes/no value"},
			"discipline": map[string]any{"type": "string", "description": "Optional yes/no value"},
			"activity":   map[string]any{"type": "string", "description": "Optional none/gym/run/sport value"},
			"note":       map[string]any{"type": "string", "description": "Optional note"},
		},
	}
}

func (t *submitResponseTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("daily discipline feature is unavailable")
	}

	cfg, err := t.feature.resolveToolConfig(ctx, stringArg(args, "key"))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	localDate, err := resolveLocalDate(cfg, stringArg(args, "date"))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	identity := resolveToolIdentity(ctx, args)
	if identity.ID == "" {
		return tools.ErrorResult("user_id is required")
	}

	var wakePtr *string
	if wake := stringArg(args, "wake"); wake != "" {
		value, ok := normalizeYesNo(wake)
		if !ok {
			return tools.ErrorResult("wake must be yes or no")
		}
		wakePtr = stringPtr(value)
	}
	var disciplinePtr *string
	if discipline := stringArg(args, "discipline"); discipline != "" {
		value, ok := normalizeYesNo(discipline)
		if !ok {
			return tools.ErrorResult("discipline must be yes or no")
		}
		disciplinePtr = stringPtr(value)
	}
	var activityPtr *string
	if activity := stringArg(args, "activity"); activity != "" {
		value, ok := normalizeActivity(activity)
		if !ok {
			return tools.ErrorResult("activity must be none, gym, run, or sport")
		}
		activityPtr = stringPtr(value)
	}

	response, err := t.feature.submitDetailedResponse(ctx, cfg, localDate, identity, wakePtr, disciplinePtr, activityPtr, optionalString(stringArg(args, "note")), "tool")
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(map[string]any{"response": response})
}
