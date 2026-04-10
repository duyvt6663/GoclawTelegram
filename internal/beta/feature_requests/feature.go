package featurerequests

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"sync"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

const approvalThreshold = 5

// TelegramPollCreator is implemented by the Telegram channel.
// Same signature as tools.SoDauBaiPollCreator — the telegram channel satisfies both.
type TelegramPollCreator interface {
	CreateSoDauBaiPoll(ctx context.Context, chatID int64, threadID int, question, yesOption, noOption string, openPeriodSeconds int) (pollID string, messageID int, err error)
}

// FeatureRequestsFeature manages user-requested beta features via Telegram.
type FeatureRequestsFeature struct {
	store   *featureStore
	msgBus  *bus.MessageBus
	resolve func(channel string) TelegramPollCreator
	mu      sync.Mutex
}

func (f *FeatureRequestsFeature) Name() string { return "feature_requests" }

func (f *FeatureRequestsFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("feature_requests requires a SQL store")
	}

	f.store = &featureStore{db: deps.Stores.DB}
	f.msgBus = deps.MessageBus

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("feature_requests migration: %w", err)
	}

	// Build poll creator resolver from channel manager.
	f.resolve = func(channel string) TelegramPollCreator {
		if strings.TrimSpace(channel) == "" || deps.ChannelManager == nil {
			return nil
		}
		ch, ok := deps.ChannelManager.GetChannel(channel)
		if !ok {
			return nil
		}
		creator, _ := ch.(TelegramPollCreator)
		return creator
	}

	// Subscribe to poll answer events to track approval votes.
	deps.MessageBus.Subscribe(bus.TopicTelegramPollAnswer, f.handlePollAnswer)
	deps.MessageBus.Subscribe(bus.TopicTelegramPollClosed, f.handlePollClosed)

	// Register tools.
	buildWorkspace := resolveBuildWorkspace(deps.Workspace)
	if buildWorkspace == "" {
		slog.Warn("beta feature_requests: build workspace could not be resolved",
			"default_workspace", deps.Workspace)
	} else {
		slog.Info("beta feature_requests: build workspace resolved", "workspace", buildWorkspace)
	}
	deps.ToolRegistry.Register(&requestFeatureTool{feature: f})
	deps.ToolRegistry.Register(&listFeaturesTool{feature: f})
	deps.ToolRegistry.Register(&featureDetailTool{feature: f})
	deps.ToolRegistry.Register(&featurePollTool{feature: f})
	deps.ToolRegistry.Register(&buildFeatureTool{feature: f, workspace: buildWorkspace})

	slog.Info("beta feature_requests: tools registered, poll handler active")
	return nil
}

func (f *FeatureRequestsFeature) Shutdown(_ context.Context) error {
	if f.msgBus != nil {
		f.msgBus.Unsubscribe(bus.TopicTelegramPollAnswer)
		f.msgBus.Unsubscribe(bus.TopicTelegramPollClosed)
	}
	return nil
}

// handlePollAnswer processes Telegram poll votes for feature approval polls.
func (f *FeatureRequestsFeature) handlePollAnswer(event bus.Event) {
	if f == nil || f.store == nil {
		return
	}

	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return
	}
	pollID, _ := payload["poll_id"].(string)
	voterID, _ := payload["voter_id"].(string)
	optionIDs := extractOptionIDs(payload["option_ids"])
	if pollID == "" || voterID == "" {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Check if this poll belongs to a feature request.
	req, err := f.store.getByPollID(tenantKey(event.TenantID), pollID)
	if err != nil {
		return // not our poll
	}

	prevApprovals := req.Approvals
	req.Voters = updateYesVoters(req.Voters, voterID, hasYesVote(optionIDs))
	req.Approvals = len(req.Voters)

	approvedNow := prevApprovals < approvalThreshold && req.Approvals >= approvalThreshold
	if approvedNow {
		req.Status = StatusApproved
		slog.Info("beta feature_requests: feature approved",
			"id", req.ID, "title", req.Title, "approvals", req.Approvals)
	}

	if err := f.store.update(req); err != nil {
		slog.Warn("beta feature_requests: failed to update vote", "error", err)
		return
	}

	if approvedNow && f.msgBus != nil && req.Channel != "" && req.ChatID != "" {
		f.msgBus.PublishOutbound(bus.OutboundMessage{
			Channel:  req.Channel,
			ChatID:   req.ChatID,
			Content:  fmt.Sprintf("Feature <b>%s</b> has been approved with %d votes! Use <code>build_feature</code> to start building.", htmlEscape(req.Title), req.Approvals),
			Metadata: outboundMeta(req),
		})
	}
}

func (f *FeatureRequestsFeature) handlePollClosed(event bus.Event) {
	if f == nil || f.store == nil {
		return
	}

	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return
	}
	pollID, _ := payload["poll_id"].(string)
	if pollID == "" {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	req, err := f.store.getByPollID(tenantKey(event.TenantID), pollID)
	if err != nil {
		return
	}
	if req.Status != StatusPending || req.Approvals >= approvalThreshold {
		return
	}

	req.Status = StatusRejected
	if err := f.store.update(req); err != nil {
		slog.Warn("beta feature_requests: failed to mark rejected poll", "poll_id", pollID, "error", err)
		return
	}

	slog.Info("beta feature_requests: feature rejected",
		"id", req.ID, "title", req.Title, "approvals", req.Approvals)
}

func htmlEscape(s string) string { return html.EscapeString(s) }
