package telegrampdfautoreview

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *TelegramPDFAutoReviewFeature, router *gateway.MethodRouter) {
	router.Register("beta.telegram_pdf_auto_review.status", feature.handleStatusMethod)
	router.Register("beta.telegram_pdf_auto_review.get_upload", feature.handleGetUploadMethod)
	router.Register("beta.telegram_pdf_auto_review.reprocess", feature.handleReprocessMethod)
}

func (f *TelegramPDFAutoReviewFeature) handleStatusMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	payload, err := f.statusSnapshot(ctx)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func (f *TelegramPDFAutoReviewFeature) handleGetUploadMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		UploadID string `json:"upload_id"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if strings.TrimSpace(params.UploadID) == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "upload_id is required"))
		return
	}

	payload, err := f.getUploadDetails(ctx, params.UploadID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, rpcErrorCode(err), err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func (f *TelegramPDFAutoReviewFeature) handleReprocessMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params ReprocessRequest
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if strings.TrimSpace(params.UploadID) == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "upload_id is required"))
		return
	}

	payload, err := f.reprocessUpload(ctx, client.UserID(), params)
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
