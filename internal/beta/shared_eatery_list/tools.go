package sharedeaterylist

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type addEateryTool struct {
	feature *SharedEateryListFeature
}

func (t *addEateryTool) Name() string { return "eatery_add" }

func (t *addEateryTool) Description() string {
	return "Add an eatery recommendation to the shared group eatery list with contributor attribution and duplicate detection."
}

func (t *addEateryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Eatery name.",
			},
			"address": map[string]any{
				"type":        "string",
				"description": "Street address or location text.",
			},
			"map_link": map[string]any{
				"type":        "string",
				"description": "Google Maps or other map link.",
			},
			"district": map[string]any{
				"type":        "string",
				"description": "District or neighborhood for filtering.",
			},
			"category": map[string]any{
				"type":        "string",
				"description": "Cuisine/category such as Vietnamese, Chinese, Korean, Japanese, Thai, buffet.",
			},
			"must_try_dishes": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Dishes the group should try.",
			},
			"contributor": map[string]any{
				"type":        "string",
				"description": "Telegram user label if known. Defaults to the current chat sender.",
			},
			"notes": map[string]any{
				"type":        "string",
				"description": "Extra notes from the contributor.",
			},
			"price_range": map[string]any{
				"type":        "string",
				"description": "Price range label such as cheap, mid, premium, 100k-200k.",
			},
			"image_urls": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional image URLs related to the eatery.",
			},
		},
		"required": []string{"name"},
	}
}

func (t *addEateryTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("shared eatery list feature is unavailable")
	}
	result, err := t.feature.addEatery(ctx, tenantKeyFromCtx(ctx), inputFromToolArgs(args), sourceMetaFromToolContext(ctx))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(result)
}

type listEateriesTool struct {
	feature *SharedEateryListFeature
}

func (t *listEateriesTool) Name() string { return "eatery_list" }

func (t *listEateriesTool) Description() string {
	return "Browse the shared eatery list with optional filters for district, category, price range, or search text."
}

func (t *listEateriesTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"district": map[string]any{
				"type":        "string",
				"description": "Filter by district or neighborhood.",
			},
			"category": map[string]any{
				"type":        "string",
				"description": "Filter by cuisine/category.",
			},
			"price_range": map[string]any{
				"type":        "string",
				"description": "Filter by price range.",
			},
			"search": map[string]any{
				"type":        "string",
				"description": "Search across name, location, dishes, contributor, and notes.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum results to return. Defaults to 25 and caps at 100.",
			},
		},
	}
}

func (t *listEateriesTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("shared eatery list feature is unavailable")
	}
	result, err := t.feature.listEateries(ctx, tenantKeyFromCtx(ctx), filterFromToolArgs(args))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(result)
}

type randomEateryTool struct {
	feature *SharedEateryListFeature
}

func (t *randomEateryTool) Name() string { return "eatery_random" }

func (t *randomEateryTool) Description() string {
	return "Randomly pick an eatery from the shared list with optional district, category, price range, or search filters."
}

func (t *randomEateryTool) Parameters() map[string]any {
	return (&listEateriesTool{}).Parameters()
}

func (t *randomEateryTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("shared eatery list feature is unavailable")
	}
	result, err := t.feature.randomEatery(ctx, tenantKeyFromCtx(ctx), filterFromToolArgs(args))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(result)
}
