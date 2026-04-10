package beta

import (
	"context"
	"os"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// FlagSource resolves whether a beta feature is enabled.
// Checks environment variable first, then falls back to system_configs DB table.
type FlagSource struct {
	sysConfig store.SystemConfigStore // optional, nil-safe
}

// NewFlagSource creates a FlagSource. sysConfig may be nil (env-only mode).
func NewFlagSource(sc store.SystemConfigStore) *FlagSource {
	return &FlagSource{sysConfig: sc}
}

// IsEnabled checks whether a feature is enabled.
// Resolution order: env var GOCLAW_BETA_{NAME}=1|true|on → system_configs key beta.{name}.
// Env var wins if set, allowing local override without DB.
func (fs *FlagSource) IsEnabled(ctx context.Context, name string) bool {
	envKey := "GOCLAW_BETA_" + strings.ToUpper(name)
	if v := os.Getenv(envKey); v != "" {
		return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on")
	}
	if fs.sysConfig != nil {
		if val, err := fs.sysConfig.Get(ctx, "beta."+name); err == nil {
			return strings.EqualFold(val, "true")
		}
	}
	return false
}
