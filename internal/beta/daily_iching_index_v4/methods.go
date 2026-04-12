package dailyichingindexv4

import (
	"context"
	"encoding/json"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *DailyIChingIndexV4Feature, router *gateway.MethodRouter) {
	router.Register("beta.daily_iching_index_v4.status", feature.handleStatusMethod)
	router.Register("beta.daily_iching_index_v4.compare", feature.handleCompareMethod)
}

func (f *DailyIChingIndexV4Feature) handleStatusMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	payload, err := f.statusPayload(ctx)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func (f *DailyIChingIndexV4Feature) handleCompareMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params struct {
		Queries []string `json:"queries"`
		Rebuild bool     `json:"rebuild"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	payload, err := f.comparePayload(ctx, params.Queries, params.Rebuild)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}
