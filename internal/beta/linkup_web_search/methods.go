package linkupwebsearch

import (
	"context"
	"encoding/json"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *LinkupWebSearchFeature, router *gateway.MethodRouter) {
	router.Register("beta.linkup_web_search.search", feature.handleSearchMethod)
}

func (f *LinkupWebSearchFeature) handleSearchMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params SearchRequest
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	payload, err := f.search(ctx, tenantKeyFromCtx(ctx), params)
	if err != nil {
		code := protocol.ErrInternal
		if isSearchInputError(err) {
			code = protocol.ErrInvalidRequest
		}
		client.SendResponse(protocol.NewErrorResponse(req.ID, code, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}
