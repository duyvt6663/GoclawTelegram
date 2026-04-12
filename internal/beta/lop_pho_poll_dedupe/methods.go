package lopphopolldedupe

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *LopPhoPollDedupeFeature, router *gateway.MethodRouter) {
	router.Register("beta.lop_pho_poll_dedupe.status", feature.handleStatusMethod)
}

func (f *LopPhoPollDedupeFeature) handleStatusMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params struct {
		ChatID   string `json:"chat_id"`
		ThreadID *int   `json:"thread_id"`
		Limit    int    `json:"limit"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	filter := ClaimFilter{
		ChatID: strings.TrimSpace(params.ChatID),
		Limit:  params.Limit,
	}
	if params.ThreadID != nil {
		filter.ThreadID = normalizeThreadID(*params.ThreadID)
		filter.HasThread = true
	}

	status, err := f.statusSnapshot(ctx, tenantKeyFromCtx(ctx), filter)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, status))
}
