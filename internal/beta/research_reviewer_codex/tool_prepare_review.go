package researchreviewercodex

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type prepareReviewTool struct {
	feature *ResearchReviewerCodexFeature
}

func (t *prepareReviewTool) Name() string { return "research_reviewer_prepare_review" }

func (t *prepareReviewTool) Description() string {
	return "Parse a paper from paper_id, pdf_path, source_url, or raw text; extract structured sections; retrieve local related work; and return a grounded review bundle."
}

func (t *prepareReviewTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"paper_id": map[string]any{
				"type":        "string",
				"description": "Optional ID of a previously indexed paper.",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "Optional title override for raw text or ambiguous sources.",
			},
			"source_url": map[string]any{
				"type":        "string",
				"description": "Optional citation URL such as an arXiv or journal link.",
			},
			"pdf_path": map[string]any{
				"type":        "string",
				"description": "Optional local PDF path. Absolute paths are preferred; relative paths resolve against the current workspace when possible.",
			},
			"paper_text": map[string]any{
				"type":        "string",
				"description": "Optional raw paper text when no URL or PDF path is available.",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{reviewModeCollaborative, reviewModeHarsh},
				"description": "Review tone. `collaborative` is constructive. `harsh` is strict and skeptical.",
			},
			"focus": map[string]any{
				"type":        "string",
				"description": "Optional extra review focus such as novelty, ablations, or writing quality.",
			},
			"top_k_related": map[string]any{
				"type":        "integer",
				"description": "Maximum number of related indexed papers to surface.",
				"minimum":     1,
				"maximum":     maxTopKRelated,
			},
			"force_refresh": map[string]any{
				"type":        "boolean",
				"description": "When true, re-fetch and re-parse URL or PDF inputs even if the paper was already indexed.",
			},
		},
	}
}

func (t *prepareReviewTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("research reviewer feature is unavailable")
	}

	bundle, err := t.feature.prepareReviewBundle(ctx, reviewRequestFromArgs(args))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(formatPreparedBundle(bundle))
}
