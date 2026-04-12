package loppho

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type statusTool struct {
	feature *LopPhoFeature
}

func (t *statusTool) Name() string { return "lop_pho_status" }

func (t *statusTool) Description() string {
	return "Show granted lớp phó roles and recent vote polls for the current Telegram group/topic."
}

func (t *statusTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *statusTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"active_only": map[string]any{
				"type":        "boolean",
				"description": "When true, only return polls that are still active.",
			},
		},
	}
}

func (t *statusTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("lớp phó feature is unavailable")
	}

	chatID, threadID := chatTargetFromToolContext(ctx)
	filter := PollFilter{
		Channel:    tools.ToolChannelFromCtx(ctx),
		ChatID:     chatID,
		ThreadID:   threadID,
		HasThread:  chatID != "",
		ActiveOnly: true,
		Limit:      20,
	}
	if activeOnly, ok := args["active_only"].(bool); ok {
		filter.ActiveOnly = activeOnly
	}

	status, err := t.feature.statusSnapshot(tenantKeyFromCtx(ctx), filter)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(status)
}
