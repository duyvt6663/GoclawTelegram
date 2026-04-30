package sharedeaterylist

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *SharedEateryListFeature, router *gateway.MethodRouter) {
	router.Register("beta.shared_eatery_list.add", feature.handleAddMethod)
	router.Register("beta.shared_eatery_list.list", feature.handleListMethod)
	router.Register("beta.shared_eatery_list.random", feature.handleRandomMethod)
	router.Register("beta.shared_eatery_list.get", feature.handleGetMethod)
}

func (f *SharedEateryListFeature) handleAddMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params EateryInput
	if err := unmarshalParams(req, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "invalid JSON params"))
		return
	}
	source := sourceMeta{
		ContributorID:    strings.TrimSpace(client.UserID()),
		ContributorLabel: strings.TrimSpace(client.UserID()),
	}
	result, err := f.addEatery(ctx, tenantKeyFromCtx(ctx), params, source)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, result))
}

func (f *SharedEateryListFeature) handleListMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params EateryFilter
	if err := unmarshalParams(req, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "invalid JSON params"))
		return
	}
	result, err := f.listEateries(ctx, tenantKeyFromCtx(ctx), params)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, result))
}

func (f *SharedEateryListFeature) handleRandomMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params EateryFilter
	if err := unmarshalParams(req, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "invalid JSON params"))
		return
	}
	result, err := f.randomEatery(ctx, tenantKeyFromCtx(ctx), params)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, result))
}

func (f *SharedEateryListFeature) handleGetMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		ID string `json:"id"`
	}
	if err := unmarshalParams(req, &params); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "invalid JSON params"))
		return
	}
	if strings.TrimSpace(params.ID) == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "id is required"))
		return
	}
	entry, err := f.store.getEatery(tenantKeyFromCtx(ctx), params.ID)
	if errors.Is(err, errEateryNotFound) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, err.Error()))
		return
	}
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"entry": entry}))
}

func unmarshalParams(req *protocol.RequestFrame, dst any) error {
	if req == nil || len(req.Params) == 0 {
		return nil
	}
	return json.Unmarshal(req.Params, dst)
}
