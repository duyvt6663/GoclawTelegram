package telegram

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

// DynamicUploadContext carries the uploaded Telegram message plus enough
// routing metadata for beta features to reply in the same chat/topic.
type DynamicUploadContext struct {
	Message         *telego.Message
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

// DynamicUploadHandler lets beta features intercept uploaded Telegram files
// without adding feature-specific branches to the core message handler.
type DynamicUploadHandler interface {
	Name() string
	MatchesMessage(ctx context.Context, channel *Channel, message *telego.Message) bool
	HandleUpload(ctx context.Context, channel *Channel, uploadCtx DynamicUploadContext) bool
}

// ChannelScopedDynamicUploadHandler optionally restricts an upload handler to
// specific Telegram channel instances.
type ChannelScopedDynamicUploadHandler interface {
	DynamicUploadHandler
	EnabledForChannel(channel *Channel) bool
}

var dynamicUploads = struct {
	mu       sync.RWMutex
	handlers map[string]DynamicUploadHandler
}{
	handlers: make(map[string]DynamicUploadHandler),
}

func normalizeDynamicUploadName(name string) string {
	return strings.TrimSpace(strings.ToLower(name))
}

// RegisterDynamicUploadHandler registers a runtime Telegram upload handler.
func RegisterDynamicUploadHandler(handler DynamicUploadHandler) {
	if handler == nil {
		return
	}
	name := normalizeDynamicUploadName(handler.Name())
	if name == "" {
		return
	}
	dynamicUploads.mu.Lock()
	defer dynamicUploads.mu.Unlock()
	dynamicUploads.handlers[name] = handler
}

// UnregisterDynamicUploadHandler removes a runtime Telegram upload handler.
func UnregisterDynamicUploadHandler(name string) {
	name = normalizeDynamicUploadName(name)
	if name == "" {
		return
	}
	dynamicUploads.mu.Lock()
	defer dynamicUploads.mu.Unlock()
	delete(dynamicUploads.handlers, name)
}

func matchingDynamicUploadHandler(ctx context.Context, channel *Channel, message *telego.Message) DynamicUploadHandler {
	if channel == nil || message == nil {
		return nil
	}

	dynamicUploads.mu.RLock()
	if len(dynamicUploads.handlers) == 0 {
		dynamicUploads.mu.RUnlock()
		return nil
	}
	names := make([]string, 0, len(dynamicUploads.handlers))
	for name := range dynamicUploads.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	handlers := make([]DynamicUploadHandler, 0, len(names))
	for _, name := range names {
		handlers = append(handlers, dynamicUploads.handlers[name])
	}
	dynamicUploads.mu.RUnlock()

	for _, handler := range handlers {
		if handler == nil {
			continue
		}
		if scoped, ok := handler.(ChannelScopedDynamicUploadHandler); ok && !scoped.EnabledForChannel(channel) {
			continue
		}
		if handler.MatchesMessage(ctx, channel, message) {
			return handler
		}
	}
	return nil
}
