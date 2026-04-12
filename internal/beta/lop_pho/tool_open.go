package loppho

import (
	"context"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/classroles"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type openVoteTool struct {
	feature *LopPhoFeature
}

func (t *openVoteTool) Name() string { return "vote_lop_pho_open" }

func (t *openVoteTool) Description() string {
	return "Open a Telegram group poll for lớp phó voting with options 'bầu' and 'hạ hạnh kiểm'. Only lớp trưởng / lớp phó can use it."
}

func (t *openVoteTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *openVoteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": map[string]any{
				"type":        "string",
				"description": "Telegram target to vote on. Use @username when possible, or a known display name from this Telegram chat.",
			},
		},
		"required": []string{"target"},
	}
}

func (t *openVoteTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("lớp phó feature is unavailable")
	}
	if tools.ToolPeerKindFromCtx(ctx) != "group" {
		return tools.ErrorResult("vote_lop_pho_open only works in Telegram group chats")
	}

	actor := parseActorIdentity(store.SenderIDFromContext(ctx))
	if actor.SenderID == "" || !classroles.CanActAsLopTruong(ctx, actor.SenderID) {
		return tools.ErrorResult("only lớp trưởng / lớp phó can open lớp phó votes")
	}

	channelName := strings.TrimSpace(tools.ToolChannelFromCtx(ctx))
	if channelName == "" {
		return tools.ErrorResult("current Telegram channel is missing from tool context")
	}
	chatID, threadID := chatTargetFromToolContext(ctx)
	if chatID == "" {
		return tools.ErrorResult("current Telegram chat is missing from tool context")
	}

	result, err := t.feature.openVotePoll(ctx, openVoteInput{
		TenantID:  tenantKeyFromCtx(ctx),
		Channel:   channelName,
		ChatID:    chatID,
		ThreadID:  threadID,
		LocalKey:  composeLocalKey(chatID, threadID),
		TargetRaw: tools.GetParamString(args, "target", ""),
		StartedBy: actor,
		Source:    voteOpenSourceTool,
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(result)
}
