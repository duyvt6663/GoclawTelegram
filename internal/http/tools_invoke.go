package http

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/sessions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// ToolsInvokeHandler handles POST /v1/tools/invoke (direct tool invocation).
type ToolsInvokeHandler struct {
	registry         *tools.Registry
	agentStore       store.AgentStore       // nil if not configured
	builtinToolStore store.BuiltinToolStore // nil if not configured
}

// NewToolsInvokeHandler creates a handler for the tools invoke endpoint.
func NewToolsInvokeHandler(registry *tools.Registry, agentStore store.AgentStore, builtinToolStore store.BuiltinToolStore) *ToolsInvokeHandler {
	return &ToolsInvokeHandler{
		registry:         registry,
		agentStore:       agentStore,
		builtinToolStore: builtinToolStore,
	}
}

type toolsInvokeRequest struct {
	Tool       string         `json:"tool"`
	Action     string         `json:"action,omitempty"`
	Args       map[string]any `json:"args"`
	SessionKey string         `json:"sessionKey,omitempty"`
	LocalKey   string         `json:"localKey,omitempty"`
	AgentID    string         `json:"agentId,omitempty"`
	DryRun     bool           `json:"dryRun,omitempty"`
	Channel    string         `json:"channel,omitempty"`  // tool context: channel name
	ChatID     string         `json:"chatId,omitempty"`   // tool context: chat ID
	PeerKind   string         `json:"peerKind,omitempty"` // tool context: "direct" or "group"
}

func (h *ToolsInvokeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	locale := extractLocale(r)

	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": i18n.T(locale, i18n.MsgMethodNotAllowed)})
		return
	}

	auth := resolveAuth(r)
	if !auth.Authenticated {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": i18n.T(locale, i18n.MsgUnauthorized)})
		return
	}
	if !permissions.HasMinRole(auth.Role, permissions.RoleOperator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(locale, i18n.MsgPermissionDenied, r.URL.Path)})
		return
	}

	// Inject tenant, role, user, and locale into context for downstream stores/tools.
	r = r.WithContext(enrichContext(r.Context(), r, auth))

	var req toolsInvokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgInvalidJSON)})
		return
	}

	if req.Tool == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(locale, i18n.MsgRequired, "tool")})
		return
	}

	slog.Info("tools invoke request", "tool", req.Tool, "dry_run", req.DryRun)

	if req.DryRun {
		// Just check if tool exists and return its schema
		tool, ok := h.registry.Get(req.Tool)
		if !ok {
			writeToolError(w, http.StatusNotFound, "NOT_FOUND", i18n.T(locale, i18n.MsgNotFound, "tool", req.Tool))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"tool":        req.Tool,
			"description": tool.Description(),
			"parameters":  tool.Parameters(),
			"dryRun":      true,
		})
		return
	}

	// Inject agentID into context for interceptors (bootstrap, memory).
	// Note: userID, tenantID, role, locale already injected by enrichContext above.
	ctx := r.Context()

	agentIDStr := req.AgentID
	if agentIDStr == "" {
		agentIDStr = extractAgentID(r, "")
	}
	if agentIDStr != "" {
		ctx = tools.WithToolAgentKey(ctx, agentIDStr)
		ctx = store.WithAgentKey(ctx, agentIDStr)
		if h.agentStore != nil {
			ag, err := h.agentStore.GetByKey(ctx, agentIDStr)
			if err == nil {
				ctx = store.WithAgentID(ctx, ag.ID)
			}
		}
	}

	// Inject tool context keys (channel, chatID, peerKind) for message routing.
	if req.Channel != "" {
		ctx = tools.WithToolChannel(ctx, req.Channel)
	}
	if req.ChatID != "" {
		ctx = tools.WithToolChatID(ctx, req.ChatID)
	}
	if req.PeerKind != "" {
		ctx = tools.WithToolPeerKind(ctx, req.PeerKind)
	}
	if req.LocalKey != "" {
		ctx = tools.WithToolLocalKey(ctx, req.LocalKey)
	}
	sessionKey := strings.TrimSpace(req.SessionKey)
	if sessionKey == "" {
		sessionKey = deriveToolSessionKey(agentIDStr, req.Channel, req.ChatID, req.PeerKind, req.LocalKey)
	}
	if h.builtinToolStore != nil {
		if def, err := h.builtinToolStore.Get(ctx, req.Tool); err == nil && len(def.Settings) > 0 {
			ctx = tools.WithBuiltinToolSettings(ctx, tools.BuiltinToolSettings{
				req.Tool: def.Settings,
			})
		}
	}

	// Execute the tool
	args := req.Args
	if args == nil {
		args = make(map[string]any)
	}

	// If action is specified, add it to args
	if req.Action != "" {
		args["action"] = req.Action
	}

	execChannel := req.Channel
	if execChannel == "" {
		execChannel = "http"
	}
	execChatID := req.ChatID
	if execChatID == "" {
		execChatID = "api"
	}
	execPeerKind := req.PeerKind
	if execPeerKind == "" {
		execPeerKind = "direct"
	}

	result := h.registry.ExecuteWithContext(ctx, req.Tool, args, execChannel, execChatID, execPeerKind, sessionKey, nil)

	if result.IsError {
		writeToolError(w, http.StatusBadRequest, "TOOL_ERROR", result.ForLLM)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{
			"output":   result.ForLLM,
			"forUser":  result.ForUser,
			"metadata": map[string]any{},
		},
	})
}

func deriveToolSessionKey(agentID, channel, chatID, peerKind, localKey string) string {
	agentID = strings.TrimSpace(agentID)
	channel = strings.TrimSpace(channel)
	chatID = strings.TrimSpace(chatID)
	peerKind = strings.TrimSpace(peerKind)
	localKey = strings.TrimSpace(localKey)
	if agentID == "" || channel == "" || chatID == "" || peerKind == "" {
		return ""
	}

	sessionKey := sessions.BuildScopedSessionKey(agentID, channel, sessions.PeerKind(peerKind), chatID)
	if localKey == "" {
		return sessionKey
	}
	if idx := strings.Index(localKey, ":topic:"); idx > 0 && peerKind == string(sessions.PeerGroup) {
		var topicID int
		fmt.Sscanf(localKey[idx+7:], "%d", &topicID)
		if topicID > 0 {
			return sessions.BuildGroupTopicSessionKey(agentID, channel, chatID, topicID)
		}
	} else if idx := strings.Index(localKey, ":thread:"); idx > 0 && peerKind == string(sessions.PeerDirect) {
		var threadID int
		fmt.Sscanf(localKey[idx+8:], "%d", &threadID)
		if threadID > 0 {
			return sessions.BuildDMThreadSessionKey(agentID, channel, chatID, threadID)
		}
	}
	return sessionKey
}

func writeToolError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
