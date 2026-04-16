package linkedinjobsproxy

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
)

// LinkedInJobsProxyFeature adds a search-proxy ingestion path for public
// LinkedIn job previews without direct scraping or login.
//
// Plan:
// 1. Search LinkedIn job pages indirectly through the configured search proxy and cache each normalized request for 24h.
// 2. Enrich candidates with lightweight public preview metadata, then apply strict title, role, exclusion, and dedupe rules.
// 3. Expose the proxy through a beta tool, RPC method, and HTTP route so other beta features can reuse the same service.
type LinkedInJobsProxyFeature struct {
	service *Service
}

func (f *LinkedInJobsProxyFeature) Name() string { return featureName }

func (f *LinkedInJobsProxyFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	f.service = NewService(deps.Stores.DB)
	if err := f.service.Migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	topicrouting.RegisterTopicFeatureTools(featureName, (&searchTool{}).Name())
	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&searchTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	if f.service.Available() {
		slog.Info("beta linkedin jobs proxy initialized", "provider", searchProviderLinkup)
	} else {
		slog.Warn("beta linkedin jobs proxy initialized without a configured search proxy")
	}
	return nil
}

func (f *LinkedInJobsProxyFeature) Shutdown(_ context.Context) error {
	topicrouting.UnregisterTopicFeatureTools(featureName)
	return nil
}
