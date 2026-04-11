package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/sessions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type contextEchoTool struct {
	t *testing.T
}

func (t *contextEchoTool) Name() string        { return "context_echo" }
func (t *contextEchoTool) Description() string { return "echoes injected tool context" }
func (t *contextEchoTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *contextEchoTool) Execute(ctx context.Context, _ map[string]any) *tools.Result {
	t.t.Helper()

	got := map[string]any{
		"tool_agent_key":  tools.ToolAgentKeyFromCtx(ctx),
		"store_agent_key": store.AgentKeyFromContext(ctx),
		"channel":         tools.ToolChannelFromCtx(ctx),
		"chat_id":         tools.ToolChatIDFromCtx(ctx),
		"peer_kind":       tools.ToolPeerKindFromCtx(ctx),
		"local_key":       tools.ToolLocalKeyFromCtx(ctx),
		"session_key":     tools.ToolSessionKeyFromCtx(ctx),
	}
	out, err := json.Marshal(got)
	if err != nil {
		t.t.Fatalf("marshal context echo: %v", err)
	}
	return tools.NewResult(string(out))
}

func TestToolsInvokeHandlerInjectsAgentAndRoutingContext(t *testing.T) {
	setupTestToken(t, "test-token")

	registry := tools.NewRegistry()
	registry.Register(&contextEchoTool{t: t})
	handler := NewToolsInvokeHandler(registry, nil, nil)

	body := `{
		"tool":"context_echo",
		"agentId":"builder-bot",
		"channel":"telegram",
		"chatId":"-100321",
		"peerKind":"group",
		"localKey":"-100321:topic:42"
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tools/invoke", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Result struct {
			Output string `json:"output"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	var echoed map[string]string
	if err := json.Unmarshal([]byte(resp.Result.Output), &echoed); err != nil {
		t.Fatalf("decode echoed context: %v", err)
	}

	wantSession := sessions.BuildGroupTopicSessionKey("builder-bot", "telegram", "-100321", 42)
	if echoed["tool_agent_key"] != "builder-bot" {
		t.Fatalf("tool agent key = %q, want %q", echoed["tool_agent_key"], "builder-bot")
	}
	if echoed["store_agent_key"] != "builder-bot" {
		t.Fatalf("store agent key = %q, want %q", echoed["store_agent_key"], "builder-bot")
	}
	if echoed["channel"] != "telegram" {
		t.Fatalf("channel = %q, want %q", echoed["channel"], "telegram")
	}
	if echoed["chat_id"] != "-100321" {
		t.Fatalf("chat_id = %q, want %q", echoed["chat_id"], "-100321")
	}
	if echoed["peer_kind"] != "group" {
		t.Fatalf("peer_kind = %q, want %q", echoed["peer_kind"], "group")
	}
	if echoed["local_key"] != "-100321:topic:42" {
		t.Fatalf("local_key = %q, want %q", echoed["local_key"], "-100321:topic:42")
	}
	if echoed["session_key"] != wantSession {
		t.Fatalf("session_key = %q, want %q", echoed["session_key"], wantSession)
	}
}
