package sharedeaterylist

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
)

const featureName = "shared_eatery_list"

// SharedEateryListFeature keeps a structured, tenant-scoped eatery database for group chat recommendations.
//
// Plan:
// 1. Store canonical eatery rows in beta_shared_eatery_list_entries with normalized dedupe/filter keys.
// 2. Expose the same add/list/random flows through tools, RPC methods, and HTTP routes.
// 3. Attribute entries to the caller or supplied Telegram contributor while keeping runtime hooks channel-neutral.
type SharedEateryListFeature struct {
	store *featureStore
}

func (f *SharedEateryListFeature) Name() string { return featureName }

func (f *SharedEateryListFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	f.store = &featureStore{db: deps.Stores.DB}
	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	addTool := &addEateryTool{feature: f}
	listTool := &listEateriesTool{feature: f}
	randomTool := &randomEateryTool{feature: f}

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(addTool)
		deps.ToolRegistry.Register(listTool)
		deps.ToolRegistry.Register(randomTool)
	}
	topicrouting.RegisterTopicFeatureTools(
		featureName,
		addTool.Name(),
		listTool.Name(),
		randomTool.Name(),
	)

	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	slog.Info("beta shared eatery list initialized")
	return nil
}

func (f *SharedEateryListFeature) Shutdown(_ context.Context) error {
	topicrouting.UnregisterTopicFeatureTools(featureName)
	return nil
}
