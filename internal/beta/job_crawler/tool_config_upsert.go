package jobcrawler

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type configUpsertTool struct {
	feature *JobCrawlerFeature
}

func (t *configUpsertTool) Name() string { return "job_crawler_config_upsert" }

func (t *configUpsertTool) Description() string {
	return "Create or update a daily remote job crawler config for the current Telegram group or topic."
}

func (t *configUpsertTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *configUpsertTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":                          map[string]any{"type": "string", "description": "Optional config key. Defaults to the current chat/topic target."},
			"name":                         map[string]any{"type": "string", "description": "Display name for the job feed."},
			"channel":                      map[string]any{"type": "string", "description": "Telegram channel instance name. Defaults to the current channel."},
			"chat_id":                      map[string]any{"type": "string", "description": "Telegram chat ID. Defaults to the current chat."},
			"thread_id":                    map[string]any{"type": "integer", "description": "Optional Telegram topic/thread ID. Defaults to the current topic if present."},
			"timezone":                     map[string]any{"type": "string", "description": "IANA timezone for the daily post schedule."},
			"keywords_include":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Keywords that should boost and include jobs for this topic."},
			"keywords_exclude":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Keywords that should exclude jobs for this topic."},
			"allowed_roles":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional allowed role IDs such as software_engineer, backend, frontend, fullstack, ai_engineer, or ml_engineer."},
			"max_seniority_level":          map[string]any{"type": "string", "description": "Optional seniority cap: any, junior, mid, senior, staff, principal, or director. Defaults to mid."},
			"remote_only":                  map[string]any{"type": "boolean", "description": "When true, only remote jobs are eligible."},
			"location_mode":                map[string]any{"type": "string", "description": "One of remote_global, vietnam, or hybrid."},
			"remote_priority":              map[string]any{"type": "number", "description": "Optional weight for remote/global jobs."},
			"vietnam_priority":             map[string]any{"type": "number", "description": "Optional weight for Vietnam jobs."},
			"sources":                      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Source IDs such as remoteok, weworkremotely, or linkedin_proxy."},
			"post_time":                    map[string]any{"type": "string", "description": "Daily post time in HH:MM."},
			"max_results":                  map[string]any{"type": "integer", "description": "Maximum jobs to post into the topic."},
			"dedupe_window_days":           map[string]any{"type": "integer", "description": "How many days to suppress reposts for the same job hash."},
			"include_ai_summary":           map[string]any{"type": "boolean", "description": "When true, try to attach a short AI summary per posted job."},
			"enable_linkedin_proxy_source": map[string]any{"type": "boolean", "description": "When true, add LinkedIn public previews sourced through the search proxy into this crawler config."},
			"hard_title_filter":            map[string]any{"type": "boolean", "description": "When true, LinkedIn proxy titles must contain AI, machine learning, ML, LLM, NLP, or computer vision."},
			"enable_llm_rerank":            map[string]any{"type": "boolean", "description": "When true, ask a small LLM to rerank the top semantic matches before posting."},
			"llm_rerank_top_n":             map[string]any{"type": "integer", "description": "How many top-ranked jobs are eligible for optional LLM reranking."},
			"enabled":                      map[string]any{"type": "boolean", "description": "Whether the config is active for the scheduler."},
		},
	}
}

func (t *configUpsertTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("job crawler feature is unavailable")
	}

	channel := stringArg(args, "channel")
	if channel == "" {
		channel = tools.ToolChannelFromCtx(ctx)
	}
	chatID, threadID := chatTargetFromToolContext(ctx, stringArg(args, "chat_id"), 0)
	if explicitThreadID, ok := intArg(args, "thread_id"); ok {
		threadID = explicitThreadID
	}

	cfg := JobCrawlerConfig{
		Key:               stringArg(args, "key"),
		Name:              stringArg(args, "name"),
		Channel:           channel,
		ChatID:            chatID,
		ThreadID:          threadID,
		Timezone:          stringArg(args, "timezone"),
		KeywordsInclude:   stringSliceArg(args, "keywords_include"),
		KeywordsExclude:   stringSliceArg(args, "keywords_exclude"),
		AllowedRoles:      stringSliceArg(args, "allowed_roles"),
		MaxSeniorityLevel: stringArg(args, "max_seniority_level"),
		LocationMode:      stringArg(args, "location_mode"),
		Sources:           stringSliceArg(args, "sources"),
		PostTime:          stringArg(args, "post_time"),
		MaxResults:        0,
		DedupeWindowDays:  0,
		LLMRerankTopN:     0,
		Enabled:           true,
	}
	if value, ok := boolArg(args, "remote_only"); ok {
		cfg.RemoteOnly = *value
	}
	if value, ok := floatArg(args, "remote_priority"); ok {
		cfg.RemotePriority = value
	}
	if value, ok := floatArg(args, "vietnam_priority"); ok {
		cfg.VietnamPriority = value
	}
	if value, ok := intArg(args, "max_results"); ok {
		cfg.MaxResults = value
	}
	if value, ok := intArg(args, "dedupe_window_days"); ok {
		cfg.DedupeWindowDays = value
	}
	if value, ok := boolArg(args, "include_ai_summary"); ok {
		cfg.IncludeAISummary = *value
	}
	if value, ok := boolArg(args, "enable_linkedin_proxy_source"); ok {
		cfg.EnableLinkedInProxySource = *value
	}
	if value, ok := boolArg(args, "hard_title_filter"); ok {
		cfg.HardTitleFilter = *value
	}
	if value, ok := boolArg(args, "enable_llm_rerank"); ok {
		cfg.EnableLLMRerank = *value
	}
	if value, ok := intArg(args, "llm_rerank_top_n"); ok {
		cfg.LLMRerankTopN = value
	}
	if value, ok := boolArg(args, "enabled"); ok {
		cfg.Enabled = *value
	}

	created, err := t.feature.upsertConfigForTenant(tenantKeyFromCtx(ctx), cfg)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return toolJSONResult(map[string]any{"config": created})
}
