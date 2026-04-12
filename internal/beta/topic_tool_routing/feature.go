package topictoolrouting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
)

// TopicToolRoutingFeature keeps per-topic feature flags that hide feature-owned
// tools before the agent sees the tool list.
//
// Plan:
// 1. Persist per-topic enabled feature lists in one beta-local config table keyed by channel/chat/thread.
// 2. Resolve the current scope before each agent run and hide registered feature tools that are not enabled there.
// 3. Expose one configure tool plus matching RPC/HTTP surfaces for inspection and updates.
type TopicToolRoutingFeature struct {
	store *featureStore
}

func (f *TopicToolRoutingFeature) Name() string { return featureName }

func (f *TopicToolRoutingFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	f.store = &featureStore{db: deps.Stores.DB}
	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	topicrouting.SetTopicToolResolver(f)

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&configureTool{feature: f})
		deps.ToolRegistry.Register(&statusTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	slog.Info("beta topic tool routing initialized")
	return nil
}

func (f *TopicToolRoutingFeature) Shutdown(_ context.Context) error {
	topicrouting.SetTopicToolResolver(nil)
	return nil
}

func (f *TopicToolRoutingFeature) ResolveTopicToolDecision(ctx context.Context, scope topicrouting.TopicToolScope) (*topicrouting.TopicToolDecision, error) {
	if f == nil || f.store == nil {
		return nil, nil
	}
	snapshot, err := f.resolveSnapshot(ctx, scope)
	if err != nil {
		return nil, err
	}
	if snapshot == nil || snapshot.Config == nil {
		return nil, nil
	}
	return &topicrouting.TopicToolDecision{
		Matched:         snapshot.MatchKind != matchKindNone,
		ConfigKey:       snapshot.Config.Key,
		EnabledFeatures: append([]string(nil), snapshot.EnabledFeatures...),
		HiddenTools:     append([]string(nil), snapshot.HiddenTools...),
	}, nil
}

func (f *TopicToolRoutingFeature) upsertConfigForTenant(tenantID string, cfg TopicRoutingConfig) (*TopicRoutingConfig, error) {
	if f == nil || f.store == nil {
		return nil, fmt.Errorf("topic tool routing feature is unavailable")
	}

	cfg = cfg.withDefaults()
	if cfg.Key == "" {
		return nil, fmt.Errorf("key is required")
	}
	if cfg.Channel == "" {
		return nil, fmt.Errorf("channel is required")
	}
	if cfg.ChatID == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	if err := validateFeatureNames(cfg.EnabledFeatures); err != nil {
		return nil, err
	}

	cfg.TenantID = tenantID
	return f.store.upsertConfig(&cfg)
}

func (f *TopicToolRoutingFeature) resolveSnapshot(ctx context.Context, scope topicrouting.TopicToolScope) (*ResolutionSnapshot, error) {
	if f == nil || f.store == nil {
		return nil, fmt.Errorf("topic tool routing feature is unavailable")
	}

	scope.Channel = strings.TrimSpace(scope.Channel)
	scope.ChatID = strings.TrimSpace(scope.ChatID)
	scope.ThreadID = normalizeThreadID(scope.ThreadID)
	scope.LocalKey = strings.TrimSpace(scope.LocalKey)
	if scope.LocalKey != "" && (scope.ChatID == "" || scope.ThreadID == 0) {
		chatID, threadID := parseCompositeLocalKey(scope.LocalKey)
		if scope.ChatID == "" {
			scope.ChatID = chatID
		}
		if scope.ThreadID == 0 {
			scope.ThreadID = threadID
		}
	}

	snapshot := &ResolutionSnapshot{
		Channel:            scope.Channel,
		ChatID:             scope.ChatID,
		ThreadID:           scope.ThreadID,
		LocalKey:           scope.LocalKey,
		MatchKind:          matchKindNone,
		RegisteredFeatures: cloneStringMap(registeredFeaturesSnapshot()),
	}
	if scope.Channel == "" || scope.ChatID == "" {
		return snapshot, nil
	}

	cfg, matchKind, err := f.store.resolveConfigByTarget(tenantKeyFromCtx(ctx), scope.Channel, scope.ChatID, scope.ThreadID)
	switch {
	case err == nil:
		snapshot.Config = cfg
		snapshot.MatchKind = matchKind
	case errors.Is(err, errTopicRoutingConfigNotFound):
		return snapshot, nil
	default:
		return nil, err
	}

	snapshot.EnabledFeatures = append([]string(nil), snapshot.Config.EnabledFeatures...)
	snapshot.HiddenTools = resolveHiddenTools(snapshot.Config.EnabledFeatures, snapshot.RegisteredFeatures)
	return snapshot, nil
}

func resolveHiddenTools(enabledFeatures []string, registered map[string][]string) []string {
	if len(registered) == 0 {
		return nil
	}

	enabled := make(map[string]struct{}, len(enabledFeatures))
	for _, featureName := range enabledFeatures {
		enabled[strings.TrimSpace(strings.ToLower(featureName))] = struct{}{}
	}

	hiddenSet := make(map[string]struct{})
	for featureName, toolNames := range registered {
		if _, ok := enabled[strings.TrimSpace(strings.ToLower(featureName))]; ok {
			continue
		}
		for _, toolName := range toolNames {
			toolName = strings.TrimSpace(toolName)
			if toolName == "" {
				continue
			}
			hiddenSet[toolName] = struct{}{}
		}
	}

	if len(hiddenSet) == 0 {
		return nil
	}
	hidden := make([]string, 0, len(hiddenSet))
	for toolName := range hiddenSet {
		hidden = append(hidden, toolName)
	}
	sort.Strings(hidden)
	return hidden
}

func validateFeatureNames(featureNames []string) error {
	for _, featureName := range normalizeFeatureNames(featureNames) {
		if beta.IsRegistered(featureName) {
			continue
		}
		return fmt.Errorf("unknown beta feature %q", featureName)
	}
	return nil
}
