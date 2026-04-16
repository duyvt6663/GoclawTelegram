package jobcrawler

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(feature *JobCrawlerFeature, router *gateway.MethodRouter) {
	router.Register("beta.job_crawler.list", feature.handleListMethod)
	router.Register("beta.job_crawler.get", feature.handleGetMethod)
	router.Register("beta.job_crawler.upsert", feature.handleUpsertMethod)
	router.Register("beta.job_crawler.run", feature.handleRunMethod)
	router.Register("beta.job_crawler.run_dynamic", feature.handleRunDynamicMethod)
}

func (f *JobCrawlerFeature) handleListMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		Key string `json:"key"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	if params.Key != "" {
		cfg, err := f.store.getConfigByKey(tenantKeyFromCtx(ctx), params.Key)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrNotFound, err.Error()))
			return
		}
		status, err := f.statusForConfig(cfg)
		if err != nil {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
			return
		}
		client.SendResponse(protocol.NewOKResponse(req.ID, status))
		return
	}

	statuses, err := f.listStatuses(tenantKeyFromCtx(ctx))
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"configs": statuses}))
}

func (f *JobCrawlerFeature) handleGetMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params struct {
		Key string `json:"key"`
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
	status, err := f.statusForConfig(cfg)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, status))
}

func (f *JobCrawlerFeature) handleUpsertMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
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

func (f *JobCrawlerFeature) handleRunMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params runParams
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	cfg, err := f.resolveRunConfig(tenantKeyFromCtx(ctx), params.Key, params.Channel, params.ChatID, params.threadID())
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}
	result, err := f.runCrawler(ctx, cfg, triggerKindManual)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, result))
}

func (f *JobCrawlerFeature) handleRunDynamicMethod(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	if !permissions.HasMinRole(client.Role(), permissions.RoleOperator) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrUnauthorized, "operator role required"))
		return
	}

	var params dynamicRunParams
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}
	params.Query = strings.TrimSpace(params.Query)
	if params.Query == "" {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "query is required"))
		return
	}

	cfg, err := f.resolveRunConfig(tenantKeyFromCtx(ctx), params.Key, params.Channel, params.ChatID, params.threadID())
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, err.Error()))
		return
	}

	result, err := f.runDynamicCrawler(ctx, cfg, params.Query, params.Limit)
	if err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, err.Error()))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, result))
}

type upsertConfigParams struct {
	Key                       string   `json:"key"`
	Name                      string   `json:"name"`
	Channel                   string   `json:"channel"`
	ChatID                    string   `json:"chat_id"`
	ThreadID                  *int     `json:"thread_id,omitempty"`
	Timezone                  string   `json:"timezone"`
	KeywordsInclude           []string `json:"keywords_include"`
	KeywordsExclude           []string `json:"keywords_exclude"`
	AllowedRoles              []string `json:"allowed_roles"`
	MaxSeniorityLevel         string   `json:"max_seniority_level"`
	RemoteOnly                *bool    `json:"remote_only,omitempty"`
	LocationMode              string   `json:"location_mode"`
	RemotePriority            float64  `json:"remote_priority"`
	VietnamPriority           float64  `json:"vietnam_priority"`
	Sources                   []string `json:"sources"`
	PostTime                  string   `json:"post_time"`
	MaxResults                int      `json:"max_results"`
	DedupeWindowDays          int      `json:"dedupe_window_days"`
	IncludeAISummary          *bool    `json:"include_ai_summary,omitempty"`
	EnableLinkedInProxySource *bool    `json:"enable_linkedin_proxy_source,omitempty"`
	HardTitleFilter           *bool    `json:"hard_title_filter,omitempty"`
	EnableLLMRerank           *bool    `json:"enable_llm_rerank,omitempty"`
	LLMRerankTopN             int      `json:"llm_rerank_top_n"`
	Enabled                   *bool    `json:"enabled,omitempty"`
}

func (p upsertConfigParams) toConfig() JobCrawlerConfig {
	cfg := JobCrawlerConfig{
		Key:               p.Key,
		Name:              p.Name,
		Channel:           p.Channel,
		ChatID:            p.ChatID,
		Timezone:          p.Timezone,
		KeywordsInclude:   p.KeywordsInclude,
		KeywordsExclude:   p.KeywordsExclude,
		AllowedRoles:      p.AllowedRoles,
		MaxSeniorityLevel: p.MaxSeniorityLevel,
		LocationMode:      p.LocationMode,
		RemotePriority:    p.RemotePriority,
		VietnamPriority:   p.VietnamPriority,
		Sources:           p.Sources,
		PostTime:          p.PostTime,
		MaxResults:        p.MaxResults,
		DedupeWindowDays:  p.DedupeWindowDays,
		LLMRerankTopN:     p.LLMRerankTopN,
		Enabled:           true,
	}
	if p.ThreadID != nil {
		cfg.ThreadID = *p.ThreadID
	}
	if p.RemoteOnly != nil {
		cfg.RemoteOnly = *p.RemoteOnly
	}
	if p.IncludeAISummary != nil {
		cfg.IncludeAISummary = *p.IncludeAISummary
	}
	if p.EnableLinkedInProxySource != nil {
		cfg.EnableLinkedInProxySource = *p.EnableLinkedInProxySource
	}
	if p.HardTitleFilter != nil {
		cfg.HardTitleFilter = *p.HardTitleFilter
	}
	if p.EnableLLMRerank != nil {
		cfg.EnableLLMRerank = *p.EnableLLMRerank
	}
	if p.Enabled != nil {
		cfg.Enabled = *p.Enabled
	}
	return cfg
}

type runParams struct {
	Key      string `json:"key"`
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	ThreadID *int   `json:"thread_id,omitempty"`
}

type dynamicRunParams struct {
	Key      string `json:"key"`
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	ThreadID *int   `json:"thread_id,omitempty"`
	Query    string `json:"query"`
	Limit    int    `json:"limit"`
}

func (p runParams) threadID() int {
	if p.ThreadID == nil {
		return 0
	}
	return *p.ThreadID
}

func (p dynamicRunParams) threadID() int {
	if p.ThreadID == nil {
		return 0
	}
	return *p.ThreadID
}
