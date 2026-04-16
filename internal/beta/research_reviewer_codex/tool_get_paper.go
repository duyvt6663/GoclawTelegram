package researchreviewercodex

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type getIndexedPaperTool struct {
	feature *ResearchReviewerCodexFeature
}

func (t *getIndexedPaperTool) Name() string { return "research_reviewer_get_indexed_paper" }

func (t *getIndexedPaperTool) Description() string {
	return "Retrieve a previously indexed paper by paper_id, including structured sections and an optional full-text excerpt."
}

func (t *getIndexedPaperTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"paper_id": map[string]any{
				"type":        "string",
				"description": "Indexed paper ID returned by research_reviewer_prepare_review or search_related_papers.",
			},
			"include_full_text": map[string]any{
				"type":        "boolean",
				"description": "When true, include a longer full-text excerpt in addition to the structured sections.",
			},
			"max_chars": map[string]any{
				"type":        "integer",
				"description": "Maximum number of characters to include for the full-text excerpt.",
				"minimum":     1000,
				"maximum":     20000,
			},
		},
		"required": []string{"paper_id"},
	}
}

func (t *getIndexedPaperTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("research reviewer feature is unavailable")
	}

	paperID := stringArg(args, "paper_id")
	if paperID == "" {
		return tools.ErrorResult("paper_id is required")
	}

	record, err := t.feature.store.getPaperByID(tenantKeyFromCtx(ctx), paperID)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	content, err := formatIndexedPaperDetail(record, boolArg(args, "include_full_text"), intArg(args, "max_chars"))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(content)
}
