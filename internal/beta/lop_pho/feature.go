package loppho

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	lopphopolldedupe "github.com/nextlevelbuilder/goclaw/internal/beta/lop_pho_poll_dedupe"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/classroles"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type telegramPollController interface {
	CreatePoll(ctx context.Context, chatID int64, threadID int, question string, options []string, openPeriodSeconds int) (string, int, error)
	StopPoll(ctx context.Context, chatID int64, messageID int) error
}

type telegramModerator interface {
	MuteMember(ctx context.Context, chatID int64, userID int64, duration time.Duration) error
}

// LopPhoFeature adds DB-backed lớp phó voting to Telegram groups.
//
// Plan:
// 1. Persist granted lớp phó roles plus per-target polls and per-voter selections in beta-local tables.
// 2. Reuse one open-vote path across Telegram commands, tools, RPC, and HTTP handlers.
// 3. Resolve poll thresholds in real time to grant lớp phó, attach a sticker, or mute for 2 hours.
type LopPhoFeature struct {
	store      *featureStore
	dedupe     *lopphopolldedupe.Store
	msgBus     *bus.MessageBus
	channelMgr *channels.Manager
	contacts   store.ContactStore
	agentStore store.AgentStore
	workspace  string

	pollMu sync.Mutex
}

func (f *LopPhoFeature) Name() string { return featureName }

func (f *LopPhoFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}
	if deps.MessageBus == nil {
		return fmt.Errorf("%s requires a message bus", featureName)
	}
	if deps.ChannelManager == nil {
		return fmt.Errorf("%s requires a channel manager", featureName)
	}

	f.store = &featureStore{db: deps.Stores.DB}
	f.dedupe = lopphopolldedupe.NewStore(deps.Stores.DB)
	f.msgBus = deps.MessageBus
	f.channelMgr = deps.ChannelManager
	f.workspace = deps.Workspace
	if deps.Stores != nil {
		f.contacts = deps.Stores.Contacts
		f.agentStore = deps.Stores.Agents
	}

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}
	if err := f.dedupe.Migrate(); err != nil {
		return fmt.Errorf("%s dedupe migration: %w", featureName, err)
	}

	classroles.SetLopPhoChecker(f.isLopPhoActor)

	deps.MessageBus.Subscribe(f.pollAnswerSubscriptionID(), f.handlePollAnswer)
	deps.MessageBus.Subscribe(f.pollClosedSubscriptionID(), f.handlePollClosed)

	telegramchannel.RegisterDynamicCommand(&voteCommand{feature: f})
	f.syncTelegramMenus()
	topicrouting.RegisterTopicFeatureTools(
		featureName,
		(&openVoteTool{}).Name(),
		(&statusTool{}).Name(),
	)

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&openVoteTool{feature: f})
		deps.ToolRegistry.Register(&statusTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	slog.Info("beta lop pho initialized")
	return nil
}

func (f *LopPhoFeature) Shutdown(_ context.Context) error {
	if f.msgBus != nil {
		f.msgBus.Unsubscribe(f.pollAnswerSubscriptionID())
		f.msgBus.Unsubscribe(f.pollClosedSubscriptionID())
	}
	classroles.SetLopPhoChecker(nil)
	telegramchannel.UnregisterDynamicCommand(voteCommandName)
	topicrouting.UnregisterTopicFeatureTools(featureName)
	return nil
}

func (f *LopPhoFeature) pollAnswerSubscriptionID() string {
	return "beta." + featureName + ".poll_answer"
}

func (f *LopPhoFeature) pollClosedSubscriptionID() string {
	return "beta." + featureName + ".poll_closed"
}

func (f *LopPhoFeature) isLopPhoActor(ctx context.Context, senderID string) bool {
	if f == nil || f.store == nil {
		return false
	}
	ok, err := f.store.isLopPho(tenantKeyFromCtx(ctx), senderID)
	return err == nil && ok
}

func (f *LopPhoFeature) resolvePollController(channelName string) telegramPollController {
	if f == nil || f.channelMgr == nil {
		return nil
	}
	channel, ok := f.channelMgr.GetChannel(channelName)
	if !ok {
		return nil
	}
	controller, _ := channel.(telegramPollController)
	return controller
}

func (f *LopPhoFeature) resolveModerator(channelName string) telegramModerator {
	if f == nil || f.channelMgr == nil {
		return nil
	}
	channel, ok := f.channelMgr.GetChannel(channelName)
	if !ok {
		return nil
	}
	moderator, _ := channel.(telegramModerator)
	return moderator
}

func (f *LopPhoFeature) syncTelegramMenus() {
	if f == nil || f.channelMgr == nil {
		return
	}
	for _, name := range f.channelMgr.GetEnabledChannels() {
		rawChannel, ok := f.channelMgr.GetChannel(name)
		if !ok || rawChannel.Type() != channels.TypeTelegram {
			continue
		}
		channel, ok := rawChannel.(*telegramchannel.Channel)
		if !ok {
			continue
		}
		tg, ok := rawChannel.(interface {
			SyncMenuCommands(ctx context.Context, commands []telego.BotCommand) error
		})
		if !ok {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := tg.SyncMenuCommands(ctx, telegramchannel.DefaultMenuCommandsForChannel(channel)); err != nil {
			slog.Warn("beta lop pho menu sync failed", "channel", name, "error", err)
		}
		cancel()
	}
}

func (f *LopPhoFeature) commandEnabledForChannel(channel *telegramchannel.Channel) bool {
	if f == nil || f.agentStore == nil || channel == nil {
		return false
	}

	agentKey := strings.TrimSpace(channel.AgentID())
	if agentKey == "" {
		return false
	}

	ctx := store.WithTenantID(context.Background(), channel.TenantID())
	agent, err := f.agentStore.GetByKey(ctx, agentKey)
	if err != nil || agent == nil {
		return false
	}

	spec := agent.ParseToolsConfig()
	return toolPolicyExplicitlyAllows(spec, (&openVoteTool{}).Name())
}

func toolPolicyExplicitlyAllows(spec *config.ToolPolicySpec, toolName string) bool {
	if spec == nil {
		return false
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return false
	}
	return toolPolicyListContains(spec.Allow, toolName) || toolPolicyListContains(spec.AlsoAllow, toolName)
}

func toolPolicyListContains(values []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}
