package topictoolrouting

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type upsertParams struct {
	Key             string   `json:"key"`
	Name            string   `json:"name"`
	Channel         string   `json:"channel"`
	ChatID          string   `json:"chat_id"`
	ThreadID        *int     `json:"thread_id,omitempty"`
	EnabledFeatures []string `json:"enabled_features"`
}

func registerMethods(feature *TopicToolRoutingFeature, router *gateway.MethodRouter) {
	router.Register("beta.topic_tool_routing.list", feature.handleListMethod)
	router.Register("beta.topic_tool_routing.upsert", feature.handleUpsertMethod)
	router.Register("beta.topic_tool_routing.resolve", feature.handleResolveMethod)
}

func (f *TopicToolRoutingFeature) handleListMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		Key string `json:"key"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if strings.TrimSpace(params.Key) != "" {
		cfg, err := f.store.getConfigByKey(tenantKeyFromCtx(ctx), params.Key)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, err.Error()))
			return
		}
		client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
			"config":              cfg,
			"registered_features": registeredFeaturesSnapshot(),
		}))
		return
	}

	configs, err := f.store.listConfigs(tenantKeyFromCtx(ctx))
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"configs":             configs,
		"registered_features": registeredFeaturesSnapshot(),
	}))
}

func (f *TopicToolRoutingFeature) handleUpsertMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params upsertParams
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	cfg, err := f.upsertConfigForTenant(tenantKeyFromCtx(ctx), upsertParamsToConfig(params))
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"config": cfg}))
}

func (f *TopicToolRoutingFeature) handleResolveMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		Channel  string `json:"channel"`
		ChatID   string `json:"chat_id"`
		ThreadID *int   `json:"thread_id,omitempty"`
		LocalKey string `json:"local_key"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	threadID := 0
	if params.ThreadID != nil {
		threadID = *params.ThreadID
	}
	snapshot, err := f.resolveSnapshot(ctx, topicrouting.TopicToolScope{
		Channel:  strings.TrimSpace(params.Channel),
		ChatID:   strings.TrimSpace(params.ChatID),
		ThreadID: threadID,
		LocalKey: strings.TrimSpace(params.LocalKey),
	})
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, snapshot))
}
