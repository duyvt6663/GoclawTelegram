package telegram

import (
	"context"
	"testing"

	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

type testDynamicMessageHandler struct {
	name           string
	enabledChannel string
	enabledThread  int
	handled        bool
}

func (h *testDynamicMessageHandler) Name() string { return h.name }

func (h *testDynamicMessageHandler) MatchesMessage(_ context.Context, _ *Channel, msgCtx DynamicMessageContext) bool {
	return msgCtx.Text == "match"
}

func (h *testDynamicMessageHandler) HandleMessage(_ context.Context, _ *Channel, _ DynamicMessageContext) bool {
	h.handled = true
	return true
}

func (h *testDynamicMessageHandler) EnabledForChannel(channel *Channel) bool {
	return channel != nil && channel.Name() == h.enabledChannel
}

func (h *testDynamicMessageHandler) EnabledForContext(_ context.Context, _ *Channel, msgCtx DynamicMessageContext) bool {
	return msgCtx.MessageThreadID == h.enabledThread
}

func TestDynamicMessageHandlerScopesByChannelContextAndMatch(t *testing.T) {
	dynamicMessages.mu.Lock()
	dynamicMessages.handlers = make(map[string]DynamicMessageHandler)
	dynamicMessages.mu.Unlock()
	t.Cleanup(func() {
		dynamicMessages.mu.Lock()
		dynamicMessages.handlers = make(map[string]DynamicMessageHandler)
		dynamicMessages.mu.Unlock()
	})

	handler := &testDynamicMessageHandler{
		name:           "scoped",
		enabledChannel: "builder-telegram",
		enabledThread:  42,
	}
	RegisterDynamicMessageHandler(handler)

	builder := &Channel{BaseChannel: channels.NewBaseChannel("builder-telegram", nil, nil)}
	other := &Channel{BaseChannel: channels.NewBaseChannel("other-telegram", nil, nil)}
	msg := &telego.Message{MessageID: 1}

	if got := matchingDynamicMessageHandler(context.Background(), other, DynamicMessageContext{
		Message:         msg,
		Text:            "match",
		MessageThreadID: 42,
	}); got != nil {
		t.Fatal("handler should be hidden from unrelated channel")
	}
	if got := matchingDynamicMessageHandler(context.Background(), builder, DynamicMessageContext{
		Message:         msg,
		Text:            "match",
		MessageThreadID: 7,
	}); got != nil {
		t.Fatal("handler should be hidden from unrelated topic context")
	}
	if got := matchingDynamicMessageHandler(context.Background(), builder, DynamicMessageContext{
		Message:         msg,
		Text:            "no match",
		MessageThreadID: 42,
	}); got != nil {
		t.Fatal("handler should not match unrelated message text")
	}
	if got := matchingDynamicMessageHandler(context.Background(), builder, DynamicMessageContext{
		Message:         msg,
		Text:            "match",
		MessageThreadID: 42,
	}); got != handler {
		t.Fatalf("handler = %v, want registered handler", got)
	}
}
