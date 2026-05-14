package pdfurlautoreview

import (
	"encoding/json"
	"net/http"
	"strings"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *PDFURLAutoReviewFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/pdf-url-auto-review/status", httpapi.RequireAuth(permissions.RoleViewer, h.handleStatus))
	mux.HandleFunc("GET /v1/beta/pdf-url-auto-review/fetches/{id}", httpapi.RequireAuth(permissions.RoleViewer, h.handleGetFetch))
	mux.HandleFunc("POST /v1/beta/pdf-url-auto-review/fetch", httpapi.RequireAuth(permissions.RoleOperator, h.handleFetch))
}

func (h *handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	payload, err := h.feature.statusSnapshot(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func (h *handler) handleGetFetch(w http.ResponseWriter, r *http.Request) {
	fetchID := strings.TrimSpace(r.PathValue("id"))
	if fetchID == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "fetch id is required")
		return
	}
	payload, err := h.feature.getFetchDetails(r.Context(), fetchID)
	if err != nil {
		status, code := httpStatusAndCode(err)
		httpapi.WriteError(w, status, code, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func (h *handler) handleFetch(w http.ResponseWriter, r *http.Request) {
	var params FetchReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(params.URL) == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "url is required")
		return
	}

	payload, err := h.feature.FetchAndReview(r.Context(), store.UserIDFromContext(r.Context()), params)
	if err != nil {
		status, code := httpStatusAndCode(err)
		httpapi.WriteError(w, status, code, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func httpStatusAndCode(err error) (int, string) {
	switch {
	case err == nil:
		return http.StatusOK, ""
	case isNotFoundError(err):
		return http.StatusNotFound, protocol.ErrNotFound
	case isInputError(err):
		return http.StatusBadRequest, protocol.ErrInvalidRequest
	default:
		return http.StatusInternalServerError, protocol.ErrInternal
	}
}
