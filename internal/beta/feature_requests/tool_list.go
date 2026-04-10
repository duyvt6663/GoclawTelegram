package featurerequests

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// listFeaturesTool lists all requested beta features and their statuses.
type listFeaturesTool struct {
	feature *FeatureRequestsFeature
}

func (t *listFeaturesTool) Name() string { return "list_features" }
func (t *listFeaturesTool) Description() string {
	return "List all requested beta features with their current status (pending, approved, building, completed, failed, rejected). " +
		"Shows title, status, approval count, and creation date for each feature."
}

func (t *listFeaturesTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{
				"type":        "string",
				"description": "Filter by status: pending, approved, building, completed, failed, rejected. Leave empty for all.",
				"enum":        []string{"pending", "approved", "building", "completed", "failed", "rejected", ""},
			},
		},
	}
}

func (t *listFeaturesTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	statusFilter, _ := args["status"].(string)

	features, err := t.feature.store.list(tenantKeyFromCtx(ctx))
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("failed to list features: %v", err))
	}

	if statusFilter != "" {
		var filtered []FeatureRequest
		for _, f := range features {
			if f.Status == statusFilter {
				filtered = append(filtered, f)
			}
		}
		features = filtered
	}

	if len(features) == 0 {
		return tools.NewResult("No feature requests found.")
	}

	type featureSummary struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		Approvals string `json:"approvals"`
		CreatedAt string `json:"created_at"`
	}

	summaries := make([]featureSummary, len(features))
	for i, f := range features {
		summaries[i] = featureSummary{
			ID:        f.ID,
			Title:     f.Title,
			Status:    f.Status,
			Approvals: fmt.Sprintf("%d/%d", f.Approvals, approvalThreshold),
			CreatedAt: f.CreatedAt.Format("2006-01-02 15:04"),
		}
	}

	out, _ := json.MarshalIndent(summaries, "", "  ")
	return tools.NewResult(string(out))
}
