package topicrouting

import (
	"context"
	"slices"
	"strings"
	"sync"
)

// TopicToolScope identifies the current routed chat/topic before agent execution.
type TopicToolScope struct {
	Channel     string
	ChannelType string
	ChatID      string
	ThreadID    int
	LocalKey    string
}

// TopicToolDecision describes a per-topic tool routing decision.
type TopicToolDecision struct {
	Matched         bool     `json:"matched"`
	ConfigKey       string   `json:"config_key,omitempty"`
	EnabledFeatures []string `json:"enabled_features,omitempty"`
	HiddenTools     []string `json:"hidden_tools,omitempty"`
}

// TopicToolResolver optionally provides per-topic tool hiding decisions.
// When nil or when Matched=false, the global toolset is preserved.
type TopicToolResolver interface {
	ResolveTopicToolDecision(ctx context.Context, scope TopicToolScope) (*TopicToolDecision, error)
}

var state = struct {
	mu           sync.RWMutex
	resolver     TopicToolResolver
	featureTools map[string]map[string]struct{}
}{
	featureTools: make(map[string]map[string]struct{}),
}

// SetTopicToolResolver installs the active topic tool resolver.
func SetTopicToolResolver(resolver TopicToolResolver) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.resolver = resolver
}

// ResolveTopicToolDecision resolves the current topic's tool filtering decision.
func ResolveTopicToolDecision(ctx context.Context, scope TopicToolScope) (*TopicToolDecision, error) {
	state.mu.RLock()
	resolver := state.resolver
	state.mu.RUnlock()
	if resolver == nil {
		return nil, nil
	}
	return resolver.ResolveTopicToolDecision(ctx, scope)
}

// RegisterTopicFeatureTools associates a beta feature with the tools it owns so
// per-topic routing can hide them together.
func RegisterTopicFeatureTools(featureName string, toolNames ...string) {
	featureName = normalizeFeatureName(featureName)
	if featureName == "" {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	toolSet := state.featureTools[featureName]
	if toolSet == nil {
		toolSet = make(map[string]struct{})
		state.featureTools[featureName] = toolSet
	}
	for _, toolName := range toolNames {
		toolName = strings.TrimSpace(toolName)
		if toolName == "" {
			continue
		}
		toolSet[toolName] = struct{}{}
	}
}

// UnregisterTopicFeatureTools removes a feature-to-tools association.
func UnregisterTopicFeatureTools(featureName string) {
	featureName = normalizeFeatureName(featureName)
	if featureName == "" {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	delete(state.featureTools, featureName)
}

// TopicFeatureToolsSnapshot returns a stable copy of topic-routable feature tools.
func TopicFeatureToolsSnapshot() map[string][]string {
	state.mu.RLock()
	defer state.mu.RUnlock()

	snapshot := make(map[string][]string, len(state.featureTools))
	for featureName, toolSet := range state.featureTools {
		tools := make([]string, 0, len(toolSet))
		for toolName := range toolSet {
			tools = append(tools, toolName)
		}
		slices.Sort(tools)
		snapshot[featureName] = tools
	}
	return snapshot
}

// Clear resets resolver and feature-tool mappings. Used by beta shutdown/tests.
func Clear() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.resolver = nil
	state.featureTools = make(map[string]map[string]struct{})
}

func normalizeFeatureName(name string) string {
	return strings.TrimSpace(strings.ToLower(name))
}
