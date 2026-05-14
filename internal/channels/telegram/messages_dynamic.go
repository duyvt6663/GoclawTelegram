package telegram

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

// DynamicMessageContext carries a Telegram text/caption message plus enough
// routing metadata for beta features to reply in the same chat/topic.
type DynamicMessageContext struct {
	Message         *telego.Message
	Text            string
	ChatID          int64
	ChatIDStr       string
	LocalKey        string
	SenderID        string
	UserID          string
	IsGroup         bool
	IsForum         bool
	MessageThreadID int
	Send            func(ctx context.Context, msg bus.OutboundMessage) error
	Reply           func(ctx context.Context, text string) error
}

// DynamicMessageHandler lets beta features intercept scoped Telegram messages
// without adding feature-specific branches to the core message handler.
type DynamicMessageHandler interface {
	Name() string
	MatchesMessage(ctx context.Context, channel *Channel, msgCtx DynamicMessageContext) bool
	HandleMessage(ctx context.Context, channel *Channel, msgCtx DynamicMessageContext) bool
}

// ChannelScopedDynamicMessageHandler restricts a dynamic message handler to
// specific Telegram channel instances.
type ChannelScopedDynamicMessageHandler interface {
	DynamicMessageHandler
	EnabledForChannel(channel *Channel) bool
}

// ContextScopedDynamicMessageHandler restricts a dynamic message handler to
// specific chat/topic targets within a Telegram channel instance.
type ContextScopedDynamicMessageHandler interface {
	DynamicMessageHandler
	EnabledForContext(ctx context.Context, channel *Channel, msgCtx DynamicMessageContext) bool
}

var dynamicMessages = struct {
	mu       sync.RWMutex
	handlers map[string]DynamicMessageHandler
}{
	handlers: make(map[string]DynamicMessageHandler),
}

func normalizeDynamicMessageName(name string) string {
	return strings.TrimSpace(strings.ToLower(name))
}

// RegisterDynamicMessageHandler registers a runtime Telegram text/caption handler.
func RegisterDynamicMessageHandler(handler DynamicMessageHandler) {
	if handler == nil {
		return
	}
	name := normalizeDynamicMessageName(handler.Name())
	if name == "" {
		return
	}
	dynamicMessages.mu.Lock()
	defer dynamicMessages.mu.Unlock()
	dynamicMessages.handlers[name] = handler
}

// UnregisterDynamicMessageHandler removes a runtime Telegram message handler.
func UnregisterDynamicMessageHandler(name string) {
	name = normalizeDynamicMessageName(name)
	if name == "" {
		return
	}
	dynamicMessages.mu.Lock()
	defer dynamicMessages.mu.Unlock()
	delete(dynamicMessages.handlers, name)
}

func matchingDynamicMessageHandler(ctx context.Context, channel *Channel, msgCtx DynamicMessageContext) DynamicMessageHandler {
	if channel == nil || msgCtx.Message == nil {
		return nil
	}

	dynamicMessages.mu.RLock()
	if len(dynamicMessages.handlers) == 0 {
		dynamicMessages.mu.RUnlock()
		return nil
	}
	names := make([]string, 0, len(dynamicMessages.handlers))
	for name := range dynamicMessages.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	handlers := make([]DynamicMessageHandler, 0, len(names))
	for _, name := range names {
		handlers = append(handlers, dynamicMessages.handlers[name])
	}
	dynamicMessages.mu.RUnlock()

	for _, handler := range handlers {
		if handler == nil {
			continue
		}
		if scoped, ok := handler.(ChannelScopedDynamicMessageHandler); ok && !scoped.EnabledForChannel(channel) {
			continue
		}
		if scoped, ok := handler.(ContextScopedDynamicMessageHandler); ok && !scoped.EnabledForContext(ctx, channel, msgCtx) {
			continue
		}
		if handler.MatchesMessage(ctx, channel, msgCtx) {
			return handler
		}
	}
	return nil
}
