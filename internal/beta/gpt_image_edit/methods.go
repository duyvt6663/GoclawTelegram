package gptimageedit

import (
	"context"
	"encoding/json"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *GPTImageEditFeature, router *gateway.MethodRouter) {
	router.Register("beta.gpt_image_edit.edit", feature.handleEditMethod)
	router.Register("beta.gpt_image_edit.runs", feature.handleRunsMethod)
}

func (f *GPTImageEditFeature) handleEditMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params EditRequest
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	payload, err := f.edit(ctx, params, true)
	if err != nil {
		code := protocol.ErrInternal
		if isEditInputError(err) {
			code = protocol.ErrInvalidRequest
		}
		client.SendResponse(protocol.NewErrorResponse(req.ID, code, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func (f *GPTImageEditFeature) handleRunsMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
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
