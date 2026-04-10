package telegram

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/mymmrac/telego"
)

// DynamicCommandContext carries the normalized Telegram command plus enough
// routing metadata for beta features to reply in the same chat/topic.
type DynamicCommandContext struct {
	Command         string
	Text            string
	ChatID          int64
	ChatIDStr       string
	LocalKey        string
	SenderID        string
	IsGroup         bool
	IsForum         bool
	MessageThreadID int
	Reply           func(ctx context.Context, text string)
}

// DynamicCommandHandler lets beta features register isolated Telegram commands
// without adding feature-specific cases to the core command switch.
type DynamicCommandHandler interface {
	Command() string
	Description() string
	Handle(ctx context.Context, channel *Channel, cmdCtx DynamicCommandContext) bool
}

var dynamicCommands = struct {
	mu       sync.RWMutex
	handlers map[string]DynamicCommandHandler
}{
	handlers: make(map[string]DynamicCommandHandler),
}

func normalizeDynamicCommand(command string) string {
	command = strings.TrimSpace(strings.ToLower(command))
	if command == "" {
		return ""
	}
	if parts := strings.SplitN(command, "@", 2); len(parts) > 0 {
		command = parts[0]
	}
	if !strings.HasPrefix(command, "/") {
		command = "/" + command
	}
	return command
}

// RegisterDynamicCommand registers a runtime Telegram command handler.
func RegisterDynamicCommand(handler DynamicCommandHandler) {
	if handler == nil {
		return
	}
	command := normalizeDynamicCommand(handler.Command())
	if command == "" {
		return
	}
	dynamicCommands.mu.Lock()
	defer dynamicCommands.mu.Unlock()
	dynamicCommands.handlers[command] = handler
}

// UnregisterDynamicCommand removes a runtime Telegram command handler.
func UnregisterDynamicCommand(command string) {
	command = normalizeDynamicCommand(command)
	if command == "" {
		return
	}
	dynamicCommands.mu.Lock()
	defer dynamicCommands.mu.Unlock()
	delete(dynamicCommands.handlers, command)
}

// DispatchDynamicCommand executes a registered runtime handler if one exists.
func DispatchDynamicCommand(ctx context.Context, channel *Channel, command string, cmdCtx DynamicCommandContext) bool {
	command = normalizeDynamicCommand(command)
	if command == "" {
		return false
	}

	dynamicCommands.mu.RLock()
	handler := dynamicCommands.handlers[command]
	dynamicCommands.mu.RUnlock()
	if handler == nil {
		return false
	}
	return handler.Handle(ctx, channel, cmdCtx)
}

// RegisteredMenuCommands returns Telegram bot menu entries contributed by
// runtime command handlers, sorted for stable command syncs.
func RegisteredMenuCommands() []telego.BotCommand {
	dynamicCommands.mu.RLock()
	defer dynamicCommands.mu.RUnlock()

	if len(dynamicCommands.handlers) == 0 {
		return nil
	}

	commands := make([]telego.BotCommand, 0, len(dynamicCommands.handlers))
	for command, handler := range dynamicCommands.handlers {
		desc := strings.TrimSpace(handler.Description())
		if desc == "" {
			continue
		}
		commands = append(commands, telego.BotCommand{
			Command:     strings.TrimPrefix(command, "/"),
			Description: desc,
		})
	}
	sort.Slice(commands, func(i, j int) bool {
		return commands[i].Command < commands[j].Command
	})
	return commands
}
