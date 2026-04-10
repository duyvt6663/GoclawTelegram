package featurerequests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type activateBetaFeatureTool struct {
	feature *FeatureRequestsFeature
}

func (t *activateBetaFeatureTool) Name() string { return "activate_beta_feature" }

func (t *activateBetaFeatureTool) Description() string {
	return "Enable or disable a compiled beta feature for this tenant via system_configs, and hot-activate it in the current gateway when possible."
}

func (t *activateBetaFeatureTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"feature_name": map[string]any{
				"type":        "string",
				"description": "Compiled beta feature name, for example daily_discipline",
			},
			"enabled": map[string]any{
				"type":        "boolean",
				"description": "Whether the feature should be enabled. Defaults to true.",
			},
		},
		"required": []string{"feature_name"},
	}
}

func (t *activateBetaFeatureTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("beta feature activation is unavailable")
	}
	if t.feature.sysConfigs == nil {
		return tools.ErrorResult("system config store is unavailable")
	}

	featureName := normalizeBetaFeatureName(tools.GetParamString(args, "feature_name", ""))
	if featureName == "" {
		return tools.ErrorResult("feature_name is required")
	}
	if !beta.IsRegistered(featureName) {
		return tools.ErrorResult(fmt.Sprintf("unknown compiled beta feature: %s", featureName))
	}

	enabled := tools.GetParamBool(args, "enabled", true)
	value := "false"
	if enabled {
		value = "true"
	}

	key := "beta." + featureName
	if err := t.feature.sysConfigs.Set(ctx, key, value); err != nil {
		return tools.ErrorResult(fmt.Sprintf("failed to update %s: %v", key, err))
	}

	t.broadcastSystemConfigChanged(ctx)

	wasActive := beta.IsActive(featureName)
	hotActivated := false
	var activationErr error
	if enabled && !wasActive {
		hotActivated, activationErr = beta.EnsureActive(ctx, beta.NewFlagSource(t.feature.sysConfigs), t.feature.betaDeps, featureName)
	}
	isActive := beta.IsActive(featureName)

	message := activationResultMessage(featureName, enabled, wasActive, hotActivated, isActive, activationErr)
	result := map[string]any{
		"feature_name":      featureName,
		"system_config_key": key,
		"enabled":           enabled,
		"runtime_active":    isActive,
		"hot_activated":     hotActivated,
		"message":           message,
	}
	if activationErr != nil {
		result["activation_error"] = activationErr.Error()
	}

	out, _ := json.Marshal(result)
	return tools.NewResult(string(out))
}

func (t *activateBetaFeatureTool) broadcastSystemConfigChanged(ctx context.Context) {
	if t == nil || t.feature == nil || t.feature.msgBus == nil {
		return
	}

	tenantID := store.TenantIDFromContext(ctx)
	freshCtx := context.Background()
	if tenantID == uuid.Nil {
		tenantID = store.MasterTenantID
	}
	freshCtx = store.WithTenantID(freshCtx, tenantID)
	t.feature.msgBus.Broadcast(bus.Event{
		Name:     bus.TopicSystemConfigChanged,
		Payload:  freshCtx,
		TenantID: tenantID,
	})
}

func normalizeBetaFeatureName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.ReplaceAll(name, "-", "_")
	return name
}

func activationResultMessage(featureName string, enabled, wasActive, hotActivated, isActive bool, activationErr error) string {
	if enabled {
		switch {
		case hotActivated:
			return fmt.Sprintf("Beta feature '%s' is enabled and was activated in the running gateway.", featureName)
		case isActive && wasActive:
			return fmt.Sprintf("Beta feature '%s' is enabled. Its runtime was already active.", featureName)
		case isActive:
			return fmt.Sprintf("Beta feature '%s' is enabled and active in the running gateway.", featureName)
		case activationErr != nil:
			return fmt.Sprintf("Beta feature '%s' is enabled, but hot activation failed: %s", featureName, activationErr.Error())
		default:
			return fmt.Sprintf("Beta feature '%s' is enabled. A gateway restart may still be required before runtime hooks become available.", featureName)
		}
	}

	if isActive {
		return fmt.Sprintf("Beta feature '%s' was disabled in system_configs, but it remains active until the gateway restarts.", featureName)
	}
	return fmt.Sprintf("Beta feature '%s' is disabled.", featureName)
}
