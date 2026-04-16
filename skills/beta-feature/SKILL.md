---
name: beta-feature
description: |
  Create new GoClaw beta features in isolated folders. Use when asked to build a new feature,
  add a new tool, add RPC methods, or add HTTP endpoints as a beta/experimental feature.
  Triggers on: "new beta feature", "add feature", "create feature folder", "beta tool",
  "experimental feature", "feature flag", "isolated feature".
metadata:
  author: GoClaw
  version: "1.1.0"
---

# Beta Feature Builder

Build self-contained beta features under `internal/beta/{name}/`. Each feature is flag-gated, developed in isolation, and wired at startup via `init()` self-registration.

## Architecture

```
internal/beta/
    feature.go          # Feature interface, Deps struct
    flags.go            # FlagSource (env + system_configs DB)
    registry.go         # Register(), ActivateAll()
    all/all.go          # Blank imports aggregator
    {feature_name}/     # One folder per feature
        init.go
        feature.go
        tool_*.go       # Agent-facing tools (optional)
        methods.go      # RPC method handlers (optional)
        handler.go      # HTTP routes (optional)
        store.go        # Feature-local DB store (optional)
```

## Step-by-Step: Create a New Beta Feature

### 1. Create the feature directory

```bash
mkdir -p internal/beta/{name}
```

### 2. Create init.go (self-registration)

```go
package {name}

import "github.com/nextlevelbuilder/goclaw/internal/beta"

func init() {
    beta.Register(&{Name}Feature{})
}
```

### 3. Create feature.go (entry point)

```go
package {name}

import (
    "github.com/nextlevelbuilder/goclaw/internal/beta"
)

type {Name}Feature struct{}

func (f *{Name}Feature) Name() string { return "{name}" }

func (f *{Name}Feature) Init(deps beta.Deps) error {
    // Optional: self-migrate DB tables
    // if err := f.migrate(deps.Stores.DB); err != nil {
    //     return err
    // }

    // Register tools, methods, routes here
    // deps.ToolRegistry.Register(...)
    // deps.MethodRouter.Register(...)
    // deps.Server.AddRouteRegistrar(...)

    return nil
}
```

### 4. Register in the aggregator

Add one blank import line to `internal/beta/all/all.go`:

```go
import (
    _ "github.com/nextlevelbuilder/goclaw/internal/beta/{name}"
)
```

### 5. Enable the feature

```bash
export GOCLAW_BETA_{NAME_UPPER}=1
```

Or via DB: insert into `system_configs` table with key `beta.{name}`, value `true`.

## Deps Available to Features

The `beta.Deps` struct passed to `Init()`:

| Field | Type | Use for |
|-------|------|---------|
| `Config` | `*config.Config` | Read gateway configuration |
| `Stores` | `*store.Stores` | Access all store interfaces + `.DB` for raw SQL |
| `ToolRegistry` | `*tools.Registry` | Register agent-facing tools |
| `MethodRouter` | `*gateway.MethodRouter` | Register WebSocket RPC methods |
| `Server` | `*gateway.Server` | Register HTTP routes via `AddRouteRegistrar()` |
| `MessageBus` | `*bus.MessageBus` | Subscribe/publish events |
| `Workspace` | `string` | Workspace directory path |
| `DataDir` | `string` | Data directory path |

## Telegram Runtime Safety Rules

When a beta feature registers Telegram runtime handlers, treat them as **process-global** by default.

- `telegramchannel.RegisterDynamicCommand(...)` is visible to every Telegram bot in the gateway unless the handler implements `EnabledForChannel(channel *telegramchannel.Channel) bool`.
- `telegramchannel.RegisterDynamicUploadHandler(...)` is visible to every Telegram bot in the gateway unless the handler implements `EnabledForChannel(channel *telegramchannel.Channel) bool`.
- Do **not** assume “the current bot” is implicit. A globally registered handler will fire on every matching bot unless you gate it explicitly.

Required rule for new Telegram features:

1. If the feature registers a dynamic command, upload handler, or similar Telegram runtime hook, implement channel scoping.
2. Gate that scoping using the owning agent’s `tools_config` allowlist and/or topic routing, following existing patterns such as `internal/beta/lop_pho/feature.go`.
3. Add a regression test that proves the intended bot is enabled and unrelated bots are disabled.

Do not ship Telegram features that:

- register runtime handlers globally with no channel gate
- auto-create per-channel state from an unscoped handler
- assume a command/upload should be handled by every Telegram bot in the process

## Writable Storage Rules

Do not assume `deps.DataDir` is writable in every runtime. Beta features that cache files locally must resolve a writable storage root.

Required rule for new file-caching features:

1. Probe the configured `DataDir` before relying on it for writes.
2. If it is not writable, fall back to a workspace-local cache such as `filepath.Join(deps.Workspace, "beta_cache", <feature_name>)`.
3. If workspace-local storage is unavailable, fall back to a temp directory under `/tmp`.
4. Add a regression test that proves the feature falls back cleanly when the configured data dir is not writable.

Prefer feature-local cache roots over writing directly into shared global paths. Keep cached files under the feature’s own subtree.

## Adding Agent Tools

Implement `tools.Tool` interface. Register in `Init()`.

```go
type myTool struct{}

func (t *myTool) Name() string        { return "my_tool_name" }
func (t *myTool) Description() string { return "What this tool does" }
func (t *myTool) Parameters() map[string]any {
    return map[string]any{
        "type": "object",
        "properties": map[string]any{
            "param1": map[string]any{"type": "string", "description": "Param description"},
        },
        "required": []string{"param1"},
    }
}
func (t *myTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
    val, _ := args["param1"].(string)
    return tools.NewResult("output: " + val)
}
```

Optional tool interfaces (implement to opt in):
- `ContextualTool` — receive channel/chatID context
- `SessionStoreAware` — receive SessionStore
- `BusAware` — receive MessageBus
- `ChannelSenderAware` — send messages to channels
- `AsyncTool` — async execution with callback

## Adding RPC Methods

Register in `Init()` with `beta.{feature}.{action}` naming:

```go
deps.MethodRouter.Register("beta.{name}.list", func(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
    // Parse params from req.Payload
    // Do work
    client.SendResponse(protocol.NewOKResponse(req.ID, result))
})
```

For errors: `client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, "message"))`

## Adding HTTP Routes

Implement `gateway.RouteRegistrar` and call `deps.Server.AddRouteRegistrar()`:

```go
type myHandler struct{}

func (h *myHandler) RegisterRoutes(mux *http.ServeMux) {
    mux.HandleFunc("/api/v1/beta/{name}/endpoint", h.handle)
}

func (h *myHandler) handle(w http.ResponseWriter, r *http.Request) {
    // Handle HTTP request
}
```

Register in Init: `deps.Server.AddRouteRegistrar(&myHandler{})`

## Adding DB Tables (Self-Migration)

Use `CREATE TABLE IF NOT EXISTS` in `Init()`. Prefix tables with `beta_`.

```go
func (f *{Name}Feature) migrate(db *sql.DB) error {
    _, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS beta_{name} (
            id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            data JSONB NOT NULL,
            created_at TIMESTAMPTZ DEFAULT NOW()
        );
    `)
    return err
}
```

For SQLite compatibility, use `TEXT` instead of `UUID`/`JSONB` and `DATETIME` instead of `TIMESTAMPTZ`.

## Optional: Shutdown Cleanup

Implement `beta.Shutdownable` for cleanup on gateway shutdown:

```go
func (f *{Name}Feature) Shutdown(ctx context.Context) error {
    // Close connections, flush buffers, etc.
    return nil
}
```

## Naming Conventions

| Thing | Convention | Example |
|-------|-----------|---------|
| Package | lowercase feature name | `polls` |
| Feature flag | `GOCLAW_BETA_{NAME}` | `GOCLAW_BETA_POLLS=1` |
| DB tables | `beta_{name}` prefix | `beta_polls` |
| RPC methods | `beta.{name}.{action}` | `beta.polls.create` |
| HTTP routes | `/api/v1/beta/{name}/` | `/api/v1/beta/polls/` |
| Tool names | descriptive, globally unique | `create_poll` |

## Feature Lifecycle

- **Promote to core:** Move code to `internal/`, add proper migration in `migrations/`, rename `beta_` tables, remove import from `all/all.go`
- **Drop:** `rm -rf internal/beta/{name}/`, remove import from `all/all.go`

## Reference Implementation

See `internal/beta/_example/` for a complete working example with an echo tool and ping RPC method. Enable with `GOCLAW_BETA_EXAMPLE=1`.

## Verification

After creating a beta feature, run:

```bash
go build ./...                      # PG build
go build -tags sqliteonly ./...     # SQLite build
go vet ./...                        # Static analysis
```

For Telegram features, also verify:

- only the intended bot(s) expose the dynamic command/upload handler
- unrelated Telegram bots stay silent for the same trigger
- any cached files can actually be written in the target runtime
