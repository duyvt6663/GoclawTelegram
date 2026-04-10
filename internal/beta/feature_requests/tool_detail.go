package featurerequests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// featureDetailTool shows detailed information about a single feature request.
type featureDetailTool struct {
	feature *FeatureRequestsFeature
}

func (t *featureDetailTool) Name() string { return "feature_detail" }
func (t *featureDetailTool) Description() string {
	return "Show detailed information about a single beta feature request including its full description, " +
		"plan, architecture, build log, approval status, and voters. " +
		"Use when users ask about a specific feature's details or progress."
}

func (t *featureDetailTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"feature_id": map[string]any{
				"type":        "string",
				"description": "The ID of the feature request to inspect",
			},
		},
		"required": []string{"feature_id"},
	}
}

func (t *featureDetailTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	featureID := strings.TrimSpace(tools.GetParamString(args, "feature_id", ""))
	if featureID == "" {
		return tools.ErrorResult("feature_id is required")
	}

	req, err := t.feature.store.getByID(tenantKeyFromCtx(ctx), featureID)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("feature not found: %v", err))
	}

	detail := map[string]any{
		"id":           req.ID,
		"title":        req.Title,
		"description":  req.Description,
		"status":       req.Status,
		"requested_by": req.RequestedBy,
		"approvals":    fmt.Sprintf("%d/%d", req.Approvals, approvalThreshold),
		"voters":       req.Voters,
		"created_at":   req.CreatedAt.Format("2006-01-02 15:04:05"),
		"updated_at":   req.UpdatedAt.Format("2006-01-02 15:04:05"),
	}

	if req.Plan != "" {
		detail["plan"] = req.Plan
	}
	if req.BuildLog != "" {
		detail["build_log"] = req.BuildLog
	}
	if req.PollID != "" {
		detail["poll_id"] = req.PollID
	}

	out, _ := json.MarshalIndent(detail, "", "  ")
	return tools.NewResult(string(out))
}
