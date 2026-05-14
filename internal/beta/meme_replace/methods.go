package memereplace

import (
	"context"
	"encoding/json"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *MemeReplaceFeature, router *gateway.MethodRouter) {
	router.Register("beta.meme_replace.replace", feature.handleReplaceMethod)
	router.Register("beta.meme_replace.runs", feature.handleRunsMethod)
}

func (f *MemeReplaceFeature) handleReplaceMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params ReplaceRequest
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	payload, err := f.replace(ctx, params, true)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func (f *MemeReplaceFeature) handleRunsMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	limit := 20
	if req.Params != nil {
		var params struct {
			Limit int `json:"limit"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if params.Limit > 0 && params.Limit <= 100 {
			limit = params.Limit
		}
	}
	runs, err := f.store.listRecentRuns(tenantKeyFromCtx(ctx), limit)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"runs": runs}))
}
