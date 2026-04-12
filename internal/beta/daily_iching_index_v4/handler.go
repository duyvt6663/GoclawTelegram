package dailyichingindexv4

import (
	"encoding/json"
	"io"
	"net/http"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *DailyIChingIndexV4Feature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/daily-iching-index-v4/status", httpapi.RequireAuth("", h.handleStatus))
	mux.HandleFunc("POST /v1/beta/daily-iching-index-v4/compare", httpapi.RequireAuth(permissions.RoleOperator, h.handleCompare))
}

func (h *handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	payload, err := h.feature.statusPayload(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func (h *handler) handleCompare(w http.ResponseWriter, r *http.Request) {
	var params struct {
		Queries []string `json:"queries"`
		Rebuild bool     `json:"rebuild"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil && err != io.EOF {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}

	payload, err := h.feature.comparePayload(r.Context(), params.Queries, params.Rebuild)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}
