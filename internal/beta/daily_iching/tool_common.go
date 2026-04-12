package dailyiching

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func toolJSONResult(data any) *tools.Result {
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

func boolArg(args map[string]any, key string) (*bool, bool) {
	value, ok := args[key].(bool)
	if !ok {
		return nil, false
	}
	return &value, true
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

func (f *DailyIChingFeature) resolveToolConfig(ctx context.Context, key string) (*DailyIChingConfig, error) {
	tenantID := tenantKeyFromCtx(ctx)
	if key != "" {
		return f.store.getConfigByKey(tenantID, key)
	}

	channelName := strings.TrimSpace(tools.ToolChannelFromCtx(ctx))
	chatID, threadID := chatTargetFromToolContext(ctx, "", 0)
	if channelName != "" && chatID != "" {
		cfg, err := f.store.getConfigByTarget(tenantID, channelName, chatID, threadID)
		if err == nil {
			return cfg, nil
		}
		if !errors.Is(err, errDailyIChingConfigNotFound) {
			return nil, err
		}
	}

	configs, err := f.store.listConfigs(tenantID)
	if err != nil {
		return nil, err
	}
	enabled := make([]DailyIChingConfig, 0, len(configs))
	for _, cfg := range configs {
		if cfg.Enabled {
			enabled = append(enabled, cfg)
		}
	}
	if len(enabled) == 1 {
		return &enabled[0], nil
	}
	return nil, fmt.Errorf("config key is required when multiple daily i ching configs are available")
}
