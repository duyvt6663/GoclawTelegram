package researchreviewercodex

import (
	"context"
	"encoding/json"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *ResearchReviewerCodexFeature, router *gateway.MethodRouter) {
	router.Register("beta.research_reviewer_codex.status", feature.handleStatusMethod)
	router.Register("beta.research_reviewer_codex.review", feature.handleReviewMethod)
	router.Register("beta.research_reviewer_codex.get_review", feature.handleGetReviewMethod)
}

func (f *ResearchReviewerCodexFeature) handleStatusMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	payload, err := f.statusSnapshot(ctx)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func (f *ResearchReviewerCodexFeature) handleReviewMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params ReviewRequest
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	payload, err := f.review(ctx, client.UserID(), params)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, rpcErrorCode(err), err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func (f *ResearchReviewerCodexFeature) handleGetReviewMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		ReviewID string `json:"review_id"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if params.ReviewID == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "review_id is required"))
		return
	}

	payload, err := f.getStoredReview(ctx, params.ReviewID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, rpcErrorCode(err), err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, payload))
}

func rpcErrorCode(err error) string {
	switch {
	case err == nil:
		return protocol.ErrInternal
	case isReviewInputError(err):
		return protocol.ErrInvalidRequest
	case isNotFoundError(err):
		return protocol.ErrNotFound
	default:
		return protocol.ErrInternal
	}
}
