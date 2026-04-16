package researchreviewercodex

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type searchRelatedPapersTool struct {
	feature *ResearchReviewerCodexFeature
}

func (t *searchRelatedPapersTool) Name() string { return "research_reviewer_search_related_papers" }

func (t *searchRelatedPapersTool) Description() string {
	return "Search the local indexed-paper store for related work using a query or a previously indexed paper_id."
}

func (t *searchRelatedPapersTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"paper_id": map[string]any{
				"type":        "string",
				"description": "Optional indexed paper ID to derive the related-work query from.",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Optional explicit query. Required when paper_id is omitted.",
			},
			"top_k": map[string]any{
				"type":        "integer",
				"description": "Maximum number of related papers to return.",
				"minimum":     1,
				"maximum":     maxTopKRelated,
			},
		},
	}
}

func (t *searchRelatedPapersTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("research reviewer feature is unavailable")
	}

	topK := intArg(args, "top_k")
	if topK <= 0 {
		topK = defaultTopKRelated
	}

	agentInfo, err := t.feature.ensureReviewerAgent(ctx)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	var (
		query   string
		related []RelatedPaper
	)

	paperID := stringArg(args, "paper_id")
	if paperID != "" {
		paper, paperErr := t.feature.store.getPaperByID(tenantKeyFromCtx(ctx), paperID)
		if paperErr != nil {
			return tools.ErrorResult(paperErr.Error())
		}
		structured, structuredErr := paper.structured()
		if structuredErr != nil {
			return tools.ErrorResult(structuredErr.Error())
		}
		query = buildRetrievalQuery(structured)
		related, err = t.feature.searchIndexedRelatedByQuery(ctx, agentInfo, tenantKeyFromCtx(ctx), query, paper.ID, topK)
	} else {
		query = stringArg(args, "query")
		if query == "" {
			return tools.ErrorResult("query is required when paper_id is omitted")
		}
		related, err = t.feature.searchIndexedRelatedByQuery(ctx, agentInfo, tenantKeyFromCtx(ctx), query, "", topK)
	}
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	return tools.NewResult(formatRelatedSearchResults(query, related))
}
