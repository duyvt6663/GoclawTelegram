package lopphopolldedupe

import (
	"net/http"
	"strconv"
	"strings"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *LopPhoPollDedupeFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/lop-pho-poll-dedupe/status", httpapi.RequireAuth(permissions.RoleOperator, h.handleStatus))
}

func (h *handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	filter := ClaimFilter{
		ChatID: strings.TrimSpace(r.URL.Query().Get("chat_id")),
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
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil {
			httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid limit")
			return
		}
		filter.Limit = limit
	}

	status, err := h.feature.statusSnapshot(r.Context(), tenantKeyFromCtx(r.Context()), filter)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, status)
}
