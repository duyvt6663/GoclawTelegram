package russianroulette

import (
	"context"
	"encoding/json"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *RussianRouletteFeature, router *gateway.MethodRouter) {
	router.Register("beta.russian_roulette.list", feature.handleListMethod)
	router.Register("beta.russian_roulette.get", feature.handleGetMethod)
	router.Register("beta.russian_roulette.leaderboard", feature.handleLeaderboardMethod)
	router.Register("beta.russian_roulette.upsert", feature.handleUpsertMethod)
	router.Register("beta.russian_roulette.join", feature.handleJoinMethod)
	router.Register("beta.russian_roulette.start", feature.handleStartMethod)
	router.Register("beta.russian_roulette.pull", feature.handlePullMethod)
	router.Register("beta.russian_roulette.leave", feature.handleLeaveMethod)
}

func (f *RussianRouletteFeature) handleListMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	statuses, err := f.listStatuses(tenantKeyFromCtx(ctx), leaderboardDefaultLimit)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"configs": statuses}))
}

func (f *RussianRouletteFeature) handleGetMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		Key              string `json:"key"`
		LeaderboardLimit int    `json:"leaderboard_limit"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if params.Key == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "key is required"))
		return
	}
	cfg, err := f.store.getConfigByKey(tenantKeyFromCtx(ctx), params.Key)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, err.Error()))
		return
	}
	status, err := f.statusForConfig(cfg, params.LeaderboardLimit)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, status))
}

func (f *RussianRouletteFeature) handleLeaderboardMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		Key   string `json:"key"`
		Limit int    `json:"limit"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if params.Key == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "key is required"))
		return
	}
	cfg, err := f.store.getConfigByKey(tenantKeyFromCtx(ctx), params.Key)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, err.Error()))
		return
	}
	stats, err := f.leaderboardForConfig(cfg, params.Limit)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
		"config":      cfg,
		"leaderboard": stats,
	}))
}

func (f *RussianRouletteFeature) handleUpsertMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}
	var params upsertConfigParams
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	cfg, err := f.upsertConfigForTenant(tenantKeyFromCtx(ctx), params.toConfig())
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"config": cfg}))
}

func (f *RussianRouletteFeature) handleJoinMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	f.handleActionMethod(ctx, client, req, func(cfg *RouletteConfig, actor playerIdentity, params actionParams) (RouletteActionResponse, error) {
		return f.joinRound(cfg, actor)
	})
}

func (f *RussianRouletteFeature) handleStartMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	f.handleActionMethod(ctx, client, req, func(cfg *RouletteConfig, actor playerIdentity, params actionParams) (RouletteActionResponse, error) {
		return f.startRound(cfg, actor, params.ChamberSize)
	})
}

func (f *RussianRouletteFeature) handlePullMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	f.handleActionMethod(ctx, client, req, func(cfg *RouletteConfig, actor playerIdentity, params actionParams) (RouletteActionResponse, error) {
		return f.pullTrigger(cfg, actor)
	})
}

func (f *RussianRouletteFeature) handleLeaveMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	f.handleActionMethod(ctx, client, req, func(cfg *RouletteConfig, actor playerIdentity, params actionParams) (RouletteActionResponse, error) {
		return f.leaveRound(cfg, actor)
	})
}

func (f *RussianRouletteFeature) handleActionMethod(
	ctx context.Context,
	client *gateway.Client,
	req *protocol.RequestFrame,
	fn func(cfg *RouletteConfig, actor playerIdentity, params actionParams) (RouletteActionResponse, error),
) {
	var params actionParams
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if params.Key == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "key is required"))
		return
	}
	cfg, err := f.store.getConfigByKey(tenantKeyFromCtx(ctx), params.Key)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, err.Error()))
		return
	}
	actor := params.identity(client.UserID())
	if actor.ID == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "user_id is required"))
		return
	}
	resp, err := fn(cfg, actor, params)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	if err := f.announceAction(ctx, &resp); err != nil {
		resp.Warning = err.Error()
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, resp))
}

type upsertConfigParams struct {
	Key                     string `json:"key"`
	Name                    string `json:"name"`
	Channel                 string `json:"channel"`
	ChatID                  string `json:"chat_id"`
	ThreadID                *int   `json:"thread_id,omitempty"`
	ChamberSize             int    `json:"chamber_size"`
	TurnCooldownSeconds     int    `json:"turn_cooldown_seconds"`
	PenaltyMode             string `json:"penalty_mode"`
	PenaltyDurationSeconds  int    `json:"penalty_duration_seconds"`
	PenaltyTag              string `json:"penalty_tag"`
	SafeStickerFileID       string `json:"safe_sticker_file_id"`
	EliminatedStickerFileID string `json:"eliminated_sticker_file_id"`
	WinnerStickerFileID     string `json:"winner_sticker_file_id"`
	Enabled                 *bool  `json:"enabled,omitempty"`
}

func (p upsertConfigParams) toConfig() RouletteConfig {
	cfg := RouletteConfig{
		Key:                     p.Key,
		Name:                    p.Name,
		Channel:                 p.Channel,
		ChatID:                  p.ChatID,
		ChamberSize:             p.ChamberSize,
		TurnCooldownSeconds:     p.TurnCooldownSeconds,
		PenaltyMode:             p.PenaltyMode,
		PenaltyDurationSeconds:  p.PenaltyDurationSeconds,
		PenaltyTag:              p.PenaltyTag,
		SafeStickerFileID:       p.SafeStickerFileID,
		EliminatedStickerFileID: p.EliminatedStickerFileID,
		WinnerStickerFileID:     p.WinnerStickerFileID,
		Enabled:                 true,
	}
	if p.ThreadID != nil {
		cfg.ThreadID = *p.ThreadID
	}
	if p.Enabled != nil {
		cfg.Enabled = *p.Enabled
	}
	return cfg
}
