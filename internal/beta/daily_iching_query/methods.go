package dailyichingquery

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *DailyIChingQueryFeature, router *gateway.MethodRouter) {
	router.Register("beta.daily_iching.query", feature.handleQueryMethod)
}

func (f *DailyIChingQueryFeature) handleQueryMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		Question string `json:"question"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if strings.TrimSpace(params.Question) == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "question is required"))
		return
	}

	payload, err := f.answerQuestion(ctx, tenantKeyFromCtx(ctx), params.Question)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}
