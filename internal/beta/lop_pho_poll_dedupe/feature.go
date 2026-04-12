package lopphopolldedupe

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
)

// LopPhoPollDedupeFeature exposes operator-facing visibility into DB-backed
// lớp phó poll dedupe claims that suppress duplicate `/vote_lp` emissions.
//
// Plan:
// 1. Persist per-chat/topic dedupe claims keyed by chat scope + target + time window.
// 2. Reuse the same store from lop_pho command handling so all bot instances share one writer guard.
// 3. Register status tool, RPC, and HTTP surfaces for recent suppressions and claim state.
type LopPhoPollDedupeFeature struct {
	store *Store
}

func (f *LopPhoPollDedupeFeature) Name() string { return featureName }

func (f *LopPhoPollDedupeFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	f.store = NewStore(deps.Stores.DB)
	if err := f.store.Migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&statusTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	slog.Info("beta lop pho poll dedupe initialized")
	return nil
}

func (f *LopPhoPollDedupeFeature) Shutdown(_ context.Context) error {
	return nil
}

func (f *LopPhoPollDedupeFeature) statusSnapshot(ctx context.Context, tenantID string, filter ClaimFilter) (StatusSnapshot, error) {
	if f == nil || f.store == nil {
		return StatusSnapshot{}, fmt.Errorf("lớp phó poll dedupe feature is unavailable")
	}
	claims, err := f.store.ListClaims(ctx, strings.TrimSpace(tenantID), filter)
	if err != nil {
		return StatusSnapshot{}, err
	}
	return StatusSnapshot{Claims: claims}, nil
}
