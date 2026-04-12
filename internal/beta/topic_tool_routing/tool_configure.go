package topictoolrouting

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type configureTool struct {
	feature *TopicToolRoutingFeature
}

func (t *configureTool) Name() string { return "topic_tool_routing_configure" }

func (t *configureTool) Description() string {
	return "Configure which beta feature toolsets are visible in the current Telegram topic/thread."
}

func (t *configureTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":       map[string]any{"type": "string", "description": "Optional config key. Defaults to channel/chat/thread scope when omitted."},
			"name":      map[string]any{"type": "string", "description": "Optional display name for this scope config."},
			"channel":   map[string]any{"type": "string", "description": "Optional channel instance name. Defaults to the current tool context."},
			"chat_id":   map[string]any{"type": "string", "description": "Optional chat ID. Defaults to the current tool context."},
			"thread_id": map[string]any{"type": "integer", "description": "Optional topic/thread ID. Defaults to the current tool context."},
			"enabled_features": map[string]any{
				"type":        "array",
				"description": "The beta features whose owned tools should remain visible in this topic.",
				"items":       map[string]any{"type": "string"},
			},
		},
		"required": []string{"enabled_features"},
	}
}

func (t *configureTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("topic tool routing feature is unavailable")
	}

	threadID, _ := intArg(args, "thread_id")
	scope := currentScopeFromToolContext(
		ctx,
		stringArg(args, "channel"),
		stringArg(args, "chat_id"),
		threadID,
	)
	cfg, err := t.feature.upsertConfigForTenant(tenantKeyFromCtx(ctx), TopicRoutingConfig{
		Key:             stringArg(args, "key"),
		Name:            stringArg(args, "name"),
		Channel:         scope.Channel,
		ChatID:          scope.ChatID,
		ThreadID:        scope.ThreadID,
		EnabledFeatures: stringSliceArg(args, "enabled_features"),
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return jsonResult(map[string]any{"config": cfg})
}
