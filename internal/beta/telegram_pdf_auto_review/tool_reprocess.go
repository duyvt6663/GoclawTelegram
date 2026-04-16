package telegrampdfautoreview

import (
	"context"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type reprocessTool struct {
	feature *TelegramPDFAutoReviewFeature
}

func (t *reprocessTool) Name() string { return "telegram_pdf_auto_review_reprocess" }

func (t *reprocessTool) Description() string {
	return "Review a PDF explicitly attached to the current conversation, or re-run a previous PDF review by upload_id. " +
		"Use this only when the user actually asked for a PDF review."
}

func (t *reprocessTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"upload_id": map[string]any{
				"type":        "string",
				"description": "Optional. The upload_id returned by a previous PDF review run. When omitted, the tool reviews the most recent PDF attached to the current conversation.",
			},
			"media_id": map[string]any{
				"type":        "string",
				"description": "Optional. A specific media_id from a <media:document> tag in the current conversation. Only needed when multiple PDFs are attached.",
			},
			"mode": map[string]any{
				"type":        "string",
				"description": "Optional review mode override: collaborative or harsh. Defaults to collaborative.",
			},
			"focus": map[string]any{
				"type":        "string",
				"description": "Optional extra review focus such as baselines or writing quality.",
			},
			"force_refresh": map[string]any{
				"type":        "boolean",
				"description": "When true, ignore cached review results and re-run prepare + review.",
			},
		},
	}
}

func (t *reprocessTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("telegram PDF auto review feature is unavailable")
	}

	req := ReprocessRequest{
		UploadID:     stringArg(args, "upload_id"),
		Mode:         stringArg(args, "mode"),
		Focus:        stringArg(args, "focus"),
		ForceRefresh: boolArg(args, "force_refresh"),
	}
	mediaID := stringArg(args, "media_id")

	var (
		payload *UploadResultPayload
		err     error
	)
	if req.UploadID != "" {
		payload, err = t.feature.reprocessUpload(ctx, store.UserIDFromContext(ctx), req)
	} else {
		payload, err = t.feature.reviewCurrentConversationPDF(ctx, store.UserIDFromContext(ctx), mediaID, req)
	}
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(formatUploadResultForChat(payload))
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}
