package topictoolrouting

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	featureName = "topic_tool_routing"

	matchKindNone        = "none"
	matchKindExact       = "exact"
	matchKindChatDefault = "chat_default"
)

type ResolutionSnapshot struct {
	Channel            string              `json:"channel"`
	ChatID             string              `json:"chat_id"`
	ThreadID           int                 `json:"thread_id"`
	LocalKey           string              `json:"local_key,omitempty"`
	MatchKind          string              `json:"match_kind"`
	Config             *TopicRoutingConfig `json:"config,omitempty"`
	EnabledFeatures    []string            `json:"enabled_features,omitempty"`
	HiddenTools        []string            `json:"hidden_tools,omitempty"`
	RegisteredFeatures map[string][]string `json:"registered_features,omitempty"`
}

func tenantKey(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return strings.TrimSpace(id.String())
}

func tenantKeyFromCtx(ctx context.Context) string {
	return tenantKey(store.TenantIDFromContext(ctx))
}

func normalizeThreadID(threadID int) int {
	if threadID <= 1 {
		return 0
	}
	return threadID
}

func parseCompositeLocalKey(localKey string) (string, int) {
	localKey = strings.TrimSpace(localKey)
	if localKey == "" {
		return "", 0
	}
	if idx := strings.Index(localKey, ":topic:"); idx > 0 {
		return localKey[:idx], normalizeThreadID(intArgString(localKey[idx+len(":topic:"):]))
	}
	if idx := strings.Index(localKey, ":thread:"); idx > 0 {
		return localKey[:idx], normalizeThreadID(intArgString(localKey[idx+len(":thread:"):]))
	}
	return localKey, 0
}

func composeLocalKey(chatID string, threadID int) string {
	chatID = strings.TrimSpace(chatID)
	threadID = normalizeThreadID(threadID)
	if chatID == "" {
		return ""
	}
	if threadID == 0 {
		return chatID
	}
	return fmt.Sprintf("%s:topic:%d", chatID, threadID)
}

func defaultConfigKey(channel, chatID string, threadID int) string {
	channel = strings.TrimSpace(strings.ToLower(channel))
	chatID = strings.TrimSpace(chatID)
	threadID = normalizeThreadID(threadID)

	parts := []string{channel, chatID}
	if threadID > 0 {
		parts = append(parts, fmt.Sprintf("topic-%d", threadID))
	}
	return normalizeConfigKey(strings.Join(parts, "-"))
}

func normalizeConfigKey(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(value))
	lastHyphen := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastHyphen = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if lastHyphen {
				continue
			}
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func normalizeFeatureNames(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		name := strings.TrimSpace(strings.ToLower(value))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func registeredFeaturesSnapshot() map[string][]string {
	return topicrouting.TopicFeatureToolsSnapshot()
}

func jsonResult(data any) *tools.Result {
	encoded, err := json.Marshal(data)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(string(encoded))
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func stringSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func intArg(args map[string]any, key string) (int, bool) {
	switch value := args[key].(type) {
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func intArgString(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	var n int
	fmt.Sscanf(raw, "%d", &n)
	return n
}

func currentScopeFromToolContext(ctx context.Context, explicitChannel, explicitChatID string, explicitThreadID int) topicrouting.TopicToolScope {
	scope := topicrouting.TopicToolScope{
		Channel:  strings.TrimSpace(explicitChannel),
		ChatID:   strings.TrimSpace(explicitChatID),
		ThreadID: normalizeThreadID(explicitThreadID),
	}
	if scope.Channel == "" {
		scope.Channel = strings.TrimSpace(tools.ToolChannelFromCtx(ctx))
	}
	if scope.ChatID == "" {
		scope.ChatID = strings.TrimSpace(tools.ToolChatIDFromCtx(ctx))
	}
	if scope.ThreadID == 0 {
		scope.ThreadID = normalizeThreadID(tools.ToolThreadIDFromCtx(ctx))
	}
	scope.LocalKey = strings.TrimSpace(tools.ToolLocalKeyFromCtx(ctx))
	if scope.LocalKey != "" && (scope.ChatID == "" || scope.ThreadID == 0) {
		chatID, threadID := parseCompositeLocalKey(scope.LocalKey)
		if scope.ChatID == "" {
			scope.ChatID = chatID
		}
		if scope.ThreadID == 0 {
			scope.ThreadID = threadID
		}
	}
	return scope
}

func upsertParamsToConfig(params upsertParams) TopicRoutingConfig {
	cfg := TopicRoutingConfig{
		Key:             params.Key,
		Name:            params.Name,
		Channel:         params.Channel,
		ChatID:          params.ChatID,
		EnabledFeatures: params.EnabledFeatures,
	}
	if params.ThreadID != nil {
		cfg.ThreadID = *params.ThreadID
	}
	return cfg
}

func cloneStringMap(src map[string][]string) map[string][]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string][]string, len(src))
	for key, values := range src {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func nowUTC() time.Time {
	return time.Now().UTC()
}
