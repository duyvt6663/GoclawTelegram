package telegram

import (
	"context"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

type testScopedDynamicHandler struct {
	enabledChannels map[string]bool
	enabledLocalKey string
	handleCalls     int
}

func (h *testScopedDynamicHandler) Command() string     { return "/scoped" }
func (h *testScopedDynamicHandler) Description() string { return "Scoped test command" }

func (h *testScopedDynamicHandler) Handle(_ context.Context, _ *Channel, _ DynamicCommandContext) bool {
	h.handleCalls++
	return true
}

func (h *testScopedDynamicHandler) EnabledForChannel(channel *Channel) bool {
	if channel == nil {
		return false
	}
	return h.enabledChannels[channel.Name()]
}

func (h *testScopedDynamicHandler) EnabledForContext(_ context.Context, _ *Channel, cmdCtx DynamicCommandContext) bool {
	return cmdCtx.LocalKey == h.enabledLocalKey
}

func TestDynamicCommandsRespectChannelScopeForMenusAndDispatch(t *testing.T) {
	restore := snapshotDynamicCommands()
	defer restore()

	handler := &testScopedDynamicHandler{
		enabledChannels: map[string]bool{"builder-bot": true},
		enabledLocalKey: "-1001:topic:42",
	}
	RegisterDynamicCommand(handler)

	builder := testTelegramChannel("builder-bot")
	foxy := testTelegramChannel("my-foxy-lady")

	builderCommands := RegisteredMenuCommandsForChannel(builder)
	if len(builderCommands) != 1 || builderCommands[0].Command != "scoped" {
		t.Fatalf("builder commands = %#v, want scoped command", builderCommands)
	}

	foxyCommands := RegisteredMenuCommandsForChannel(foxy)
	if len(foxyCommands) != 0 {
		t.Fatalf("foxy commands = %#v, want no scoped commands", foxyCommands)
	}

	ok := DispatchDynamicCommand(context.Background(), builder, "/scoped", DynamicCommandContext{
		Command:  "/scoped",
		LocalKey: "-1001:topic:42",
	})
	if !ok {
		t.Fatal("DispatchDynamicCommand(builder configured target) = false, want true")
	}

	ok = DispatchDynamicCommand(context.Background(), builder, "/scoped", DynamicCommandContext{
		Command:  "/scoped",
		LocalKey: "-1001:topic:777",
	})
	if ok {
		t.Fatal("DispatchDynamicCommand(builder wrong target) = true, want false")
	}

	ok = DispatchDynamicCommand(context.Background(), foxy, "/scoped", DynamicCommandContext{
		Command:  "/scoped",
		LocalKey: "-1001:topic:42",
	})
	if ok {
		t.Fatal("DispatchDynamicCommand(other channel) = true, want false")
	}

	if handler.handleCalls != 1 {
		t.Fatalf("handleCalls = %d, want 1", handler.handleCalls)
	}
}

func snapshotDynamicCommands() func() {
	dynamicCommands.mu.Lock()
	previous := dynamicCommands.handlers
	dynamicCommands.handlers = make(map[string]DynamicCommandHandler)
	dynamicCommands.mu.Unlock()

	return func() {
		dynamicCommands.mu.Lock()
		dynamicCommands.handlers = previous
		dynamicCommands.mu.Unlock()
	}
}

func testTelegramChannel(name string) *Channel {
	return &Channel{BaseChannel: channels.NewBaseChannel(name, nil, nil)}
}
