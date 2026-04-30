package eaterychat

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
)

const featureName = "eatery_chat"

// EateryChatFeature adds chat-native ingestion and prompt recommendations on top of the shared eatery list.
//
// Plan:
// 1. Parse free-text chat submissions into structured, tagged eatery candidates.
// 2. Insert confident parses immediately, but store low-confidence parses as pending suggestions that require confirmation.
// 3. Rank recommendations by parsed budget, district, group size, category, and vibe tags while mirroring confirmed entries into the shared list table.
type EateryChatFeature struct {
	store *featureStore
}

func (f *EateryChatFeature) Name() string { return featureName }

func (f *EateryChatFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	f.store = &featureStore{db: deps.Stores.DB}
	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	ingestTool := &ingestTool{feature: f}
	confirmTool := &confirmTool{feature: f}
	recommendTool := &recommendTool{feature: f}
	listTool := &listTool{feature: f}

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(ingestTool)
		deps.ToolRegistry.Register(confirmTool)
		deps.ToolRegistry.Register(recommendTool)
		deps.ToolRegistry.Register(listTool)
	}
	topicrouting.RegisterTopicFeatureTools(
		featureName,
		ingestTool.Name(),
		confirmTool.Name(),
		recommendTool.Name(),
		listTool.Name(),
	)

	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	slog.Info("beta eatery chat initialized")
	return nil
}

func (f *EateryChatFeature) Shutdown(_ context.Context) error {
	topicrouting.UnregisterTopicFeatureTools(featureName)
	return nil
}
