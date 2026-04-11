package dailydiscipline

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
)

const featureName = "daily_discipline"

type telegramPollController interface {
	CreatePoll(ctx context.Context, chatID int64, threadID int, question string, options []string, openPeriodSeconds int) (string, int, error)
	StopPoll(ctx context.Context, chatID int64, messageID int) error
}

// DailyDisciplineFeature is an isolated beta feature for trust-based Telegram
// discipline check-ins with daily poll posting, per-user response capture,
// optional detailed manual submissions, and noon summary posting.
//
// Plan:
// 1. Migrate beta-local tables for configs, poll posts, and per-user responses.
// 2. Start a small scheduler loop that posts the daily survey window and summary when due.
// 3. Register tools, RPC methods, HTTP routes, Telegram command handling, and poll-answer syncing.
type DailyDisciplineFeature struct {
	store      *featureStore
	msgBus     *bus.MessageBus
	channelMgr *channels.Manager
	postMu     sync.Mutex

	schedulerCancel context.CancelFunc
	schedulerDone   chan struct{}
}

func (f *DailyDisciplineFeature) Name() string { return featureName }

func (f *DailyDisciplineFeature) Init(deps beta.Deps) error {
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
	f.msgBus = deps.MessageBus
	f.channelMgr = deps.ChannelManager

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	deps.MessageBus.Subscribe(f.pollSubscriptionID(), f.handlePollAnswer)

	telegramchannel.RegisterDynamicCommand(&disciplineCommand{feature: f})
	f.syncTelegramMenus()

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&configureTool{feature: f})
		deps.ToolRegistry.Register(&statusTool{feature: f})
		deps.ToolRegistry.Register(&runTool{feature: f})
		deps.ToolRegistry.Register(&submitResponseTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	schedulerCtx, cancel := context.WithCancel(context.Background())
	f.schedulerCancel = cancel
	f.schedulerDone = make(chan struct{})
	go f.runScheduler(schedulerCtx)

	slog.Info("beta daily discipline initialized")
	return nil
}

func (f *DailyDisciplineFeature) Shutdown(_ context.Context) error {
	if f.msgBus != nil {
		f.msgBus.Unsubscribe(f.pollSubscriptionID())
	}
	telegramchannel.UnregisterDynamicCommand("/discipline")
	if f.schedulerCancel != nil {
		f.schedulerCancel()
	}
	if f.schedulerDone != nil {
		select {
		case <-f.schedulerDone:
		case <-time.After(5 * time.Second):
			slog.Warn("beta daily discipline scheduler did not stop before timeout")
		}
	}
	return nil
}

func (f *DailyDisciplineFeature) pollSubscriptionID() string {
	return "beta." + featureName + ".poll_answer"
}

func (f *DailyDisciplineFeature) resolvePollController(channelName string) telegramPollController {
	if f == nil || f.channelMgr == nil {
		return nil
	}
	channel, ok := f.channelMgr.GetChannel(strings.TrimSpace(channelName))
	if !ok {
		return nil
	}
	controller, _ := channel.(telegramPollController)
	return controller
}

func (f *DailyDisciplineFeature) syncTelegramMenus() {
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
			slog.Warn("beta daily discipline menu sync failed", "channel", name, "error", err)
		}
		cancel()
	}
}
