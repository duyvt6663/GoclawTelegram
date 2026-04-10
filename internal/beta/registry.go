package beta

import (
	"context"
	"log/slog"
)

// registry holds all compiled-in beta features.
// Features register themselves via init() in their package.
var registry []Feature

// Register adds a feature to the global registry. Called from init().
func Register(f Feature) {
	registry = append(registry, f)
}

// ActivateAll checks flags and initializes enabled features.
// Returns Shutdownable features for deferred cleanup.
func ActivateAll(ctx context.Context, flags *FlagSource, deps Deps) []Shutdownable {
	var shutdowns []Shutdownable
	for _, f := range registry {
		if !flags.IsEnabled(ctx, f.Name()) {
			slog.Info("beta feature disabled", "feature", f.Name())
			continue
		}
		slog.Info("beta feature activating", "feature", f.Name())
		if err := f.Init(deps); err != nil {
			slog.Error("beta feature failed to init", "feature", f.Name(), "error", err)
			continue
		}
		slog.Info("beta feature activated", "feature", f.Name())
		if s, ok := f.(Shutdownable); ok {
			shutdowns = append(shutdowns, s)
		}
	}
	return shutdowns
}
