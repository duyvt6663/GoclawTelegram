package russianroulette

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
)

// RussianRouletteFeature runs a Telegram-only group mini-game behind beta flags.
//
// Plan:
// 1. Persist per-chat configs, rounds, players, and stats in beta-local tables.
// 2. Reuse one shared service for Telegram commands plus HTTP/RPC mutations.
// 3. Keep reactions and optional penalties isolated behind the beta package.
type RussianRouletteFeature struct {
	store      *featureStore
	msgBus     *bus.MessageBus
	channelMgr *channels.Manager
	mu         sync.Mutex
}

func (f *RussianRouletteFeature) Name() string { return featureName }

func (f *RussianRouletteFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}
	if deps.MessageBus == nil {
		return fmt.Errorf("%s requires a message bus", featureName)
	}

	f.store = &featureStore{db: deps.Stores.DB}
	f.msgBus = deps.MessageBus
	f.channelMgr = deps.ChannelManager

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	telegramchannel.RegisterDynamicCommand(&rouletteCommand{feature: f})
	f.syncTelegramMenus()
	topicrouting.RegisterTopicFeatureTools(
		featureName,
		(&configureTool{}).Name(),
		(&statusTool{}).Name(),
		(&leaderboardTool{}).Name(),
		(&playTool{}).Name(),
	)

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&configureTool{feature: f})
		deps.ToolRegistry.Register(&statusTool{feature: f})
		deps.ToolRegistry.Register(&leaderboardTool{feature: f})
		deps.ToolRegistry.Register(&playTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	slog.Info("beta russian roulette initialized")
	return nil
}

func (f *RussianRouletteFeature) Shutdown(_ context.Context) error {
	telegramchannel.UnregisterDynamicCommand("/roulette")
	topicrouting.UnregisterTopicFeatureTools(featureName)
	return nil
}

func (f *RussianRouletteFeature) syncTelegramMenus() {
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
			slog.Warn("beta russian roulette menu sync failed", "channel", name, "error", err)
		}
		cancel()
	}
}
