package eaterychat

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type ingestTool struct {
	feature *EateryChatFeature
}

func (t *ingestTool) Name() string { return "eatery_chat_ingest" }

func (t *ingestTool) Description() string {
	return "Parse a free-text eatery recommendation into structured fields."
}

func (t *ingestTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "Free-text eatery recommendation.",
			},
			"confirm": map[string]any{
				"type":        "boolean",
				"description": "Reserved for future confirmation flow.",
			},
		},
		"required": []string{"text"},
	}
}

func (t *ingestTool) Execute(_ context.Context, args map[string]any) *tools.Result {
	text := tools.GetParamString(args, "text", "")
	if text == "" {
		return tools.ErrorResult("text is required")
	}
	return toolJSONResult(parseChatText(text))
}

type confirmTool struct {
	feature *EateryChatFeature
}

func (t *confirmTool) Name() string { return "eatery_chat_confirm" }

func (t *confirmTool) Description() string {
	return "Confirm a pending eatery chat suggestion. Reserved for the full eatery_chat implementation."
}

func (t *confirmTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"suggestion_id": map[string]any{"type": "string"},
		},
		"required": []string{"suggestion_id"},
	}
}

func (t *confirmTool) Execute(context.Context, map[string]any) *tools.Result {
	return tools.ErrorResult("eatery_chat confirmation storage is not implemented yet")
}

type recommendTool struct {
	feature *EateryChatFeature
}

func (t *recommendTool) Name() string { return "eatery_chat_recommend" }

func (t *recommendTool) Description() string {
	return "Parse recommendation constraints from a food prompt. Reserved for the full eatery_chat implementation."
}

func (t *recommendTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{"type": "string"},
		},
		"required": []string{"prompt"},
	}
}

func (t *recommendTool) Execute(_ context.Context, args map[string]any) *tools.Result {
	req := recommendFromToolArgs(args)
	return toolJSONResult(parseRecommendationConstraints(req))
}

type listTool struct {
	feature *EateryChatFeature
}

func (t *listTool) Name() string { return "eatery_chat_list" }

func (t *listTool) Description() string {
	return "List eatery_chat entries. Reserved for the full eatery_chat implementation."
}

func (t *listTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *listTool) Execute(context.Context, map[string]any) *tools.Result {
	return toolJSONResult(ListResult{Entries: nil, Count: 0})
}
