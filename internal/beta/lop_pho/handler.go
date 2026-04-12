package loppho

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *LopPhoFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/lop-pho/status", httpapi.RequireAuth("", h.handleStatus))
	mux.HandleFunc("POST /v1/beta/lop-pho/polls", httpapi.RequireAuth(permissions.RoleOperator, h.handleOpenVote))
}

func (h *handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	filter := PollFilter{
		Channel:    strings.TrimSpace(r.URL.Query().Get("channel")),
		ChatID:     strings.TrimSpace(r.URL.Query().Get("chat_id")),
		ActiveOnly: true,
	}
	if rawThreadID := strings.TrimSpace(r.URL.Query().Get("thread_id")); rawThreadID != "" {
		threadID, err := strconv.Atoi(rawThreadID)
		if err != nil {
			httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid thread_id")
			return
		}
		filter.ThreadID = normalizeThreadID(threadID)
		filter.HasThread = true
	}
	if rawActiveOnly := strings.TrimSpace(r.URL.Query().Get("active_only")); rawActiveOnly != "" {
		activeOnly, err := strconv.ParseBool(rawActiveOnly)
		if err != nil {
			httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid active_only")
			return
		}
		filter.ActiveOnly = activeOnly
	}
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil {
			httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid limit")
			return
		}
		filter.Limit = limit
	}

	status, err := h.feature.statusSnapshot(tenantKeyFromCtx(r.Context()), filter)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, status)
}

func (h *handler) handleOpenVote(w http.ResponseWriter, r *http.Request) {
	var params struct {
		Channel  string `json:"channel"`
		ChatID   string `json:"chat_id"`
		ThreadID int    `json:"thread_id"`
		Target   string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(params.Channel) == "" || strings.TrimSpace(params.ChatID) == "" || strings.TrimSpace(params.Target) == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "channel, chat_id, and target are required")
		return
	}

	actorID := strings.TrimSpace(store.UserIDFromContext(r.Context()))
	if actorID == "" {
		actorID = "http-operator"
	}
	result, err := h.feature.openVotePoll(r.Context(), openVoteInput{
		TenantID:  tenantKeyFromCtx(r.Context()),
		Channel:   strings.TrimSpace(params.Channel),
		ChatID:    strings.TrimSpace(params.ChatID),
		ThreadID:  normalizeThreadID(params.ThreadID),
		LocalKey:  composeLocalKey(params.ChatID, params.ThreadID),
		TargetRaw: params.Target,
		StartedBy: telegramIdentity{
			UserID:   actorID,
			SenderID: actorID,
			Label:    actorID,
		},
		Source: voteOpenSourceHTTP,
	})
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}
