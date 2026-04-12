package dailyichingindexv4

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	dailyiching "github.com/nextlevelbuilder/goclaw/internal/beta/daily_iching"
)

const featureName = "daily_iching_index_v4"

// DailyIChingIndexV4Feature exposes inspection and validation for the upgraded
// daily_iching index_v4 pipeline.
//
// Plan:
// 1. Build or rebuild index_v4 from the shared daily_iching source books and surface sample chunks.
// 2. Persist snapshot metadata plus v2/v3/v4 comparison runs in beta-local tables.
// 3. Register one tool, one status tool, RPC methods, and HTTP routes for inspection.
type DailyIChingIndexV4Feature struct {
	store     *featureStore
	workspace string
	dataDir   string
}

func (f *DailyIChingIndexV4Feature) Name() string { return featureName }

func (f *DailyIChingIndexV4Feature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	f.store = &featureStore{db: deps.Stores.DB}
	f.workspace = deps.Workspace
	f.dataDir = deps.DataDir

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&statusTool{feature: f})
		deps.ToolRegistry.Register(&compareTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	return nil
}

func (f *DailyIChingIndexV4Feature) liveReport(queries []string, rebuild bool) (*dailyiching.IndexInspectionReport, error) {
	return dailyiching.BuildIndexInspectionReport(f.workspace, f.dataDir, queries, rebuild)
}

func (f *DailyIChingIndexV4Feature) statusPayload(ctx context.Context) (map[string]any, error) {
	report, err := f.liveReport(nil, false)
	if err != nil {
		return nil, err
	}
	tenantID := tenantKeyFromCtx(ctx)
	snapshot, err := f.store.latestSnapshot(tenantID)
	if err != nil {
		return nil, err
	}
	run, err := f.store.latestRun(tenantID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"live":            report,
		"latest_snapshot": snapshot,
		"latest_run":      run,
	}, nil
}

func (f *DailyIChingIndexV4Feature) comparePayload(ctx context.Context, queries []string, rebuild bool) (map[string]any, error) {
	if len(queries) == 0 {
		queries = dailyiching.DefaultIndexInspectionQueries()
	}

	report, err := f.liveReport(queries, rebuild)
	if err != nil {
		return nil, err
	}

	snapshot, err := f.persistSnapshot(ctx, report)
	if err != nil {
		return nil, err
	}
	run, err := f.persistRun(ctx, report)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"report":   report,
		"snapshot": snapshot,
		"run":      run,
	}, nil
}

func (f *DailyIChingIndexV4Feature) persistSnapshot(ctx context.Context, report *dailyiching.IndexInspectionReport) (*snapshotRecord, error) {
	if report == nil {
		return nil, nil
	}
	summary := findVersionSummary(report, "v4")
	if summary == nil || !summary.Available {
		return nil, nil
	}

	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}

	record := &snapshotRecord{
		TenantID:        tenantKeyFromCtx(ctx),
		SourceSignature: summary.SourceSignature,
		IndexVersion:    summary.IndexVersion,
		Extractor:       summary.Extractor,
		SourceRoot:      report.SourceRoot,
		CachePath:       summary.CachePath,
		SourceCount:     summary.SourceCount,
		HexagramCount:   summary.HexagramCount,
		ChunkCount:      summary.ChunkCount,
		Summary:         summaryJSON,
	}
	if summary.GeneratedAt != nil {
		generatedAt := summary.GeneratedAt.UTC()
		record.GeneratedAt = &generatedAt
	}

	return f.store.upsertSnapshot(record)
}

func (f *DailyIChingIndexV4Feature) persistRun(ctx context.Context, report *dailyiching.IndexInspectionReport) (*compareRunRecord, error) {
	if report == nil {
		return nil, nil
	}
	summary := findVersionSummary(report, "v4")
	if summary == nil || !summary.Available {
		return nil, nil
	}

	reportJSON, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}

	record := &compareRunRecord{
		TenantID:        tenantKeyFromCtx(ctx),
		SourceSignature: summary.SourceSignature,
		IndexVersion:    summary.IndexVersion,
		QueryCount:      len(report.RequestedQueries),
		Report:          reportJSON,
	}
	return f.store.insertRun(record)
}

func findVersionSummary(report *dailyiching.IndexInspectionReport, label string) *dailyiching.IndexVersionSummary {
	if report == nil {
		return nil
	}
	for i := range report.Versions {
		if report.Versions[i].Label == label {
			return &report.Versions[i]
		}
	}
	return nil
}
