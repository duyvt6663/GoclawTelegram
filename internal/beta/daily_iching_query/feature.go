package dailyichingquery

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	featureName               = "daily_iching_query"
	defaultTopK               = 5
	answerReferenceCount      = 3
	indexRefreshInterval      = 2 * time.Minute
	embeddingBatchSize        = 24
	embeddingRequestTimeout   = 20 * time.Second
	defaultEmbeddingModel     = "text-embedding-3-small"
	lowConfidenceThreshold    = 0.28
	semanticOnlyConfidenceMin = 0.42
)

// DailyIChingQueryFeature adds grounded Q&A on top of the cached daily_iching v4 index.
//
// Plan:
// 1. Load and reuse the cached daily_iching index_v4 sections/chunks, then cache chunk embeddings in beta-local tables plus memory.
// 2. Rank hits with semantic similarity first and keyword boosts for quẻ/hào markers, then render grounded Vietnamese answers with short quotes.
// 3. Expose one tool, one RPC method, and one HTTP endpoint under the existing daily_iching namespace.
type DailyIChingQueryFeature struct {
	config    *config.Config
	stores    *store.Stores
	store     queryStore
	workspace string
	dataDir   string

	indexMu         sync.RWMutex
	index           *compiledIndex
	lastIndexLoaded time.Time

	backfillMu sync.Mutex

	cacheMu           sync.RWMutex
	chunkEmbeddings   map[string][]float32
	loadedNamespaces  map[string]bool
	warmingNamespaces map[string]bool
	queryEmbeddings   map[string][]float32
}

func (f *DailyIChingQueryFeature) Name() string { return featureName }

func (f *DailyIChingQueryFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	f.config = deps.Config
	f.stores = deps.Stores
	f.store = &featureStore{db: deps.Stores.DB}
	f.workspace = deps.Workspace
	f.dataDir = deps.DataDir
	f.chunkEmbeddings = make(map[string][]float32)
	f.loadedNamespaces = make(map[string]bool)
	f.warmingNamespaces = make(map[string]bool)
	f.queryEmbeddings = make(map[string][]float32)

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	index, err := f.ensureIndex(false)
	if err != nil {
		return fmt.Errorf("%s index: %w", featureName, err)
	}

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&queryTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	slog.Info("beta daily iching query initialized",
		"source_signature", index.SourceSignature,
		"sections", len(index.Sections),
		"chunks", len(index.FlatChunks),
	)
	return nil
}
