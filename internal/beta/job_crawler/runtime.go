package jobcrawler

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	memembed "github.com/nextlevelbuilder/goclaw/internal/memory"
)

type ConfigStatus struct {
	Config      JobCrawlerConfig `json:"config"`
	LastRun     *JobCrawlerRun   `json:"last_run,omitempty"`
	RecentPosts int              `json:"recent_posts"`
}

type RunResult struct {
	Config        *JobCrawlerConfig     `json:"config"`
	Run           *JobCrawlerRun        `json:"run"`
	Jobs          []RankedJob           `json:"jobs,omitempty"`
	DynamicQuery  string                `json:"dynamic_query,omitempty"`
	DynamicConfig *DynamicRankingConfig `json:"dynamic_config,omitempty"`
	Posted        bool                  `json:"posted"`
	PostedCount   int                   `json:"posted_count"`
	Warnings      []string              `json:"warnings,omitempty"`
}

type crawlerRunRequest struct {
	TriggerKind   string
	LimitOverride int
	DynamicQuery  string
	DynamicConfig *DynamicRankingConfig
}

func defaultLocationPriorities(mode string) (float64, float64) {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case locationModeRemoteGlobal:
		return 1.0, 0.2
	case locationModeVietnam:
		return 0.2, 1.0
	default:
		return 0.6, 0.4
	}
}

func (f *JobCrawlerFeature) upsertConfigForTenant(tenantID string, cfg JobCrawlerConfig) (*JobCrawlerConfig, error) {
	cfg.TenantID = tenantID
	cfg.Channel = strings.TrimSpace(cfg.Channel)
	cfg.ChatID = strings.TrimSpace(cfg.ChatID)
	cfg.ThreadID = normalizeThreadID(cfg.ThreadID)
	if cfg.Channel == "" {
		cfg.Channel = "telegram"
	}
	if cfg.ChatID == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	if cfg.Key == "" {
		cfg.Key = defaultConfigKey(cfg.Channel, cfg.ChatID, cfg.ThreadID)
	}
	cfg.Key = normalizeConfigKey(cfg.Key)
	if !configKeyRe.MatchString(cfg.Key) {
		return nil, fmt.Errorf("key must match %s", configKeyRe.String())
	}

	mode := strings.TrimSpace(strings.ToLower(cfg.LocationMode))
	switch mode {
	case "", locationModeRemoteGlobal, locationModeVietnam, locationModeHybrid:
		if mode == "" {
			mode = defaultLocationMode
		}
	default:
		return nil, fmt.Errorf("location_mode must be one of %s", strings.Join([]string{
			locationModeRemoteGlobal, locationModeVietnam, locationModeHybrid,
		}, ", "))
	}
	cfg.LocationMode = mode

	if _, err := parseTimeOfDay(cfg.PostTime); err != nil && cfg.PostTime != "" {
		return nil, fmt.Errorf("invalid post_time: %w", err)
	}
	if cfg.Timezone != "" {
		if _, err := time.LoadLocation(cfg.Timezone); err != nil {
			return nil, fmt.Errorf("invalid timezone: %s", cfg.Timezone)
		}
	}
	if cfg.MaxResults < 0 {
		return nil, fmt.Errorf("max_results must be positive")
	}
	if cfg.DedupeWindowDays < 0 {
		return nil, fmt.Errorf("dedupe_window_days must be positive")
	}
	if cfg.LLMRerankTopN < 0 {
		return nil, fmt.Errorf("llm_rerank_top_n must be positive")
	}
	if cfg.RemotePriority < 0 || cfg.VietnamPriority < 0 {
		return nil, fmt.Errorf("priority weights must be positive")
	}
	if cfg.MaxSeniorityLevel != "" && normalizeSeniorityLevel(cfg.MaxSeniorityLevel) == "" {
		return nil, fmt.Errorf("max_seniority_level must be one of %s", strings.Join([]string{
			seniorityAny, seniorityJunior, seniorityMid, senioritySenior, seniorityStaff, seniorityPrincipal, seniorityDirector,
		}, ", "))
	}
	for _, role := range cfg.AllowedRoles {
		if normalizeRoleID(role) == "" {
			return nil, fmt.Errorf("allowed_roles must contain only %s", strings.Join(supportedAllowedRoles(), ", "))
		}
	}

	cfg.KeywordsInclude = normalizeKeywords(cfg.KeywordsInclude)
	cfg.KeywordsExclude = normalizeKeywords(cfg.KeywordsExclude)
	cfg.AllowedRoles = normalizeAllowedRoles(cfg.AllowedRoles)
	if cfg.MaxSeniorityLevel != "" {
		cfg.MaxSeniorityLevel = normalizeSeniorityLevel(cfg.MaxSeniorityLevel)
	}
	cfg.Sources = normalizeSources(cfg.Sources)
	if len(cfg.Sources) == 0 {
		return nil, fmt.Errorf("sources must include at least one of %s", strings.Join(supportedSourceList(), ", "))
	}
	for _, sourceID := range cfg.Sources {
		if _, ok := sourceSpecs[sourceID]; !ok {
			return nil, fmt.Errorf("unsupported source %q", sourceID)
		}
	}

	return f.store.upsertConfig(&cfg)
}

func (f *JobCrawlerFeature) listStatuses(tenantID string) ([]ConfigStatus, error) {
	configs, err := f.store.listConfigs(tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]ConfigStatus, 0, len(configs))
	for i := range configs {
		status, err := f.statusForConfig(&configs[i])
		if err != nil {
			return nil, err
		}
		out = append(out, status)
	}
	return out, nil
}

func (f *JobCrawlerFeature) statusForConfig(cfg *JobCrawlerConfig) (ConfigStatus, error) {
	lastRun, err := f.store.lastRunByConfig(cfg.TenantID, cfg.ID)
	if err != nil && err != errCrawlerRunNotFound {
		return ConfigStatus{}, err
	}
	if err == errCrawlerRunNotFound {
		lastRun = nil
	}
	recentPosts, err := f.store.countRecentPosts(cfg.TenantID, cfg.ID, time.Now().UTC().AddDate(0, 0, -cfg.DedupeWindowDays))
	if err != nil {
		return ConfigStatus{}, err
	}
	return ConfigStatus{
		Config:      *cfg,
		LastRun:     lastRun,
		RecentPosts: recentPosts,
	}, nil
}

func (f *JobCrawlerFeature) resolveRunConfig(tenantID, key, channel, chatID string, threadID int) (*JobCrawlerConfig, error) {
	if key = strings.TrimSpace(key); key != "" {
		return f.store.getConfigByKey(tenantID, key)
	}
	channel = strings.TrimSpace(channel)
	chatID = strings.TrimSpace(chatID)
	threadID = normalizeThreadID(threadID)
	if channel != "" && chatID != "" {
		return f.store.getConfigByTarget(tenantID, channel, chatID, threadID)
	}

	configs, err := f.store.listConfigs(tenantID)
	if err != nil {
		return nil, err
	}
	if len(configs) == 1 {
		return &configs[0], nil
	}
	return nil, fmt.Errorf("key is required when the current topic cannot be resolved")
}

func (f *JobCrawlerFeature) runScheduler(ctx context.Context) {
	defer close(f.schedulerDone)

	f.runDueChecks(ctx, time.Now().UTC())

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			f.runDueChecks(ctx, now.UTC())
		}
	}
}

func (f *JobCrawlerFeature) runDueChecks(ctx context.Context, now time.Time) {
	configs, err := f.store.listEnabledConfigs()
	if err != nil {
		slog.Warn("beta job crawler: failed to list configs", "error", err)
		return
	}

	for i := range configs {
		cfg := configs[i]
		runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := f.runDueConfig(runCtx, &cfg, now); err != nil {
			slog.Warn("beta job crawler: due check failed", "config", cfg.Key, "error", err)
		}
		cancel()
	}
}

func (f *JobCrawlerFeature) runDueConfig(ctx context.Context, cfg *JobCrawlerConfig, now time.Time) error {
	localNow := cfg.localNow(now)
	dueMinute, err := parseTimeOfDay(cfg.PostTime)
	if err != nil {
		return fmt.Errorf("invalid post_time for %s: %w", cfg.Key, err)
	}
	if !withinWindow(localNow.Hour()*60+localNow.Minute(), dueMinute) {
		return nil
	}

	localDate := localNow.Format("2006-01-02")
	done, err := f.store.hasCompletedScheduledRun(cfg.TenantID, cfg.ID, localDate)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	_, err = f.runCrawler(ctx, cfg, triggerKindScheduled)
	return err
}

func (f *JobCrawlerFeature) runDynamicCrawler(ctx context.Context, cfg *JobCrawlerConfig, query string, limit int) (*RunResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	dynamicConfig, warnings := f.parseDynamicQuery(ctx, cfg.TenantID, query)
	result, err := f.runCrawlerRequest(ctx, cfg, crawlerRunRequest{
		TriggerKind:   triggerKindDynamic,
		LimitOverride: limit,
		DynamicQuery:  strings.TrimSpace(query),
		DynamicConfig: &dynamicConfig,
	})
	if result != nil && len(warnings) > 0 {
		result.Warnings = append(result.Warnings, warnings...)
	}
	return result, err
}

func (f *JobCrawlerFeature) runCrawler(ctx context.Context, cfg *JobCrawlerConfig, triggerKind string) (*RunResult, error) {
	return f.runCrawlerRequest(ctx, cfg, crawlerRunRequest{TriggerKind: triggerKind})
}

func (f *JobCrawlerFeature) runCrawlerRequest(ctx context.Context, cfg *JobCrawlerConfig, request crawlerRunRequest) (result *RunResult, retErr error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if request.TriggerKind == "" {
		request.TriggerKind = triggerKindManual
	}
	if !f.tryAcquireRun(cfg.ID) {
		return nil, fmt.Errorf("job crawler %q is already running", cfg.Key)
	}
	defer f.releaseRun(cfg.ID)

	run := &JobCrawlerRun{
		TenantID:    cfg.TenantID,
		ConfigID:    cfg.ID,
		LocalDate:   cfg.localDate(time.Now().UTC()),
		TriggerKind: request.TriggerKind,
		Status:      runStatusRunning,
	}
	if err := f.store.createRun(run); err != nil {
		return nil, err
	}

	result = &RunResult{
		Config:        cfg,
		Run:           run,
		DynamicQuery:  request.DynamicQuery,
		DynamicConfig: request.DynamicConfig,
	}
	defer func() {
		if retErr != nil {
			run.Status = runStatusFailed
			run.ErrorText = retErr.Error()
		}
		if run.Status == "" {
			run.Status = runStatusNoResults
		}
		if err := f.store.finishRun(run); err != nil {
			slog.Warn("beta job crawler: failed to finish run", "config", cfg.Key, "run", run.ID, "error", err)
			if retErr == nil {
				retErr = err
			}
		}
	}()

	now := time.Now().UTC()
	allJobs := make([]JobListing, 0, 64)
	sourceFailures := 0
	for _, sourceID := range cfg.Sources {
		sourceCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		jobs, err := f.fetchJobsForSource(sourceCtx, sourceID)
		cancel()
		if err != nil {
			sourceFailures++
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s failed: %v", sourceID, err))
			continue
		}
		allJobs = append(allJobs, jobs...)
	}
	run.TotalFetched = len(allJobs)
	if len(allJobs) == 0 {
		if sourceFailures == len(cfg.Sources) {
			return result, fmt.Errorf("all configured sources failed")
		}
		run.Status = runStatusNoResults
		return result, nil
	}

	recentlyPosted, err := f.store.recentlyPostedJobs(cfg.TenantID, cfg.ID, now.AddDate(0, 0, -cfg.DedupeWindowDays))
	if err != nil {
		return result, err
	}

	ranked, profileText, rankWarnings, err := f.rankJobs(ctx, cfg, allJobs, recentlyPosted, now, request.DynamicConfig)
	if err != nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, rankWarnings...)
	run.TotalFiltered = len(ranked)
	if len(ranked) == 0 {
		run.Status = runStatusNoResults
		return result, nil
	}

	reranked, rerankWarnings := f.maybeRerankJobs(ctx, cfg, ranked, profileText, request.DynamicConfig)
	result.Warnings = append(result.Warnings, rerankWarnings...)
	ranked = reranked

	limit := cfg.MaxResults
	if request.LimitOverride > 0 {
		limit = request.LimitOverride
	}
	if limit <= 0 {
		limit = defaultMaxResults
	}
	if limit > maxMaxResults {
		limit = maxMaxResults
	}
	if limit > len(ranked) {
		limit = len(ranked)
	}
	top := make([]RankedJob, limit)
	copy(top, ranked[:limit])
	if cfg.IncludeAISummary {
		result.Warnings = append(result.Warnings, f.enrichSummaries(ctx, top)...)
	}
	result.Jobs = top

	content := formatDigestMessage(cfg, top, run.LocalDate, request.DynamicQuery)
	if err := f.postDigest(ctx, cfg, content); err != nil {
		return result, err
	}

	postedAt := now
	for _, job := range top {
		snapshot := SeenJobSnapshot{
			JobHash:            job.JobHash,
			Source:             job.Source,
			Title:              job.Title,
			Company:            job.Company,
			URL:                job.URL,
			NormalizedTitle:    job.NormalizedTitle,
			SeniorityLevel:     job.SeniorityLevel,
			ContentTokens:      append([]string(nil), job.ContentTokens...),
			NormalizedLocation: job.NormalizedLocation,
			IsRemote:           job.IsRemote,
			IsVietnam:          job.IsVietnam,
			Score:              job.Score,
			Summary:            job.ShortSummary,
		}
		if err := f.store.upsertSeenJob(cfg, snapshot, &postedAt); err != nil {
			slog.Warn("beta job crawler: failed to update seen-job snapshot", "config", cfg.Key, "job_hash", job.JobHash, "error", err)
		}
	}

	result.Posted = true
	result.PostedCount = len(top)
	run.TotalPosted = len(top)
	run.Status = runStatusSuccess
	return result, nil
}

func (f *JobCrawlerFeature) rankJobs(ctx context.Context, cfg *JobCrawlerConfig, jobs []JobListing, recentlyPosted []RecentSeenJob, now time.Time, dynamic *DynamicRankingConfig) ([]RankedJob, string, []string, error) {
	maxSeniority := resolveMaxSeniorityRank(cfg)
	if dynamic != nil && dynamic.SeniorityCapOverride != "" {
		maxSeniority = resolveMaxSeniorityRank(&JobCrawlerConfig{MaxSeniorityLevel: dynamic.SeniorityCapOverride})
	}
	candidates := make([]RankedJob, 0, len(jobs))
	warnings := make([]string, 0, 2)
	semanticAvailable := false
	profileText := buildSemanticProfileText(cfg, dynamic)
	jobEmbeddings := make(map[string][]float32)
	var profileEmbedding []float32

	for _, raw := range jobs {
		title := cleanText(raw.Title)
		company := cleanText(raw.Company)
		jobURL := canonicalizeURL(raw.URL)
		if title == "" || company == "" || jobURL == "" {
			continue
		}

		tags := normalizeStringSlice(raw.Tags)
		textTags := strings.Join(tags, " ")
		textBody := cleanText(strings.Join([]string{raw.Description, raw.Location, company}, " "))

		excluded := false
		for _, keyword := range cfg.KeywordsExclude {
			if keywordMatchScore(keyword, title, textTags, textBody) > 0 {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}

		roleEval := evaluateRoleFit(effectiveAllowedRoles(cfg, dynamic), title, textTags, textBody)
		if roleEval.Exclude {
			continue
		}

		seniorityLevel := detectSeniorityLevel(title, textTags)
		if shouldExcludeBySeniority(seniorityLevel, maxSeniority) {
			continue
		}

		location := normalizeLocation(strings.Join([]string{raw.Location, textTags, raw.Description}, " "), raw.AssumeRemote)
		if cfg.RemoteOnly && !location.IsRemote {
			continue
		}

		candidate := RankedJob{
			JobListing: JobListing{
				Source:       raw.Source,
				SourceLabel:  raw.SourceLabel,
				Title:        title,
				Company:      company,
				Location:     raw.Location,
				Tags:         tags,
				URL:          jobURL,
				PostedAt:     raw.PostedAt,
				Description:  raw.Description,
				AssumeRemote: raw.AssumeRemote,
			},
			JobHash:            makeJobHash(title, company, jobURL),
			RoleType:           roleEval.PrimaryRole,
			SeniorityLevel:     seniorityLevel,
			NormalizedLocation: location.Label,
			IsRemote:           location.IsRemote,
			IsVietnam:          location.IsVietnam,
			IsAsia:             location.IsAsia,
			NormalizedTitle:    normalizeTitleForDedupe(title),
			ContentTokens:      contentTokensForJob(title, raw.Description),
		}
		if len(candidate.ContentTokens) == 0 {
			candidate.ContentTokens = contentTokensForJob(title, raw.Location)
		}
		if candidate.RoleType == "" && len(roleEval.MatchedAllowed) > 0 {
			candidate.RoleType = roleEval.MatchedAllowed[0]
		}

		if postedAt := latestRecentPost(candidate, recentlyPosted); postedAt != nil {
			candidate.LastPostedAt = postedAt
			continue
		}
		candidates = append(candidates, candidate)
	}

	if len(candidates) == 0 {
		return nil, profileText, warnings, nil
	}

	embeddingProvider, err := f.resolveEmbeddingProvider(ctx, cfg.TenantID)
	if err != nil {
		warnings = append(warnings, "semantic ranking unavailable: "+err.Error())
	} else if embeddingProvider == nil {
		warnings = append(warnings, "semantic ranking unavailable: no embedding provider configured")
	} else if strings.TrimSpace(profileText) != "" {
		profileHash := memembed.ContentHash(profileText)
		embedCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		profileEmbedding, err = f.embedText(embedCtx, cfg.TenantID, embeddingKindProfile, profileHash, profileText, embeddingProvider)
		cancel()
		if err != nil {
			warnings = append(warnings, "semantic profile embedding failed: "+err.Error())
			profileEmbedding = nil
		}
		if len(profileEmbedding) > 0 {
			semanticAvailable = true
			embedCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			jobEmbeddings, err = f.batchEmbedJobs(embedCtx, cfg.TenantID, candidates, embeddingProvider)
			cancel()
			if err != nil {
				warnings = append(warnings, "job embedding cache warmup failed: "+err.Error())
				jobEmbeddings = make(map[string][]float32)
			}
		}
	}

	scored := make([]RankedJob, 0, len(candidates))
	for _, candidate := range candidates {
		title := cleanText(candidate.Title)
		textTags := strings.Join(candidate.Tags, " ")
		textBody := cleanText(strings.Join([]string{candidate.Description, candidate.Location, candidate.Company}, " "))

		keywordScore, matchedKeywords, includeEligible := computeStaticKeywordScore(cfg, semanticAvailable, title, textTags, textBody)
		if !includeEligible {
			continue
		}

		semanticScore := 0.0
		if len(profileEmbedding) > 0 {
			if vector := jobEmbeddings[candidate.JobHash]; len(vector) > 0 {
				semanticScore = memembed.CosineSimilarity(profileEmbedding, vector)
			}
		}

		roleEval := evaluateRoleFit(effectiveAllowedRoles(cfg, dynamic), title, textTags, textBody)
		roleMatch, rolePenalty := computeRoleMatchAndPenalty(roleEval)
		seniorityPenalty := computeSeniorityPenalty(candidate.SeniorityLevel, maxSeniority)
		locationWeight := computeLocationWeight(cfg, normalizeLocation(strings.Join([]string{candidate.Location, textTags, candidate.Description}, " "), candidate.AssumeRemote))
		recencyWeight := computeRecencyWeight(now, candidate.PostedAt)
		dynamicKeywordBoost, dynamicKeywordMatches := computeDynamicKeywordBoost(dynamic, title, textTags, textBody)
		dynamicRoleBoost := computeRoleBiasBoost(dynamic, candidate.RoleType, roleEval.MatchedAllowed)
		locationBoost, locationPenalty := computeLocationBiasBoost("", normalizedLocation{})
		if dynamic != nil {
			locationBoost, locationPenalty = computeLocationBiasBoost(dynamic.LocationBias, normalizeLocation(strings.Join([]string{candidate.Location, textTags, candidate.Description}, " "), candidate.AssumeRemote))
		}
		dynamicBoost := dynamicKeywordBoost + dynamicRoleBoost + locationBoost
		penalties := rolePenalty + seniorityPenalty + computeDynamicKeywordPenalty(dynamic, title, textTags, textBody) + locationPenalty
		score := semanticScore + keywordScore + locationWeight + roleMatch + recencyWeight + dynamicBoost - penalties
		if score <= 0 {
			continue
		}

		candidate.Score = score
		candidate.SemanticScore = semanticScore
		candidate.KeywordScore = keywordScore
		candidate.LocationWeight = locationWeight
		candidate.RoleMatch = roleMatch
		candidate.RecencyWeight = recencyWeight
		candidate.DynamicBoost = dynamicBoost
		candidate.PenaltyScore = penalties
		candidate.MatchedKeywords = unionKeywordMatches(matchedKeywords, dynamicKeywordMatches)
		scored = append(scored, candidate)
	}

	out := dedupeRankedCandidates(scored)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		switch {
		case out[i].PostedAt == nil && out[j].PostedAt == nil:
			return out[i].Title < out[j].Title
		case out[i].PostedAt == nil:
			return false
		case out[j].PostedAt == nil:
			return true
		default:
			return out[i].PostedAt.After(*out[j].PostedAt)
		}
	})
	return out, profileText, warnings, nil
}

func resolveMaxSeniorityRank(cfg *JobCrawlerConfig) int {
	if cfg == nil {
		return seniorityRank(defaultMaxSeniorityLevel)
	}
	level := normalizeSeniorityLevel(cfg.MaxSeniorityLevel)
	switch level {
	case "", seniorityAny:
		if level == seniorityAny {
			return 0
		}
		return seniorityRank(defaultMaxSeniorityLevel)
	default:
		return seniorityRank(level)
	}
}

func shouldExcludeBySeniority(level string, maxRank int) bool {
	levelRank := seniorityRank(level)
	if maxRank == 0 || levelRank == 0 || levelRank <= maxRank {
		return false
	}
	return levelRank >= seniorityRank(seniorityStaff) || levelRank-maxRank >= 2
}

func seniorityPenaltyForLevel(level string, maxRank int) float64 {
	levelRank := seniorityRank(level)
	if maxRank == 0 || levelRank == 0 || levelRank <= maxRank {
		return 0
	}
	return 1.75 * float64(levelRank-maxRank)
}

func latestRecentPost(candidate RankedJob, recentlyPosted []RecentSeenJob) *time.Time {
	var latest *time.Time
	for _, seen := range recentlyPosted {
		if !sameRecentSeenJob(candidate, seen) {
			continue
		}
		postedAt := seen.LastPostedAt
		if latest == nil || postedAt.After(*latest) {
			ts := postedAt
			latest = &ts
		}
	}
	return latest
}

func dedupeRankedCandidates(candidates []RankedJob) []RankedJob {
	if len(candidates) == 0 {
		return nil
	}

	grouped := make(map[string][]RankedJob, len(candidates))
	for _, candidate := range candidates {
		groupKey := dedupeGroupKey(candidate.Company, candidate.NormalizedTitle)
		grouped[groupKey] = append(grouped[groupKey], candidate)
	}

	out := make([]RankedJob, 0, len(candidates))
	for _, group := range grouped {
		sort.Slice(group, func(i, j int) bool {
			return betterRankedJob(group[i], group[j])
		})

		kept := make([]RankedJob, 0, len(group))
		for _, candidate := range group {
			deduped := false
			for i := range kept {
				if !sameDedupeCluster(candidate, kept[i]) {
					continue
				}
				deduped = true
				if betterRankedJob(candidate, kept[i]) {
					kept[i] = candidate
				}
				break
			}
			if !deduped {
				kept = append(kept, candidate)
			}
		}
		out = append(out, kept...)
	}

	return out
}

func betterRankedJob(candidate, existing RankedJob) bool {
	if candidate.Score != existing.Score {
		return candidate.Score > existing.Score
	}
	switch {
	case candidate.PostedAt == nil && existing.PostedAt == nil:
		return candidate.Title < existing.Title
	case candidate.PostedAt == nil:
		return false
	case existing.PostedAt == nil:
		return true
	default:
		return candidate.PostedAt.After(*existing.PostedAt)
	}
}

func locationScore(cfg *JobCrawlerConfig, location normalizedLocation) float64 {
	switch cfg.LocationMode {
	case locationModeRemoteGlobal:
		if location.IsRemote {
			return cfg.RemotePriority
		}
		if location.IsVietnam {
			return cfg.VietnamPriority * 0.5
		}
		return 0
	case locationModeVietnam:
		if location.IsVietnam {
			return cfg.VietnamPriority
		}
		if location.IsRemote {
			return cfg.RemotePriority * 0.5
		}
		return 0
	default:
		score := 0.0
		if location.IsRemote {
			score += cfg.RemotePriority
		}
		if location.IsVietnam {
			score += cfg.VietnamPriority
		}
		return score
	}
}

func (f *JobCrawlerFeature) enrichSummaries(ctx context.Context, jobs []RankedJob) []string {
	if len(jobs) == 0 {
		return nil
	}
	if f.crawl4ai == nil {
		return []string{"AI summary skipped: GOCLAW_BETA_JOB_CRAWLER_CRAWL4AI_URL is not configured"}
	}

	var warnings []string
	for i := range jobs {
		summaryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		summary, err := f.crawl4ai.SummarizeURL(summaryCtx, jobs[i].URL)
		cancel()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("AI summary failed for %s: %v", jobs[i].Title, err))
			continue
		}
		jobs[i].ShortSummary = summary
	}
	return warnings
}

func formatDigestMessage(cfg *JobCrawlerConfig, jobs []RankedJob, localDate, dynamicQuery string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Job feed for %s on %s\n", cfg.Name, localDate)
	if strings.TrimSpace(dynamicQuery) != "" {
		fmt.Fprintf(&b, "Dynamic ranking: %s\n", trimText(dynamicQuery, 140))
	}
	fmt.Fprintf(&b, "Top %d matches for this topic\n", len(jobs))
	for i, job := range jobs {
		b.WriteString("\n")
		fmt.Fprintf(&b, "%d. %s %s at %s\n", i+1, jobLabelTags(job), job.Title, job.Company)
		if job.NormalizedLocation != "" {
			fmt.Fprintf(&b, "Location: %s\n", job.NormalizedLocation)
		}
		if len(job.Tags) > 0 {
			fmt.Fprintf(&b, "Tags: %s\n", strings.Join(job.Tags[:min(len(job.Tags), 5)], ", "))
		}
		if len(job.MatchedKeywords) > 0 {
			fmt.Fprintf(&b, "Match: %s\n", strings.Join(job.MatchedKeywords, ", "))
		}
		if job.PostedAt != nil {
			fmt.Fprintf(&b, "Posted: %s\n", humanizeAge(time.Now().UTC(), *job.PostedAt))
		}
		fmt.Fprintf(&b, "Source: %s\n", job.SourceLabel)
		if job.ShortSummary != "" {
			fmt.Fprintf(&b, "Summary: %s\n", job.ShortSummary)
		}
		fmt.Fprintf(&b, "%s\n", job.URL)
	}
	return b.String()
}

func jobLabelTags(job RankedJob) string {
	var tags []string
	if job.IsRemote {
		tags = append(tags, "[REMOTE]")
	}
	if job.IsVietnam {
		tags = append(tags, "[VN]")
	} else if job.IsAsia {
		tags = append(tags, "[ASIA]")
	}
	return strings.Join(tags, "")
}

func humanizeAge(now, then time.Time) string {
	delta := now.Sub(then)
	switch {
	case delta < time.Hour:
		minutes := int(delta.Minutes())
		if minutes < 1 {
			minutes = 1
		}
		return fmt.Sprintf("%dm ago", minutes)
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(delta.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(delta.Hours()/24))
	}
}

func (f *JobCrawlerFeature) postDigest(ctx context.Context, cfg *JobCrawlerConfig, content string) error {
	if f == nil || f.channelMgr == nil {
		return fmt.Errorf("channel manager is unavailable")
	}
	channel, ok := f.channelMgr.GetChannel(cfg.Channel)
	if !ok {
		return fmt.Errorf("channel %q is unavailable", cfg.Channel)
	}

	meta := map[string]string{
		"local_key": composeLocalKey(cfg.ChatID, cfg.ThreadID),
	}
	if cfg.ThreadID > 0 {
		meta["message_thread_id"] = strconv.Itoa(cfg.ThreadID)
	}

	return channel.Send(ctx, bus.OutboundMessage{
		Channel:  cfg.Channel,
		ChatID:   cfg.ChatID,
		Content:  content,
		Metadata: meta,
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
