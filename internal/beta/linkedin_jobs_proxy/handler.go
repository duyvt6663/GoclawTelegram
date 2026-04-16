package linkedinjobsproxy

import (
	"encoding/json"
	"net/http"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *LinkedInJobsProxyFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/beta/linkedin-jobs-proxy/search", httpapi.RequireAuth(permissions.RoleViewer, h.handleSearch))
}

func (h *handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	var params SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}

	payload, err := h.feature.service.Search(r.Context(), tenantKeyFromCtx(r.Context()), params)
	if err != nil {
		status := http.StatusInternalServerError
		code := protocol.ErrInternal
		if isSearchInputError(err) {
			status = http.StatusBadRequest
			code = protocol.ErrInvalidRequest
		}
		httpapi.WriteError(w, status, code, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}
