package beta

import (
	"context"
	"net/http"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// Feature is the core interface every beta feature implements.
// Each feature self-registers via init() and is activated at startup
// when its runtime flag is enabled.
type Feature interface {
	// Name returns the feature flag name (e.g., "polls").
	// Used as env var suffix: GOCLAW_BETA_POLLS=1
	Name() string

	// Init is called at startup when the feature flag is active.
	// Features register their tools, RPC methods, HTTP routes, and
	// run any self-migrations inside Init.
	Init(deps Deps) error
}

// Deps bundles all dependencies a beta feature might need.
// Features take what they need, ignore the rest.
type Deps struct {
	Config         *config.Config
	Stores         *store.Stores
	ToolRegistry   *tools.Registry
	MethodRouter   *gateway.MethodRouter
	Server         *gateway.Server
	MessageBus     *bus.MessageBus
	ChannelManager *channels.Manager
	Workspace      string
	DataDir        string
}

// Shutdownable is optionally implemented by features that need cleanup on shutdown.
type Shutdownable interface {
	Shutdown(ctx context.Context) error
}

// RouteHandler wraps an HTTP handler for beta features to register routes
// without importing internal gateway types.
type RouteHandler struct {
	Pattern string
	Handler http.HandlerFunc
}
