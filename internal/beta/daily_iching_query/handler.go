package dailyichingquery

import (
	"encoding/json"
	"net/http"
	"strings"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *DailyIChingQueryFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/beta/daily-iching/query", httpapi.RequireAuth("", h.handleQuery))
}

func (h *handler) handleQuery(w http.ResponseWriter, r *http.Request) {
	var params struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(params.Question) == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "question is required")
		return
	}

	payload, err := h.feature.answerQuestion(r.Context(), tenantKeyFromCtx(r.Context()), params.Question)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}
