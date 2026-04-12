package dailyiching

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *DailyIChingFeature, router *gateway.MethodRouter) {
	router.Register("beta.daily_iching.list", feature.handleListMethod)
	router.Register("beta.daily_iching.upsert", feature.handleUpsertMethod)
	router.Register("beta.daily_iching.run", feature.handleRunMethod)
}

func (f *DailyIChingFeature) handleListMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		Key  string `json:"key"`
		Date string `json:"date"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	tenantID := tenantKeyFromCtx(ctx)
	if params.Key != "" {
		cfg, err := f.store.getConfigByKey(tenantID, params.Key)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, err.Error()))
			return
		}
		localDate, err := resolveLocalDate(cfg, params.Date)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
			return
		}
		status, err := f.statusForConfig(cfg, localDate)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
			return
		}
		client.SendResponse(protocol.NewOKResponse(req.ID, status))
		return
	}

	configs, err := f.store.listConfigs(tenantID)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	statuses := make([]ConfigStatus, 0, len(configs))
	for i := range configs {
		localDate, err := resolveLocalDate(&configs[i], params.Date)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
			return
		}
		status, err := f.statusForConfig(&configs[i], localDate)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
			return
		}
		statuses = append(statuses, status)
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"configs": statuses}))
}

func (f *DailyIChingFeature) handleUpsertMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
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

func (f *DailyIChingFeature) handleRunMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params struct {
		Key  string `json:"key"`
		Mode string `json:"mode"`
		Date string `json:"date"`
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
	localDate, err := resolveLocalDate(cfg, params.Date)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}

	mode := params.Mode
	if mode == "" {
		mode = "post"
	}

	response := map[string]any{
		"mode":       mode,
		"local_date": localDate,
	}
	switch mode {
	case "post":
		delivery, posted, err := f.postNextLesson(ctx, cfg, localDate, triggerKindManual, false)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
			return
		}
		response["posted"] = posted
		if delivery != nil {
			response["hexagram"] = delivery.Hexagram
		}
	case "next":
		delivery, posted, err := f.postNextLesson(ctx, cfg, localDate, triggerKindManual, true)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
			return
		}
		response["posted"] = posted
		if delivery != nil {
			response["hexagram"] = delivery.Hexagram
		}
	case "deeper":
		delivery, err := f.postDeeperLesson(ctx, cfg, localDate, triggerKindManual)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
			return
		}
		response["posted"] = delivery != nil
		if delivery != nil {
			response["hexagram"] = delivery.Hexagram
		}
	default:
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, fmt.Sprintf("unsupported mode %q", mode)))
		return
	}

	status, err := f.statusForConfig(cfg, localDate)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	response["status"] = status
	client.SendResponse(protocol.NewOKResponse(req.ID, response))
}

type upsertConfigParams struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	ThreadID *int   `json:"thread_id,omitempty"`
	Timezone string `json:"timezone"`
	PostTime string `json:"post_time"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

func (p upsertConfigParams) toConfig() DailyIChingConfig {
	cfg := DailyIChingConfig{
		Key:      p.Key,
		Name:     p.Name,
		Channel:  p.Channel,
		ChatID:   p.ChatID,
		Timezone: p.Timezone,
		PostTime: p.PostTime,
		Enabled:  true,
	}
	if p.ThreadID != nil {
		cfg.ThreadID = *p.ThreadID
	}
	if p.Enabled != nil {
		cfg.Enabled = *p.Enabled
	}
	return cfg
}

func (f *DailyIChingFeature) upsertConfigForTenant(tenantID string, cfg DailyIChingConfig) (*DailyIChingConfig, error) {
	cfg = cfg.withDefaults()
	if cfg.Key == "" {
		return nil, fmt.Errorf("key is required")
	}
	if !configKeyRe.MatchString(cfg.Key) {
		return nil, fmt.Errorf("key must use lowercase letters, numbers, and hyphens")
	}
	if strings.TrimSpace(cfg.Channel) == "" {
		return nil, fmt.Errorf("channel is required")
	}
	if strings.TrimSpace(cfg.ChatID) == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	if _, err := parseTimeOfDay(cfg.PostTime); err != nil {
		return nil, fmt.Errorf("post_time: %w", err)
	}
	if loc := loadLocation(cfg.Timezone); loc == time.UTC && strings.TrimSpace(cfg.Timezone) != "" && strings.TrimSpace(cfg.Timezone) != "UTC" {
		return nil, fmt.Errorf("timezone must be a valid IANA timezone")
	}
	cfg.TenantID = tenantID
	saved, err := f.store.upsertConfig(&cfg)
	if err != nil {
		return nil, err
	}
	if _, err := f.store.getOrCreateProgress(saved.TenantID, saved.ID); err != nil {
		return nil, err
	}
	return saved, nil
}
