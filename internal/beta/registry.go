package beta

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
)

// registry holds all compiled-in beta features.
// Features register themselves via init() in their package.
var (
	registryMu      sync.Mutex
	registry        []Feature
	activeFeatures  = make(map[string]Feature)
	activeShutdowns = make(map[string]Shutdownable)
)

// Register adds a feature to the global registry. Called from init().
func Register(f Feature) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, f)
}

// ActivateAll checks flags and initializes enabled features.
// Returns Shutdownable features for deferred cleanup.
func ActivateAll(ctx context.Context, flags *FlagSource, deps Deps) []Shutdownable {
	registryMu.Lock()
	defer registryMu.Unlock()

	for _, f := range registry {
		if isActiveLocked(f.Name()) {
			continue
		}
		if flags != nil && !flags.IsEnabled(ctx, f.Name()) {
			slog.Info("beta feature disabled", "feature", f.Name())
			continue
		}
		if _, err := activateLocked(ctx, deps, f); err != nil {
			continue
		}
	}
	return snapshotShutdownsLocked()
}

// ActivatePending activates any newly-enabled compiled beta features without
// re-initializing features that are already active. Useful for reacting to
// system_configs changes at runtime.
func ActivatePending(ctx context.Context, flags *FlagSource, deps Deps) []string {
	registryMu.Lock()
	defer registryMu.Unlock()

	var activated []string
	for _, f := range registry {
		if isActiveLocked(f.Name()) {
			continue
		}
		if flags != nil && !flags.IsEnabled(ctx, f.Name()) {
			continue
		}
		if ok, err := activateLocked(ctx, deps, f); err == nil && ok {
			activated = append(activated, f.Name())
		}
	}
	return activated
}

// EnsureActive activates a specific compiled beta feature if it is enabled for
// the current tenant and not already active.
func EnsureActive(ctx context.Context, flags *FlagSource, deps Deps, name string) (bool, error) {
	registryMu.Lock()
	defer registryMu.Unlock()

	feature, ok := findLocked(name)
	if !ok {
		return false, fmt.Errorf("unknown beta feature: %s", strings.TrimSpace(name))
	}
	if isActiveLocked(feature.Name()) {
		return false, nil
	}
	if flags != nil && !flags.IsEnabled(ctx, feature.Name()) {
		return false, fmt.Errorf("beta feature is not enabled for this tenant: %s", feature.Name())
	}
	return activateLocked(ctx, deps, feature)
}

// IsRegistered reports whether a compiled beta feature with this name exists.
func IsRegistered(name string) bool {
	registryMu.Lock()
	defer registryMu.Unlock()
	_, ok := findLocked(name)
	return ok
}

// IsActive reports whether a beta feature has already been initialized in this process.
func IsActive(name string) bool {
	registryMu.Lock()
	defer registryMu.Unlock()
	return isActiveLocked(name)
}

// ShutdownAll stops every active beta feature exactly once.
func ShutdownAll(ctx context.Context) {
	registryMu.Lock()
	shutdowns := snapshotShutdownsLocked()
	activeFeatures = make(map[string]Feature)
	activeShutdowns = make(map[string]Shutdownable)
	registryMu.Unlock()

	for _, s := range shutdowns {
		if s == nil {
			continue
		}
		if err := s.Shutdown(ctx); err != nil {
			slog.Warn("beta feature shutdown failed", "error", err)
		}
	}

	topicrouting.Clear()
}

func activateLocked(ctx context.Context, deps Deps, f Feature) (bool, error) {
	name := strings.TrimSpace(f.Name())
	if name == "" {
		return false, fmt.Errorf("beta feature has empty name")
	}
	if isActiveLocked(name) {
		return false, nil
	}

	slog.Info("beta feature activating", "feature", name)
	if err := f.Init(deps); err != nil {
		slog.Error("beta feature failed to init", "feature", name, "error", err)
		return false, err
	}
	activeFeatures[name] = f
	if s, ok := f.(Shutdownable); ok {
		activeShutdowns[name] = s
	}
	slog.Info("beta feature activated", "feature", name)
	return true, nil
}

func findLocked(name string) (Feature, bool) {
	needle := strings.TrimSpace(name)
	for _, f := range registry {
		if strings.TrimSpace(f.Name()) == needle {
			return f, true
		}
	}
	return nil, false
}

func isActiveLocked(name string) bool {
	_, ok := activeFeatures[strings.TrimSpace(name)]
	return ok
}

func snapshotShutdownsLocked() []Shutdownable {
	shutdowns := make([]Shutdownable, 0, len(activeShutdowns))
	for _, s := range activeShutdowns {
		shutdowns = append(shutdowns, s)
	}
	return shutdowns
}
