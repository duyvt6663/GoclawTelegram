package pdfurlautoreview

import (
	"context"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type fetchTool struct {
	feature *PDFURLAutoReviewFeature
}

func (t *fetchTool) Name() string { return fetchToolName }

func (t *fetchTool) Description() string {
	return "Fetch an arXiv, direct PDF, or basic web URL, then run the research-paper reviewer. Use only when the user asks to review a URL."
}

func (t *fetchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The arXiv, direct PDF, or web page URL to fetch and review.",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{reviewModeCollaborative, reviewModeHarsh},
				"description": "Optional review tone override. Defaults to collaborative.",
			},
			"focus": map[string]any{
				"type":        "string",
				"description": "Optional extra review focus such as novelty, baselines, ablations, or writing quality.",
			},
			"force_refresh": map[string]any{
				"type":        "boolean",
				"description": "When true, ignore cached review results in the downstream reviewer pipeline.",
			},
		},
		"required": []string{"url"},
	}
}

func (t *fetchTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("PDF URL auto review feature is unavailable")
	}

	payload, err := t.feature.FetchAndReview(ctx, store.UserIDFromContext(ctx), FetchReviewRequest{
		URL:          stringArg(args, "url"),
		Mode:         stringArg(args, "mode"),
		Focus:        stringArg(args, "focus"),
		ForceRefresh: boolArg(args, "force_refresh"),
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(formatFetchReviewForChat(payload))
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}
