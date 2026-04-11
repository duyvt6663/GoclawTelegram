// Package all aggregates beta feature imports.
// Add one blank import per beta feature. Remove when promoting or dropping.
package all

import (
	_ "github.com/nextlevelbuilder/goclaw/internal/beta/_example"
	_ "github.com/nextlevelbuilder/goclaw/internal/beta/daily_discipline"
	_ "github.com/nextlevelbuilder/goclaw/internal/beta/feature_requests"
	_ "github.com/nextlevelbuilder/goclaw/internal/beta/job_crawler"
	_ "github.com/nextlevelbuilder/goclaw/internal/beta/russian_roulette"
)
