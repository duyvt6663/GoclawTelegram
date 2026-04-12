package topictoolrouting

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type statusTool struct {
	feature *TopicToolRoutingFeature
}

func (t *statusTool) Name() string { return "topic_tool_routing_status" }

func (t *statusTool) Description() string {
	return "Inspect topic-scoped beta feature routing for the current chat/topic or list all saved routing configs."
}

func (t *statusTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":       map[string]any{"type": "string", "description": "Optional config key. When omitted, resolves the current chat/topic if available."},
			"channel":   map[string]any{"type": "string", "description": "Optional channel instance name for scope resolution."},
			"chat_id":   map[string]any{"type": "string", "description": "Optional chat ID for scope resolution."},
			"thread_id": map[string]any{"type": "integer", "description": "Optional topic/thread ID for scope resolution."},
		},
	}
}

func (t *statusTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("topic tool routing feature is unavailable")
	}

	key := stringArg(args, "key")
	if key != "" {
		cfg, err := t.feature.store.getConfigByKey(tenantKeyFromCtx(ctx), key)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		return jsonResult(map[string]any{"config": cfg, "registered_features": registeredFeaturesSnapshot()})
	}

	threadID, _ := intArg(args, "thread_id")
	scope := currentScopeFromToolContext(
		ctx,
		stringArg(args, "channel"),
		stringArg(args, "chat_id"),
		threadID,
	)
	if scope.Channel != "" && scope.ChatID != "" {
		snapshot, err := t.feature.resolveSnapshot(ctx, topicrouting.TopicToolScope{
			Channel:  scope.Channel,
			ChatID:   scope.ChatID,
			ThreadID: scope.ThreadID,
			LocalKey: scope.LocalKey,
		})
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		return jsonResult(snapshot)
	}

	configs, err := t.feature.store.listConfigs(tenantKeyFromCtx(ctx))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return jsonResult(map[string]any{
		"configs":             configs,
		"registered_features": registeredFeaturesSnapshot(),
	})
}
