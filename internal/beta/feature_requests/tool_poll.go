package featurerequests

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const pollOpenPeriod = 600 // 10 minutes

// featurePollTool creates a Telegram poll for approving a feature request.
type featurePollTool struct {
	feature *FeatureRequestsFeature
}

func (t *featurePollTool) Name() string { return "approve_feature_poll" }
func (t *featurePollTool) Description() string {
	return "Approve a beta feature request in a Telegram group. " +
		"For most users, this creates a poll that needs 5 group votes before the feature can be built. " +
		"When called by @duyvt6663 (lớp trưởng), it directly approves the feature without waiting for the poll."
}

func (t *featurePollTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *featurePollTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"feature_id": map[string]any{
				"type":        "string",
				"description": "The ID of the feature request to create an approval poll for",
			},
		},
		"required": []string{"feature_id"},
	}
}

func (t *featurePollTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	featureID := strings.TrimSpace(tools.GetParamString(args, "feature_id", ""))
	if featureID == "" {
		return tools.ErrorResult("feature_id is required")
	}

	// Only allow in group chats.
	peerKind := tools.ToolPeerKindFromCtx(ctx)
	if peerKind != "group" {
		return tools.ErrorResult("Approval polls can only be created in group chats")
	}

	// Load the feature request.
	req, err := t.feature.store.getByID(tenantKeyFromCtx(ctx), featureID)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("Feature not found: %v", err))
	}

	if canDirectApproveFeature(ctx) {
		if req.Status != StatusPending && req.Status != StatusRejected {
			return tools.ErrorResult(fmt.Sprintf("Feature '%s' is already %s, cannot directly approve it", req.Title, req.Status))
		}

		req.Status = StatusApproved
		if err := t.feature.store.update(req); err != nil {
			return tools.ErrorResult(fmt.Sprintf("Failed to directly approve feature: %v", err))
		}

		result := map[string]any{
			"feature_id":  req.ID,
			"title":       req.Title,
			"status":      req.Status,
			"approved_by": featureRequestsLopTruong,
			"message":     fmt.Sprintf("Feature '%s' was directly approved by %s. It can be built immediately.", req.Title, featureRequestsLopTruong),
		}
		out, _ := json.Marshal(result)
		return tools.NewResult(string(out))
	}

	if req.Status != StatusPending {
		return tools.ErrorResult(fmt.Sprintf("Feature '%s' is already %s, cannot create poll", req.Title, req.Status))
	}
	if req.PollID != "" {
		return tools.NewResult(fmt.Sprintf("Feature '%s' already has an active poll (poll_id=%s). Waiting for %d more vote(s).",
			req.Title, req.PollID, approvalThreshold-req.Approvals))
	}

	channel := strings.TrimSpace(tools.ToolChannelFromCtx(ctx))
	if t.feature.resolve == nil {
		return tools.ErrorResult("No Telegram poll creator available")
	}
	creator := t.feature.resolve(channel)
	if creator == nil {
		return tools.ErrorResult("No Telegram channel available for poll creation")
	}

	// Parse chat ID.
	chatIDStr := strings.TrimSpace(tools.ToolChatIDFromCtx(ctx))
	localKey := strings.TrimSpace(tools.ToolLocalKeyFromCtx(ctx))
	chatID, threadID := parseChatAndThread(chatIDStr, localKey)
	if chatID == 0 {
		return tools.ErrorResult("Cannot determine chat ID for poll creation")
	}

	// Build the poll question.
	question := truncateRunes(fmt.Sprintf("Should we build this feature?\n\n%s\n\n%s", req.Title, truncateRunes(req.Description, 200)), 300)
	yesOption := truncateRunes(fmt.Sprintf("Yes, build it! (%d needed)", approvalThreshold), 100)
	noOption := "Not now"

	pollID, messageID, err := creator.CreateSoDauBaiPoll(ctx, chatID, threadID, question, yesOption, noOption, pollOpenPeriod)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("Failed to create poll: %v", err))
	}

	// Update feature request with poll info.
	req.PollID = pollID
	req.PollMsgID = messageID
	req.Channel = channel
	req.ChatID = chatIDStr
	req.LocalKey = localKey
	if err := t.feature.store.update(req); err != nil {
		return tools.ErrorResult(fmt.Sprintf("Poll created but failed to update feature: %v", err))
	}

	result := map[string]any{
		"feature_id": req.ID,
		"title":      req.Title,
		"poll_id":    pollID,
		"message_id": messageID,
		"threshold":  approvalThreshold,
		"message":    fmt.Sprintf("Approval poll created for '%s'. Need %d votes to approve.", req.Title, approvalThreshold),
	}
	out, _ := json.Marshal(result)
	return tools.NewResult(string(out))
}

// parseChatAndThread extracts the numeric chat ID and thread ID from context strings.
func parseChatAndThread(chatIDStr, localKey string) (chatID int64, threadID int) {
	if chatIDStr == "" {
		return 0, 0
	}
	chatID, _ = strconv.ParseInt(chatIDStr, 10, 64)

	// localKey format: "-100123:topic:42"
	if parts := strings.Split(localKey, ":topic:"); len(parts) == 2 {
		tid, _ := strconv.Atoi(parts[1])
		threadID = tid
	}
	return chatID, threadID
}
