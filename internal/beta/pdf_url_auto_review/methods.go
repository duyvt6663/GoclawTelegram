package pdfurlautoreview

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *PDFURLAutoReviewFeature, router *gateway.MethodRouter) {
	router.Register("beta.pdf_url_auto_review.status", feature.handleStatusMethod)
	router.Register("beta.pdf_url_auto_review.get_fetch", feature.handleGetFetchMethod)
	router.Register("beta.pdf_url_auto_review.fetch", feature.handleFetchMethod)
}

func (f *PDFURLAutoReviewFeature) handleStatusMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	payload, err := f.statusSnapshot(ctx)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func (f *PDFURLAutoReviewFeature) handleGetFetchMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		FetchID string `json:"fetch_id"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if strings.TrimSpace(params.FetchID) == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "fetch_id is required"))
		return
	}

	payload, err := f.getFetchDetails(ctx, params.FetchID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, rpcErrorCode(err), err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func (f *PDFURLAutoReviewFeature) handleFetchMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params FetchReviewRequest
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if strings.TrimSpace(params.URL) == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "url is required"))
		return
	}

	payload, err := f.FetchAndReview(ctx, client.UserID(), params)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, rpcErrorCode(err), err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func rpcErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case isNotFoundError(err):
		return protocol.ErrNotFound
	case isInputError(err):
		return protocol.ErrInvalidRequest
	default:
		return protocol.ErrInternal
	}
}
