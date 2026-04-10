package featurerequests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// requestFeatureTool creates a new feature request entry.
type requestFeatureTool struct {
	feature *FeatureRequestsFeature
}

func (t *requestFeatureTool) Name() string { return "request_feature" }
func (t *requestFeatureTool) Description() string {
	return "Submit a new beta feature request. Creates an entry that is normally approved by a poll with 5 votes before building, " +
		"though @duyvt6663 (lớp trưởng) can directly approve it. Use when a user suggests or requests a new feature to be built."
}

func (t *requestFeatureTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{
				"type":        "string",
				"description": "Short title for the feature (e.g. 'Reminders', 'Expense Tracker')",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Detailed description of what the feature should do",
			},
		},
		"required": []string{"title", "description"},
	}
}

func (t *requestFeatureTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	title := strings.TrimSpace(tools.GetParamString(args, "title", ""))
	description := strings.TrimSpace(tools.GetParamString(args, "description", ""))

	if title == "" {
		return tools.ErrorResult("title is required")
	}
	if description == "" {
		return tools.ErrorResult("description is required")
	}

	channel := tools.ToolChannelFromCtx(ctx)
	chatID := tools.ToolChatIDFromCtx(ctx)
	localKey := strings.TrimSpace(tools.ToolLocalKeyFromCtx(ctx))

	now := time.Now().UTC()
	req := &FeatureRequest{
		ID:          uuid.New().String(),
		TenantID:    tenantKeyFromCtx(ctx),
		Title:       title,
		Description: description,
		RequestedBy: fmt.Sprintf("%s/%s", channel, chatID),
		Channel:     channel,
		ChatID:      chatID,
		LocalKey:    localKey,
		Status:      StatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := t.feature.store.create(req); err != nil {
		return tools.ErrorResult(fmt.Sprintf("failed to create feature request: %v", err))
	}

	result := map[string]any{
		"id":          req.ID,
		"title":       req.Title,
		"status":      req.Status,
		"message":     fmt.Sprintf("Feature request '%s' created. It usually needs %d approval votes via a poll before building can start, unless @duyvt6663 directly approves it. Use the approve_feature_poll tool to start approval.", title, approvalThreshold),
		"next_action": "Create an approval poll using approve_feature_poll with this feature_id",
	}
	out, _ := json.Marshal(result)
	return tools.NewResult(string(out))
}
