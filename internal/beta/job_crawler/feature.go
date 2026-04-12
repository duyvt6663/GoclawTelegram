package jobcrawler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const featureName = "job_crawler"

// JobCrawlerFeature is an isolated beta feature that maintains per-topic remote
// job feeds for Telegram chats.
//
// Plan:
// 1. Add beta-local semantic primitives: embedding cache tables, provider resolution, and conservative API throttling.
// 2. Rank v1.6 with semantic similarity first, then blend keyword, location, role, recency, dynamic boosts, and penalties.
// 3. Support per-run natural-language overrides plus optional LLM rerank without mutating stored crawler configs.
type JobCrawlerFeature struct {
	config     *config.Config
	stores     *store.Stores
	store      *featureStore
	channelMgr *channels.Manager
	crawl4ai   *crawl4aiClient

	schedulerCancel context.CancelFunc
	schedulerDone   chan struct{}

	runMu   sync.Mutex
	running map[string]struct{}

	cacheMu     sync.Mutex
	sourceCache map[string]cachedSourceResult

	embeddingLimiter *apiCallLimiter
	llmLimiter       *apiCallLimiter
}

func (f *JobCrawlerFeature) Name() string { return featureName }

func (f *JobCrawlerFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}
	if deps.ChannelManager == nil {
		return fmt.Errorf("%s requires a channel manager", featureName)
	}

	f.config = deps.Config
	f.stores = deps.Stores
	f.store = &featureStore{db: deps.Stores.DB}
	f.channelMgr = deps.ChannelManager
	f.running = make(map[string]struct{})
	f.sourceCache = make(map[string]cachedSourceResult)
	f.embeddingLimiter = newAPICallLimiter(embeddingThrottleInterval)
	f.llmLimiter = newAPICallLimiter(llmThrottleInterval)
	f.crawl4ai = newCrawl4AIClient(
		os.Getenv("GOCLAW_BETA_JOB_CRAWLER_CRAWL4AI_URL"),
		os.Getenv("GOCLAW_BETA_JOB_CRAWLER_CRAWL4AI_TOKEN"),
	)

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}
	topicrouting.RegisterTopicFeatureTools(
		featureName,
		(&configUpsertTool{}).Name(),
		(&listConfigsTool{}).Name(),
		(&runCrawlerTool{}).Name(),
		(&runDynamicCrawlerTool{}).Name(),
	)

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&configUpsertTool{feature: f})
		deps.ToolRegistry.Register(&listConfigsTool{feature: f})
		deps.ToolRegistry.Register(&runCrawlerTool{feature: f})
		deps.ToolRegistry.Register(&runDynamicCrawlerTool{feature: f})
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

	if f.crawl4ai != nil {
		slog.Info("beta job crawler initialized", "crawl4ai", strings.TrimSpace(f.crawl4ai.baseURL))
	} else {
		slog.Info("beta job crawler initialized", "crawl4ai", "disabled")
	}
	return nil
}

func (f *JobCrawlerFeature) Shutdown(_ context.Context) error {
	topicrouting.UnregisterTopicFeatureTools(featureName)
	if f.schedulerCancel != nil {
		f.schedulerCancel()
	}
	if f.schedulerDone != nil {
		select {
		case <-f.schedulerDone:
		case <-time.After(5 * time.Second):
			slog.Warn("beta job crawler scheduler did not stop before timeout")
		}
	}
	return nil
}

func (f *JobCrawlerFeature) tryAcquireRun(configID string) bool {
	f.runMu.Lock()
	defer f.runMu.Unlock()
	if _, ok := f.running[configID]; ok {
		return false
	}
	f.running[configID] = struct{}{}
	return true
}

func (f *JobCrawlerFeature) releaseRun(configID string) {
	f.runMu.Lock()
	defer f.runMu.Unlock()
	delete(f.running, configID)
}
