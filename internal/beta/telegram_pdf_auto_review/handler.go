package telegrampdfautoreview

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
	feature *TelegramPDFAutoReviewFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/telegram-pdf-auto-review/status", httpapi.RequireAuth(permissions.RoleViewer, h.handleStatus))
	mux.HandleFunc("GET /v1/beta/telegram-pdf-auto-review/uploads/{id}", httpapi.RequireAuth(permissions.RoleViewer, h.handleGetUpload))
	mux.HandleFunc("POST /v1/beta/telegram-pdf-auto-review/reprocess", httpapi.RequireAuth(permissions.RoleOperator, h.handleReprocess))
}

func (h *handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	payload, err := h.feature.statusSnapshot(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func (h *handler) handleGetUpload(w http.ResponseWriter, r *http.Request) {
	uploadID := strings.TrimSpace(r.PathValue("id"))
	if uploadID == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "upload id is required")
		return
	}

	payload, err := h.feature.getUploadDetails(r.Context(), uploadID)
	if err != nil {
		status, code := httpStatusAndCode(err)
		httpapi.WriteError(w, status, code, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func (h *handler) handleReprocess(w http.ResponseWriter, r *http.Request) {
	var params ReprocessRequest
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(params.UploadID) == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "upload_id is required")
		return
	}

	payload, err := h.feature.reprocessUpload(r.Context(), store.UserIDFromContext(r.Context()), params)
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
