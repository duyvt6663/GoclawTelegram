package jobcrawler

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	memembed "github.com/nextlevelbuilder/goclaw/internal/memory"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	embeddingKindJob     = "job"
	embeddingKindProfile = "profile"

	defaultSemanticEmbeddingModel = "text-embedding-3-small"
	defaultDynamicQueryModel      = "gpt-5-mini"
	defaultLLMRerankTopN          = 8
	maxLLMRerankTopN              = 20
	semanticEmbeddingBatchSize    = 24
	embeddingThrottleInterval     = 350 * time.Millisecond
	llmThrottleInterval           = 650 * time.Millisecond

	locationBiasAsia    = "asia"
	locationBiasVietnam = "vietnam"
	locationBiasRemote  = "remote"
	locationBiasGlobal  = "global"
)

var (
	quotedPhraseRe = regexp.MustCompile(`"([^"]+)"|'([^']+)'`)
)

type DynamicRankingConfig struct {
	Query                string             `json:"query,omitempty"`
	BoostKeywords        []string           `json:"boost_keywords,omitempty"`
	PenaltyKeywords      []string           `json:"penalty_keywords,omitempty"`
	RoleBias             map[string]float64 `json:"role_bias,omitempty"`
	AllowedRoles         []string           `json:"allowed_roles,omitempty"`
	LocationBias         string             `json:"location_bias,omitempty"`
	SeniorityCapOverride string             `json:"seniority_cap_override,omitempty"`
	ParseMode            string             `json:"parse_mode,omitempty"`
}

type resolvedEmbeddingProvider struct {
	provider store.EmbeddingProvider
}

type resolvedLLMProvider struct {
	provider *providers.OpenAIProvider
	model    string
}

type apiCallLimiter struct {
	mu          sync.Mutex
	lastRequest time.Time
	minInterval time.Duration
}

func newAPICallLimiter(minInterval time.Duration) *apiCallLimiter {
	if minInterval <= 0 {
		minInterval = 250 * time.Millisecond
	}
	return &apiCallLimiter{minInterval: minInterval}
}

func (l *apiCallLimiter) Wait(ctx context.Context) error {
	if l == nil || l.minInterval <= 0 {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.lastRequest.IsZero() {
		l.lastRequest = time.Now()
		return nil
	}

	waitFor := l.minInterval - time.Since(l.lastRequest)
	if waitFor > 0 {
		timer := time.NewTimer(waitFor)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}

	l.lastRequest = time.Now()
	return nil
}

func resolveLLMRerankTopN(cfg *JobCrawlerConfig) int {
	if cfg == nil || cfg.LLMRerankTopN <= 0 {
		return defaultLLMRerankTopN
	}
	if cfg.LLMRerankTopN > maxLLMRerankTopN {
		return maxLLMRerankTopN
	}
	return cfg.LLMRerankTopN
}

func dynamicQueryModel() string {
	if model := strings.TrimSpace(os.Getenv("GOCLAW_BETA_JOB_CRAWLER_QUERY_MODEL")); model != "" {
		return model
	}
	return defaultDynamicQueryModel
}

func tenantScopedContext(ctx context.Context, tenantKey string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if store.TenantIDFromContext(ctx) != uuid.Nil {
		return ctx
	}
	if tenantID, err := uuid.Parse(strings.TrimSpace(tenantKey)); err == nil {
		return store.WithTenantID(ctx, tenantID)
	}
	return store.WithTenantID(ctx, store.MasterTenantID)
}

func normalizeOptionalRoles(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		role := normalizeRoleID(value)
		if role == "" {
			continue
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		out = append(out, role)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeRoleBias(values map[string]float64) map[string]float64 {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]float64, len(values))
	for key, weight := range values {
		role := normalizeRoleID(key)
		if role == "" {
			continue
		}
		out[role] = clamp(weight, 0, 1.5)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeLocationBias(value string) string {
	value = normalizeComparableText(value)
	switch {
	case value == "":
		return ""
	case containsPhrase(value, "vietnam"), containsPhrase(value, "hanoi"), containsPhrase(value, "ho chi minh"), containsPhrase(value, "saigon"):
		return locationBiasVietnam
	case containsPhrase(value, "asia"), containsPhrase(value, "apac"), containsPhrase(value, "southeast asia"), containsPhrase(value, "sea"):
		return locationBiasAsia
	case containsPhrase(value, "remote"), containsPhrase(value, "anywhere"), containsPhrase(value, "work from home"), containsPhrase(value, "distributed"):
		return locationBiasRemote
	case containsPhrase(value, "global"), containsPhrase(value, "worldwide"), containsPhrase(value, "international"):
		return locationBiasGlobal
	default:
		return ""
	}
}

func sanitizeDynamicRankingConfig(cfg DynamicRankingConfig) DynamicRankingConfig {
	cfg.Query = strings.TrimSpace(cfg.Query)
	cfg.BoostKeywords = trimKeywordList(cfg.BoostKeywords, 8)
	cfg.PenaltyKeywords = trimKeywordList(cfg.PenaltyKeywords, 8)
	cfg.AllowedRoles = normalizeOptionalRoles(cfg.AllowedRoles)
	cfg.RoleBias = normalizeRoleBias(cfg.RoleBias)
	cfg.LocationBias = normalizeLocationBias(cfg.LocationBias)
	cfg.SeniorityCapOverride = normalizeSeniorityLevel(cfg.SeniorityCapOverride)

	switch strings.TrimSpace(cfg.ParseMode) {
	case "heuristic", "llm", "llm+heuristic":
	default:
		cfg.ParseMode = "heuristic"
	}

	return cfg
}

func trimKeywordList(values []string, limit int) []string {
	values = normalizeKeywords(values)
	if len(values) == 0 {
		return nil
	}
	if limit > 0 && len(values) > limit {
		return append([]string(nil), values[:limit]...)
	}
	return values
}

func mergeDynamicRankingConfig(base, override DynamicRankingConfig) DynamicRankingConfig {
	base = sanitizeDynamicRankingConfig(base)
	override = sanitizeDynamicRankingConfig(override)

	merged := base
	merged.BoostKeywords = trimKeywordList(append(base.BoostKeywords, override.BoostKeywords...), 8)
	merged.PenaltyKeywords = trimKeywordList(append(base.PenaltyKeywords, override.PenaltyKeywords...), 8)
	if len(override.AllowedRoles) > 0 {
		merged.AllowedRoles = append([]string(nil), override.AllowedRoles...)
	}
	if len(base.RoleBias) > 0 || len(override.RoleBias) > 0 {
		merged.RoleBias = make(map[string]float64, len(base.RoleBias)+len(override.RoleBias))
		for role, weight := range base.RoleBias {
			merged.RoleBias[role] = weight
		}
		for role, weight := range override.RoleBias {
			merged.RoleBias[role] = weight
		}
	}
	if override.LocationBias != "" {
		merged.LocationBias = override.LocationBias
	}
	if override.SeniorityCapOverride != "" {
		merged.SeniorityCapOverride = override.SeniorityCapOverride
	}

	switch {
	case override.ParseMode == "llm" && base.ParseMode == "heuristic":
		merged.ParseMode = "llm+heuristic"
	case override.ParseMode != "":
		merged.ParseMode = override.ParseMode
	}
	return sanitizeDynamicRankingConfig(merged)
}

func effectiveAllowedRoles(cfg *JobCrawlerConfig, dynamic *DynamicRankingConfig) []string {
	if dynamic != nil && len(dynamic.AllowedRoles) > 0 {
		return append([]string(nil), dynamic.AllowedRoles...)
	}
	if cfg == nil {
		return append([]string(nil), defaultAllowedRoles...)
	}
	return append([]string(nil), cfg.AllowedRoles...)
}

func effectiveMaxSeniorityLevel(cfg *JobCrawlerConfig, dynamic *DynamicRankingConfig) string {
	if dynamic != nil && dynamic.SeniorityCapOverride != "" {
		return dynamic.SeniorityCapOverride
	}
	if cfg == nil {
		return defaultMaxSeniorityLevel
	}
	return cfg.MaxSeniorityLevel
}

func buildSemanticProfileText(cfg *JobCrawlerConfig, dynamic *DynamicRankingConfig) string {
	if cfg == nil {
		return ""
	}

	var parts []string
	if cfg.Name != "" {
		parts = append(parts, "topic: "+cfg.Name)
	}
	if len(cfg.KeywordsInclude) > 0 {
		parts = append(parts, "keywords: "+strings.Join(cfg.KeywordsInclude, ", "))
	}
	if len(cfg.KeywordsExclude) > 0 {
		parts = append(parts, "avoid: "+strings.Join(cfg.KeywordsExclude, ", "))
	}
	if roles := effectiveAllowedRoles(cfg, dynamic); len(roles) > 0 {
		parts = append(parts, "roles: "+strings.Join(roles, ", "))
	}
	if level := effectiveMaxSeniorityLevel(cfg, dynamic); level != "" {
		parts = append(parts, "seniority cap: "+level)
	}
	if cfg.RemoteOnly {
		parts = append(parts, "remote only")
	} else if cfg.LocationMode != "" {
		parts = append(parts, "location mode: "+cfg.LocationMode)
	}
	if dynamic != nil {
		if dynamic.Query != "" {
			parts = append(parts, "query: "+dynamic.Query)
		}
		if len(dynamic.BoostKeywords) > 0 {
			parts = append(parts, "dynamic boost: "+strings.Join(dynamic.BoostKeywords, ", "))
		}
		if len(dynamic.PenaltyKeywords) > 0 {
			parts = append(parts, "dynamic avoid: "+strings.Join(dynamic.PenaltyKeywords, ", "))
		}
		if dynamic.LocationBias != "" {
			parts = append(parts, "dynamic location bias: "+dynamic.LocationBias)
		}
		if len(dynamic.RoleBias) > 0 {
			roleKeys := make([]string, 0, len(dynamic.RoleBias))
			for role := range dynamic.RoleBias {
				roleKeys = append(roleKeys, role)
			}
			sort.Strings(roleKeys)
			parts = append(parts, "role bias: "+strings.Join(roleKeys, ", "))
		}
	}
	return strings.Join(parts, "\n")
}

func embeddingTextForJob(job JobListing) string {
	parts := []string{
		cleanText(job.Title),
		cleanText(job.Company),
		cleanText(job.Location),
		strings.Join(normalizeStringSlice(job.Tags), ", "),
		trimText(cleanText(job.Description), 2400),
	}
	return strings.Join(filterEmptyStrings(parts), "\n")
}

func filterEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func (f *JobCrawlerFeature) resolveEmbeddingProvider(ctx context.Context, tenantKey string) (*resolvedEmbeddingProvider, error) {
	if f == nil {
		return nil, nil
	}

	tenantCtx := tenantScopedContext(ctx, tenantKey)
	overrideModel := ""
	if f.stores != nil && f.stores.SystemConfigs != nil {
		overrideModel, _ = f.stores.SystemConfigs.Get(tenantCtx, "embedding.model")
		if providerName, _ := f.stores.SystemConfigs.Get(tenantCtx, "embedding.provider"); strings.TrimSpace(providerName) != "" {
			if provider := f.resolveNamedEmbeddingProvider(tenantCtx, providerName, overrideModel); provider != nil {
				return provider, nil
			}
		}
	}

	if f.stores != nil && f.stores.Providers != nil {
		providerList, err := f.stores.Providers.ListProviders(tenantCtx)
		if err != nil {
			return nil, err
		}
		for _, providerData := range providerList {
			if !providerData.Enabled || store.NoEmbeddingTypes[providerData.ProviderType] {
				continue
			}
			settings := store.ParseEmbeddingSettings(providerData.Settings)
			if settings == nil || !settings.Enabled {
				continue
			}
			if provider := f.buildEmbeddingProviderFromData(&providerData, overrideModel); provider != nil {
				return provider, nil
			}
		}
	}

	if f.config != nil && f.config.Providers.OpenAI.APIKey != "" {
		model := strings.TrimSpace(overrideModel)
		if model == "" {
			model = defaultSemanticEmbeddingModel
		}
		ep := memembed.NewOpenAIEmbeddingProvider("openai", f.config.Providers.OpenAI.APIKey, f.config.Providers.OpenAI.APIBase, model)
		return &resolvedEmbeddingProvider{provider: ep}, nil
	}

	return nil, nil
}

func (f *JobCrawlerFeature) resolveNamedEmbeddingProvider(ctx context.Context, providerName, overrideModel string) *resolvedEmbeddingProvider {
	if f == nil || f.stores == nil || f.stores.Providers == nil {
		return nil
	}
	providerData, err := f.stores.Providers.GetProviderByName(ctx, strings.TrimSpace(providerName))
	if err != nil || providerData == nil || !providerData.Enabled || store.NoEmbeddingTypes[providerData.ProviderType] {
		return nil
	}
	return f.buildEmbeddingProviderFromData(providerData, overrideModel)
}

func (f *JobCrawlerFeature) buildEmbeddingProviderFromData(providerData *store.LLMProviderData, overrideModel string) *resolvedEmbeddingProvider {
	if providerData == nil || providerData.APIKey == "" {
		return nil
	}

	model := strings.TrimSpace(overrideModel)
	settings := store.ParseEmbeddingSettings(providerData.Settings)
	if model == "" && settings != nil && settings.Model != "" {
		model = settings.Model
	}
	if model == "" {
		model = defaultSemanticEmbeddingModel
	}

	apiBase := ""
	if settings != nil && settings.APIBase != "" {
		apiBase = settings.APIBase
	}
	if apiBase == "" {
		apiBase = strings.TrimSpace(providerData.APIBase)
	}
	if apiBase == "" && f.config != nil {
		apiBase = f.config.Providers.APIBaseForType(providerData.ProviderType)
	}

	ep := memembed.NewOpenAIEmbeddingProvider(providerData.Name, providerData.APIKey, apiBase, model)
	if settings != nil && settings.Dimensions > 0 {
		ep.WithDimensions(settings.Dimensions)
	}
	return &resolvedEmbeddingProvider{provider: ep}
}

func (f *JobCrawlerFeature) resolveDynamicLLM(ctx context.Context, tenantKey string) (*resolvedLLMProvider, error) {
	if f == nil {
		return nil, nil
	}
	model := dynamicQueryModel()
	tenantCtx := tenantScopedContext(ctx, tenantKey)

	if f.stores != nil && f.stores.Providers != nil {
		if providerData, err := f.stores.Providers.GetProviderByName(tenantCtx, "openai"); err == nil && providerData != nil {
			if provider := f.buildDynamicLLMFromData(providerData, model); provider != nil {
				return provider, nil
			}
		}

		providerList, err := f.stores.Providers.ListProviders(tenantCtx)
		if err != nil {
			return nil, err
		}
		for _, providerData := range providerList {
			if provider := f.buildDynamicLLMFromData(&providerData, model); provider != nil {
				return provider, nil
			}
		}
	}

	if f.config != nil && f.config.Providers.OpenAI.APIKey != "" {
		provider := providers.NewOpenAIProvider("openai", f.config.Providers.OpenAI.APIKey, f.config.Providers.OpenAI.APIBase, model)
		return &resolvedLLMProvider{provider: provider, model: model}, nil
	}

	return nil, nil
}

func (f *JobCrawlerFeature) buildDynamicLLMFromData(providerData *store.LLMProviderData, model string) *resolvedLLMProvider {
	if providerData == nil || !providerData.Enabled || providerData.APIKey == "" {
		return nil
	}

	name := strings.ToLower(strings.TrimSpace(providerData.Name))
	apiBase := strings.ToLower(strings.TrimSpace(providerData.APIBase))
	if name != "openai" && !strings.Contains(name, "openai") && !strings.Contains(apiBase, "api.openai.com") {
		return nil
	}

	base := strings.TrimSpace(providerData.APIBase)
	if base == "" && f.config != nil {
		base = f.config.Providers.APIBaseForType(providerData.ProviderType)
	}

	provider := providers.NewOpenAIProvider(providerData.Name, providerData.APIKey, base, model)
	return &resolvedLLMProvider{provider: provider, model: model}
}

func (f *JobCrawlerFeature) cachedSemanticEmbedding(tenantID, kind, subjectHash, providerName, model, contentHash string) ([]float32, error) {
	if f == nil || f.store == nil {
		return nil, nil
	}
	return f.store.cachedEmbedding(tenantID, kind, subjectHash, providerName, model, contentHash)
}

func (f *JobCrawlerFeature) embedText(ctx context.Context, tenantID, kind, subjectHash, text string, provider *resolvedEmbeddingProvider) ([]float32, error) {
	if f == nil || provider == nil || provider.provider == nil {
		return nil, nil
	}

	contentHash := memembed.ContentHash(text)
	cached, err := f.cachedSemanticEmbedding(tenantID, kind, subjectHash, provider.provider.Name(), provider.provider.Model(), contentHash)
	if err != nil {
		return nil, err
	}
	if len(cached) > 0 {
		return cached, nil
	}

	if f.embeddingLimiter != nil {
		if err := f.embeddingLimiter.Wait(ctx); err != nil {
			return nil, err
		}
	}
	embeddings, err := provider.provider.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 || len(embeddings[0]) == 0 {
		return nil, fmt.Errorf("embedding provider returned no vector")
	}
	if f.store != nil {
		if err := f.store.upsertEmbedding(tenantID, kind, subjectHash, contentHash, provider.provider.Name(), provider.provider.Model(), embeddings[0]); err != nil {
			return nil, err
		}
	}
	return embeddings[0], nil
}

func (f *JobCrawlerFeature) batchEmbedJobs(ctx context.Context, tenantID string, jobs []RankedJob, provider *resolvedEmbeddingProvider) (map[string][]float32, error) {
	if f == nil || provider == nil || provider.provider == nil || len(jobs) == 0 {
		return nil, nil
	}

	result := make(map[string][]float32, len(jobs))
	type pendingEmbedding struct {
		JobHash     string
		ContentHash string
		Text        string
	}

	pending := make([]pendingEmbedding, 0, len(jobs))
	for _, job := range jobs {
		text := embeddingTextForJob(job.JobListing)
		contentHash := memembed.ContentHash(text)
		cached, err := f.cachedSemanticEmbedding(tenantID, embeddingKindJob, job.JobHash, provider.provider.Name(), provider.provider.Model(), contentHash)
		if err != nil {
			return nil, err
		}
		if len(cached) > 0 {
			result[job.JobHash] = cached
			continue
		}
		pending = append(pending, pendingEmbedding{
			JobHash:     job.JobHash,
			ContentHash: contentHash,
			Text:        text,
		})
	}

	for start := 0; start < len(pending); start += semanticEmbeddingBatchSize {
		end := start + semanticEmbeddingBatchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[start:end]
		inputs := make([]string, 0, len(batch))
		for _, item := range batch {
			inputs = append(inputs, item.Text)
		}

		if f.embeddingLimiter != nil {
			if err := f.embeddingLimiter.Wait(ctx); err != nil {
				return nil, err
			}
		}
		embeddings, err := provider.provider.Embed(ctx, inputs)
		if err != nil {
			return nil, err
		}
		if len(embeddings) != len(batch) {
			return nil, fmt.Errorf("embedding provider returned %d vectors for %d inputs", len(embeddings), len(batch))
		}
		for i, item := range batch {
			vector := embeddings[i]
			if len(vector) == 0 {
				continue
			}
			result[item.JobHash] = vector
			if f.store != nil {
				if err := f.store.upsertEmbedding(tenantID, embeddingKindJob, item.JobHash, item.ContentHash, provider.provider.Name(), provider.provider.Model(), vector); err != nil {
					return nil, err
				}
			}
		}
	}

	return result, nil
}

func (f *JobCrawlerFeature) parseDynamicQuery(ctx context.Context, tenantID, query string) (DynamicRankingConfig, []string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return DynamicRankingConfig{}, nil
	}

	heuristic := heuristicDynamicRankingConfig(query)
	warnings := make([]string, 0, 2)

	llmCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	llmParsed, err := f.parseDynamicQueryWithLLM(llmCtx, tenantID, query)
	if err != nil {
		warnings = append(warnings, "dynamic parser fell back to heuristics: "+err.Error())
		return heuristic, warnings
	}
	if llmParsed.ParseMode == "" {
		return heuristic, warnings
	}
	return mergeDynamicRankingConfig(heuristic, llmParsed), warnings
}

func heuristicDynamicRankingConfig(query string) DynamicRankingConfig {
	normalized := normalizeComparableText(query)
	cfg := DynamicRankingConfig{
		Query:     strings.TrimSpace(query),
		ParseMode: "heuristic",
	}
	cfg.LocationBias = detectHeuristicLocationBias(normalized)
	cfg.SeniorityCapOverride = detectHeuristicSeniority(normalized)

	restrictRoles := hasAnyPhrase(normalized, "only", "only show", "show only", "strictly", "focus only", "just")
	roles := matchedRoles(normalized)
	if len(roles) > 0 {
		cfg.RoleBias = make(map[string]float64, len(roles))
		for _, role := range roles {
			cfg.RoleBias[role] = 0.85
		}
		if restrictRoles {
			cfg.AllowedRoles = append([]string(nil), roles...)
		}
	}

	cfg.BoostKeywords = extractQueryKeywords(query, []string{
		"prioritize", "focus on", "boost", "prefer", "target", "looking for", "interested in",
	})
	cfg.PenaltyKeywords = extractQueryKeywords(query, []string{
		"avoid", "exclude", "deprioritize", "skip",
	})

	for _, match := range quotedPhraseRe.FindAllStringSubmatch(query, -1) {
		for _, group := range match[1:] {
			if group != "" {
				cfg.BoostKeywords = append(cfg.BoostKeywords, group)
			}
		}
	}

	return sanitizeDynamicRankingConfig(cfg)
}

func detectHeuristicLocationBias(normalized string) string {
	switch {
	case hasAnyPhrase(normalized, "vietnam", "hanoi", "ho chi minh", "saigon", "da nang"):
		return locationBiasVietnam
	case hasAnyPhrase(normalized, "asia", "apac", "southeast asia", "sea", "singapore", "japan", "india", "hong kong"):
		return locationBiasAsia
	case hasAnyPhrase(normalized, "remote", "anywhere", "work from home", "distributed"):
		return locationBiasRemote
	case hasAnyPhrase(normalized, "global", "worldwide", "international"):
		return locationBiasGlobal
	default:
		return ""
	}
}

func detectHeuristicSeniority(normalized string) string {
	switch {
	case hasAnyPhrase(normalized, "director", "head", "vice president", "vp", "chief", "cto"):
		return seniorityDirector
	case hasAnyPhrase(normalized, "principal", "architect"):
		return seniorityPrincipal
	case hasAnyPhrase(normalized, "staff", "lead", "tech lead"):
		return seniorityStaff
	case hasAnyPhrase(normalized, "senior", "sr"):
		return senioritySenior
	case hasAnyPhrase(normalized, "mid", "mid level", "intermediate"):
		return seniorityMid
	case hasAnyPhrase(normalized, "junior", "jr", "entry", "graduate", "new grad", "intern", "associate"):
		return seniorityJunior
	default:
		return ""
	}
}

func extractQueryKeywords(query string, cues []string) []string {
	lower := strings.ToLower(query)
	var keywords []string
	for _, cue := range cues {
		idx := strings.Index(lower, cue)
		if idx < 0 {
			continue
		}
		segment := query[idx+len(cue):]
		if clipped := clipKeywordSegment(segment); clipped != "" {
			keywords = append(keywords, splitKeywordSegment(clipped)...)
		}
	}
	return trimKeywordList(keywords, 8)
}

func clipKeywordSegment(segment string) string {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return ""
	}
	lower := strings.ToLower(segment)
	for _, stop := range []string{
		" in ", " near ", " within ", " around ", " but ", " while ", " and avoid ", " and exclude ",
		";", ".", "\n",
	} {
		if idx := strings.Index(lower, stop); idx >= 0 {
			segment = segment[:idx]
			break
		}
	}
	return strings.TrimSpace(segment)
}

func splitKeywordSegment(segment string) []string {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return nil
	}
	replacer := strings.NewReplacer(
		" and ", ",",
		" or ", ",",
		"/", ",",
		"|", ",",
		"jobs", "",
		"roles", "",
		"candidates", "",
		"people", "",
	)
	segment = replacer.Replace(strings.ToLower(segment))

	parts := strings.Split(segment, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func (f *JobCrawlerFeature) parseDynamicQueryWithLLM(ctx context.Context, tenantID, query string) (DynamicRankingConfig, error) {
	response, err := f.callDynamicLLM(ctx, tenantID, `You parse natural-language job ranking requests.
Return strict JSON with this shape:
{
  "boost_keywords": ["..."],
  "penalty_keywords": ["..."],
  "role_bias": {"backend": 0.8},
  "allowed_roles": ["backend"],
  "location_bias": "asia",
  "seniority_cap_override": "mid"
}

Rules:
- allowed_roles values must be from: software_engineer, backend, frontend, fullstack, ai_engineer, ml_engineer
- location_bias must be one of: "", asia, vietnam, remote, global
- seniority_cap_override must be one of: "", any, junior, mid, senior, staff, principal, director
- use empty arrays/objects/strings when unspecified
- do not explain the output`, query, 280)
	if err != nil {
		return DynamicRankingConfig{}, err
	}

	config, err := decodeDynamicRankingConfigJSON(response)
	if err != nil {
		return DynamicRankingConfig{}, err
	}
	config.Query = query
	config.ParseMode = "llm"
	return sanitizeDynamicRankingConfig(config), nil
}

func (f *JobCrawlerFeature) callDynamicLLM(ctx context.Context, tenantID, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	llm, err := f.resolveDynamicLLM(ctx, tenantID)
	if err != nil {
		return "", err
	}
	if llm == nil || llm.provider == nil {
		return "", fmt.Errorf("no OpenAI provider configured")
	}

	if f.llmLimiter != nil {
		if err := f.llmLimiter.Wait(ctx); err != nil {
			return "", err
		}
	}

	resp, err := llm.provider.Chat(ctx, providers.ChatRequest{
		Model: llm.model,
		Messages: []providers.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Options: map[string]any{
			providers.OptTemperature: 0.1,
			providers.OptMaxTokens:   maxTokens,
		},
	})
	if err != nil {
		return "", err
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return "", fmt.Errorf("LLM returned no content")
	}
	return strings.TrimSpace(resp.Content), nil
}

func decodeDynamicRankingConfigJSON(raw string) (DynamicRankingConfig, error) {
	payload := extractJSONBlock(raw, '{', '}')
	if payload == "" {
		return DynamicRankingConfig{}, fmt.Errorf("no JSON object found")
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return DynamicRankingConfig{}, err
	}

	config := DynamicRankingConfig{
		BoostKeywords:        anyStringSlice(decoded["boost_keywords"]),
		PenaltyKeywords:      anyStringSlice(decoded["penalty_keywords"]),
		AllowedRoles:         anyStringSlice(decoded["allowed_roles"]),
		LocationBias:         anyString(decoded["location_bias"]),
		SeniorityCapOverride: anyString(decoded["seniority_cap_override"]),
		ParseMode:            "llm",
	}
	if roleBias, ok := decoded["role_bias"].(map[string]any); ok {
		config.RoleBias = make(map[string]float64, len(roleBias))
		for role, rawWeight := range roleBias {
			config.RoleBias[role] = anyFloat(rawWeight)
		}
	}
	return sanitizeDynamicRankingConfig(config), nil
}

func anyString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func anyStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []string{typed}
	default:
		return nil
	}
}

func anyFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		f, _ := typed.Float64()
		return f
	case bool:
		if typed {
			return 1
		}
		return 0
	default:
		return 0
	}
}

func extractJSONBlock(raw string, opener, closer rune) string {
	start := strings.IndexRune(raw, opener)
	end := strings.LastIndex(raw, string(closer))
	if start < 0 || end <= start {
		return ""
	}
	return strings.TrimSpace(raw[start : end+1])
}

func encodeFloat32Slice(values []float32) string {
	if len(values) == 0 {
		return "[]"
	}
	data, _ := json.Marshal(values)
	return string(data)
}

func decodeFloat32Slice(value string) []float32 {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var out []float32
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		return nil
	}
	return out
}

func computeLocationBiasBoost(bias string, location normalizedLocation) (boost, penalty float64) {
	switch normalizeLocationBias(bias) {
	case locationBiasVietnam:
		switch {
		case location.IsVietnam:
			return 0.18, 0
		case location.IsAsia:
			return 0.04, 0
		default:
			return 0, 0.03
		}
	case locationBiasAsia:
		switch {
		case location.IsVietnam || location.IsAsia:
			return 0.16, 0
		case location.IsRemote:
			return 0.05, 0
		default:
			return 0, 0.025
		}
	case locationBiasRemote:
		if location.IsRemote {
			return 0.14, 0
		}
		return 0, 0.03
	case locationBiasGlobal:
		if location.IsRemote {
			return 0.1, 0
		}
		return 0, 0
	default:
		return 0, 0
	}
}

func computeRoleBiasBoost(dynamic *DynamicRankingConfig, primaryRole string, matchedRoles []string) float64 {
	if dynamic == nil || len(dynamic.RoleBias) == 0 {
		return 0
	}

	best := 0.0
	if primaryRole != "" {
		best = dynamic.RoleBias[primaryRole]
	}
	for _, role := range matchedRoles {
		best = math.Max(best, dynamic.RoleBias[role])
	}
	if best <= 0 {
		return 0
	}
	return clamp(best, 0, 1.5) * 0.18
}

func computeDynamicKeywordBoost(dynamic *DynamicRankingConfig, title, tags, body string) (float64, []string) {
	if dynamic == nil || len(dynamic.BoostKeywords) == 0 {
		return 0, nil
	}
	raw := 0.0
	matched := make([]string, 0, len(dynamic.BoostKeywords))
	for _, keyword := range dynamic.BoostKeywords {
		score := keywordMatchScore(keyword, title, tags, body)
		if score <= 0 {
			continue
		}
		raw += score
		matched = append(matched, keyword)
	}
	return math.Min(0.35, raw*0.018), normalizeKeywords(matched)
}

func computeDynamicKeywordPenalty(dynamic *DynamicRankingConfig, title, tags, body string) float64 {
	if dynamic == nil || len(dynamic.PenaltyKeywords) == 0 {
		return 0
	}
	raw := 0.0
	for _, keyword := range dynamic.PenaltyKeywords {
		raw += keywordMatchScore(keyword, title, tags, body)
	}
	return math.Min(0.4, raw*0.02)
}

func computeStaticKeywordScore(cfg *JobCrawlerConfig, semanticAvailable bool, title, tags, body string) (float64, []string, bool) {
	if cfg == nil || len(cfg.KeywordsInclude) == 0 {
		return 0, nil, true
	}
	raw := 0.0
	matched := make([]string, 0, len(cfg.KeywordsInclude))
	for _, keyword := range cfg.KeywordsInclude {
		score := keywordMatchScore(keyword, title, tags, body)
		if score <= 0 {
			continue
		}
		raw += score
		matched = append(matched, keyword)
	}
	if !semanticAvailable && raw <= 0 {
		return 0, nil, false
	}

	weight := 0.015
	if !semanticAvailable {
		weight = 0.05
	}
	return math.Min(0.3, raw*weight), normalizeKeywords(matched), true
}

func computeRoleMatchAndPenalty(roleEval roleEvaluation) (match, penalty float64) {
	match = clamp(roleEval.Boost*0.08, 0, 0.32)
	penalty = clamp(roleEval.Penalty*0.06, 0, 0.5)
	return match, penalty
}

func computeLocationWeight(cfg *JobCrawlerConfig, location normalizedLocation) float64 {
	return clamp(locationScore(cfg, location)*0.12, 0, 0.22)
}

func computeSourceBoost(cfg *JobCrawlerConfig, sourceID string) float64 {
	if cfg == nil || !cfg.EnableLinkedInProxySource {
		return 0
	}
	if strings.TrimSpace(strings.ToLower(sourceID)) == sourceLinkedInProxy {
		return 0.12
	}
	return 0
}

func computeRecencyWeight(now time.Time, postedAt *time.Time) float64 {
	return clamp(recencyScore(now, postedAt)*0.08, 0, 0.14)
}

func computeSeniorityPenalty(level string, maxRank int) float64 {
	return clamp(seniorityPenaltyForLevel(level, maxRank)*0.05, 0, 0.35)
}

func clamp(value, minValue, maxValue float64) float64 {
	switch {
	case value < minValue:
		return minValue
	case value > maxValue:
		return maxValue
	default:
		return value
	}
}

func unionKeywordMatches(groups ...[]string) []string {
	var combined []string
	for _, group := range groups {
		combined = append(combined, group...)
	}
	return trimKeywordList(combined, 12)
}

func (f *JobCrawlerFeature) maybeRerankJobs(ctx context.Context, cfg *JobCrawlerConfig, jobs []RankedJob, profileText string, dynamic *DynamicRankingConfig) ([]RankedJob, []string, bool) {
	if f == nil || cfg == nil || !cfg.EnableLLMRerank || len(jobs) < 2 {
		return jobs, nil, false
	}

	limit := resolveLLMRerankTopN(cfg)
	if limit > len(jobs) {
		limit = len(jobs)
	}
	if limit < 2 {
		return jobs, nil, false
	}

	payload := struct {
		Query       string           `json:"query,omitempty"`
		ProfileText string           `json:"profile_text"`
		Jobs        []map[string]any `json:"jobs"`
	}{
		ProfileText: profileText,
	}
	if dynamic != nil {
		payload.Query = dynamic.Query
	}
	payload.Jobs = make([]map[string]any, 0, limit)
	for _, job := range jobs[:limit] {
		payload.Jobs = append(payload.Jobs, map[string]any{
			"job_hash":       job.JobHash,
			"title":          job.Title,
			"company":        job.Company,
			"location":       job.NormalizedLocation,
			"role_type":      job.RoleType,
			"tags":           job.Tags,
			"semantic_score": job.SemanticScore,
			"score":          job.Score,
			"description":    trimText(job.Description, 480),
		})
	}
	payloadJSON, _ := json.Marshal(payload)

	order, err := f.requestRerankOrder(ctx, cfg.TenantID, string(payloadJSON))
	if err != nil {
		return fallbackRerankJobs(jobs, limit), []string{"LLM rerank fallback: " + err.Error()}, true
	}

	return applyRerankOrder(jobs, order, limit), nil, false
}

func (f *JobCrawlerFeature) requestRerankOrder(ctx context.Context, tenantID, payload string) (map[string]int, error) {
	prompts := []string{
		`You rerank engineering jobs for a targeted crawler digest.
Return strict JSON:
{"ordered_job_hashes":["hash1","hash2"]}

Rules:
- ordered_job_hashes must only contain hashes from the input jobs
- rank the best matches first for the provided profile/query
- do not explain the output`,
		`Return JSON only:
{"ordered_job_hashes":["hash1","hash2"]}

Rules:
- use only job_hash values from the input
- include at least 2 hashes when possible
- no markdown
- no explanation`,
	}

	var attemptErrors []string
	for _, prompt := range prompts {
		rerankCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		rawResponse, err := f.callDynamicLLM(rerankCtx, tenantID, prompt, payload, 320)
		cancel()
		if err != nil {
			attemptErrors = append(attemptErrors, err.Error())
			continue
		}
		order, err := decodeRerankOrder(rawResponse)
		if err != nil {
			attemptErrors = append(attemptErrors, err.Error())
			continue
		}
		if len(order) == 0 {
			attemptErrors = append(attemptErrors, "no valid job hashes returned")
			continue
		}
		return order, nil
	}

	if len(attemptErrors) == 0 {
		return nil, fmt.Errorf("rerank order unavailable")
	}
	return nil, fmt.Errorf("%s", strings.Join(attemptErrors, "; "))
}

func decodeRerankOrder(raw string) (map[string]int, error) {
	orderPayload := extractJSONBlock(raw, '{', '}')
	if orderPayload == "" {
		return nil, fmt.Errorf("invalid JSON response")
	}

	var decoded struct {
		OrderedJobHashes []string `json:"ordered_job_hashes"`
	}
	if err := json.Unmarshal([]byte(orderPayload), &decoded); err != nil {
		return nil, err
	}

	order := make(map[string]int, len(decoded.OrderedJobHashes))
	for idx, jobHash := range decoded.OrderedJobHashes {
		jobHash = strings.TrimSpace(jobHash)
		if jobHash == "" {
			continue
		}
		if _, exists := order[jobHash]; exists {
			continue
		}
		order[jobHash] = idx
	}
	return order, nil
}

func applyRerankOrder(jobs []RankedJob, order map[string]int, limit int) []RankedJob {
	if len(jobs) == 0 || len(order) == 0 {
		return append([]RankedJob(nil), jobs...)
	}
	if limit <= 0 || limit > len(jobs) {
		limit = len(jobs)
	}
	reranked := append([]RankedJob(nil), jobs...)
	sort.SliceStable(reranked[:limit], func(i, j int) bool {
		leftRank, leftOK := order[reranked[i].JobHash]
		rightRank, rightOK := order[reranked[j].JobHash]
		switch {
		case leftOK && rightOK:
			return leftRank < rightRank
		case leftOK:
			return true
		case rightOK:
			return false
		default:
			return betterRankedJob(reranked[i], reranked[j])
		}
	})
	return reranked
}

func fallbackRerankJobs(jobs []RankedJob, limit int) []RankedJob {
	if len(jobs) == 0 {
		return nil
	}
	if limit <= 0 || limit > len(jobs) {
		limit = len(jobs)
	}
	reranked := append([]RankedJob(nil), jobs...)
	sort.SliceStable(reranked[:limit], func(i, j int) bool {
		switch {
		case reranked[i].SourceBoost != reranked[j].SourceBoost:
			return reranked[i].SourceBoost > reranked[j].SourceBoost
		case reranked[i].SemanticScore != reranked[j].SemanticScore:
			return reranked[i].SemanticScore > reranked[j].SemanticScore
		case reranked[i].RoleMatch != reranked[j].RoleMatch:
			return reranked[i].RoleMatch > reranked[j].RoleMatch
		default:
			return betterRankedJob(reranked[i], reranked[j])
		}
	})
	return reranked
}
