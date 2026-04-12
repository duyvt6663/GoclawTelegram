package dailyiching

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
)

// DailyIChingFeature posts one grounded hexagram lesson per day, keeps
// per-chat progression state, and uses a cached local book index so runtime
// work stays limited to retrieval plus message rendering.
//
// Plan:
// 1. Load and cache the two local Kinh Dịch books into a lightweight section/chunk index.
// 2. Persist per-chat config, progression, and post history in beta-local tables.
// 3. Expose the scheduler, Telegram commands, tools, RPC methods, and HTTP routes behind one isolated beta package.
type DailyIChingFeature struct {
	store      *featureStore
	channelMgr *channels.Manager

	sourceRoot string
	cachePath  string

	indexMu sync.RWMutex
	index   *bookIndex

	postMu sync.Mutex

	schedulerCancel context.CancelFunc
	schedulerDone   chan struct{}
}

func (f *DailyIChingFeature) Name() string { return featureName }

func (f *DailyIChingFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}
	if deps.ChannelManager == nil {
		return fmt.Errorf("%s requires a channel manager", featureName)
	}

	f.store = &featureStore{db: deps.Stores.DB}
	f.channelMgr = deps.ChannelManager

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	sourceRoot, err := resolveBookSourceRoot(deps.Workspace)
	if err != nil {
		return fmt.Errorf("%s sources: %w", featureName, err)
	}
	cachePath, err := resolveBookCachePath(deps.Workspace, deps.DataDir)
	if err != nil {
		return fmt.Errorf("%s cache: %w", featureName, err)
	}
	index, err := loadOrBuildBookIndex(sourceRoot, cachePath)
	if err != nil {
		return fmt.Errorf("%s index: %w", featureName, err)
	}
	f.sourceRoot = sourceRoot
	f.cachePath = cachePath
	f.index = index

	telegramchannel.RegisterDynamicCommand(&ichingCommand{feature: f})
	f.syncTelegramMenus()
	topicrouting.RegisterTopicFeatureTools(
		featureName,
		(&configureTool{}).Name(),
		(&statusTool{}).Name(),
		(&runTool{}).Name(),
	)

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&configureTool{feature: f})
		deps.ToolRegistry.Register(&statusTool{feature: f})
		deps.ToolRegistry.Register(&runTool{feature: f})
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

	slog.Info("beta daily iching initialized",
		"source_root", f.sourceRoot,
		"cache_path", f.cachePath,
		"extractor", f.index.Extractor,
		"sources", len(f.index.Sources),
		"hexagrams", len(f.index.Sections),
	)
	return nil
}

func (f *DailyIChingFeature) Shutdown(_ context.Context) error {
	telegramchannel.UnregisterDynamicCommand("/iching")
	topicrouting.UnregisterTopicFeatureTools(featureName)
	if f.schedulerCancel != nil {
		f.schedulerCancel()
	}
	if f.schedulerDone != nil {
		select {
		case <-f.schedulerDone:
		case <-time.After(5 * time.Second):
			slog.Warn("beta daily iching scheduler did not stop before timeout")
		}
	}
	return nil
}

func (f *DailyIChingFeature) syncTelegramMenus() {
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
			slog.Warn("beta daily iching menu sync failed", "channel", name, "error", err)
		}
		cancel()
	}
}

func (f *DailyIChingFeature) indexSnapshot() *bookIndex {
	f.indexMu.RLock()
	defer f.indexMu.RUnlock()
	return f.index
}
