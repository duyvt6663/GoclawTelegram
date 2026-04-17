package jobcrawler

import (
	"fmt"
	"strings"
)

type JobCrawlerTraceResult struct {
	Config *JobCrawlerConfig  `json:"config,omitempty"`
	Run    *JobCrawlerRun     `json:"run,omitempty"`
	Traces []JobDecisionTrace `json:"traces,omitempty"`
}

func (f *JobCrawlerFeature) traceResultForRequest(tenantID, key, runID, channel, chatID string, threadID int) (*JobCrawlerTraceResult, error) {
	if f == nil || f.store == nil {
		return nil, fmt.Errorf("job crawler feature is unavailable")
	}

	runID = strings.TrimSpace(runID)
	if runID != "" {
		run, err := f.store.getRunByID(tenantID, runID)
		if err != nil {
			return nil, err
		}
		cfg, err := f.store.getConfigByID(tenantID, run.ConfigID)
		if err != nil {
			return nil, err
		}
		traces, err := f.store.listRunDecisionTraces(tenantID, run.ID)
		if err != nil {
			return nil, err
		}
		return &JobCrawlerTraceResult{
			Config: cfg,
			Run:    run,
			Traces: traces,
		}, nil
	}

	cfg, err := f.resolveRunConfig(tenantID, key, channel, chatID, threadID)
	if err != nil {
		return nil, err
	}
	run, err := f.store.lastRunByConfig(cfg.TenantID, cfg.ID)
	if err != nil {
		return nil, err
	}
	traces, err := f.store.listRunDecisionTraces(tenantID, run.ID)
	if err != nil {
		return nil, err
	}
	return &JobCrawlerTraceResult{
		Config: cfg,
		Run:    run,
		Traces: traces,
	}, nil
}
