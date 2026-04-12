package featurerequests

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const buildFollowupQueueKeyPrefix = "internal.feature_requests.build_followup."

func buildFollowupQueueKey(featureID string) string {
	featureID = strings.TrimSpace(featureID)
	if featureID == "" {
		return buildFollowupQueueKeyPrefix
	}
	return buildFollowupQueueKeyPrefix + featureID
}

func buildFollowupInboundMessage(req *FeatureRequest, followup *buildFollowupContext, retrying bool, summary string) bus.InboundMessage {
	meta := map[string]string{
		tools.MetaOriginChannel:  followup.Channel,
		tools.MetaOriginPeerKind: followup.PeerKind,
		"feature_id":             req.ID,
		"feature_status":         req.Status,
	}
	if strings.TrimSpace(followup.UserID) != "" {
		meta[tools.MetaOriginUserID] = followup.UserID
	}
	if strings.TrimSpace(followup.LocalKey) != "" {
		meta[tools.MetaOriginLocalKey] = followup.LocalKey
	}
	if strings.TrimSpace(followup.SessionKey) != "" {
		meta[tools.MetaOriginSessionKey] = followup.SessionKey
	}

	return bus.InboundMessage{
		Channel:  tools.ChannelSystem,
		SenderID: BuildFollowupSenderID(req.ID),
		ChatID:   followup.ChatID,
		Content:  buildFollowupMessage(req, retrying, summary),
		UserID:   followup.UserID,
		TenantID: followup.TenantID,
		AgentID:  followup.AgentKey,
		Metadata: meta,
	}
}

func queueBuildFollowupForRestart(sysConfigs store.SystemConfigStore, req *FeatureRequest, followup *buildFollowupContext, retrying bool, summary string) error {
	if sysConfigs == nil || req == nil || followup == nil {
		return fmt.Errorf("missing follow-up queue dependencies")
	}
	msg := buildFollowupInboundMessage(req, followup, retrying, summary)
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal queued build follow-up: %w", err)
	}
	queueCtx := store.WithTenantID(context.Background(), store.MasterTenantID)
	return sysConfigs.Set(queueCtx, buildFollowupQueueKey(req.ID), string(payload))
}

// PublishQueuedBuildFollowups flushes any post-restart feature-build follow-ups
// that were persisted before a gateway restart request.
func PublishQueuedBuildFollowups(ctx context.Context, sysConfigs store.SystemConfigStore, msgBus *bus.MessageBus) (int, error) {
	if sysConfigs == nil || msgBus == nil {
		return 0, nil
	}

	if store.TenantIDFromContext(ctx) == uuid.Nil {
		ctx = store.WithTenantID(ctx, store.MasterTenantID)
	}

	values, err := sysConfigs.List(ctx)
	if err != nil {
		return 0, err
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.HasPrefix(key, buildFollowupQueueKeyPrefix) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)

	published := 0
	for _, key := range keys {
		raw := strings.TrimSpace(values[key])
		if raw == "" {
			_ = sysConfigs.Delete(ctx, key)
			continue
		}

		var msg bus.InboundMessage
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			slog.Warn("beta feature_requests: dropping malformed queued build follow-up", "key", key, "error", err)
			_ = sysConfigs.Delete(ctx, key)
			continue
		}
		if !IsBuildFollowupSender(msg.SenderID) {
			slog.Warn("beta feature_requests: dropping queued follow-up with invalid sender", "key", key, "sender", msg.SenderID)
			_ = sysConfigs.Delete(ctx, key)
			continue
		}
		if !msgBus.TryPublishInbound(msg) {
			return published, fmt.Errorf("inbound buffer full while publishing queued build follow-up")
		}
		if err := sysConfigs.Delete(ctx, key); err != nil {
			slog.Warn("beta feature_requests: failed to delete queued build follow-up", "key", key, "error", err)
		}
		published++
	}

	return published, nil
}
