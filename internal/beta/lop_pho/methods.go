package loppho

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *LopPhoFeature, router *gateway.MethodRouter) {
	router.Register("beta.lop_pho.status", feature.handleStatusMethod)
	router.Register("beta.lop_pho.open_vote", feature.handleOpenVoteMethod)
}

func (f *LopPhoFeature) handleStatusMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		Channel    string `json:"channel"`
		ChatID     string `json:"chat_id"`
		ThreadID   *int   `json:"thread_id"`
		ActiveOnly *bool  `json:"active_only"`
		Limit      int    `json:"limit"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	filter := PollFilter{
		Channel:    strings.TrimSpace(params.Channel),
		ChatID:     strings.TrimSpace(params.ChatID),
		ActiveOnly: true,
		Limit:      params.Limit,
	}
	if params.ThreadID != nil {
		filter.ThreadID = normalizeThreadID(*params.ThreadID)
		filter.HasThread = true
	}
	if params.ActiveOnly != nil {
		filter.ActiveOnly = *params.ActiveOnly
	}

	status, err := f.statusSnapshot(tenantKeyFromCtx(ctx), filter)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, status))
}

func (f *LopPhoFeature) handleOpenVoteMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params struct {
		Channel  string `json:"channel"`
		ChatID   string `json:"chat_id"`
		ThreadID int    `json:"thread_id"`
		Target   string `json:"target"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if strings.TrimSpace(params.Channel) == "" || strings.TrimSpace(params.ChatID) == "" || strings.TrimSpace(params.Target) == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "channel, chat_id, and target are required"))
		return
	}

	actorID := strings.TrimSpace(client.UserID())
	if actorID == "" {
		actorID = "rpc-operator"
	}
	result, err := f.openVotePoll(ctx, openVoteInput{
		TenantID:  tenantKeyFromCtx(ctx),
		Channel:   strings.TrimSpace(params.Channel),
		ChatID:    strings.TrimSpace(params.ChatID),
		ThreadID:  normalizeThreadID(params.ThreadID),
		LocalKey:  composeLocalKey(params.ChatID, params.ThreadID),
		TargetRaw: params.Target,
		StartedBy: telegramIdentity{
			UserID:   actorID,
			SenderID: actorID,
			Label:    actorID,
		},
		Source: voteOpenSourceRPC,
	})
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, result))
}
