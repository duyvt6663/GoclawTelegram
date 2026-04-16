package researchreviewercodex

import (
	"encoding/json"
	"net/http"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *ResearchReviewerCodexFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/research-reviewer-codex/status", httpapi.RequireAuth(permissions.RoleViewer, h.handleStatus))
	mux.HandleFunc("POST /v1/beta/research-reviewer-codex/review", httpapi.RequireAuth(permissions.RoleOperator, h.handleReview))
	mux.HandleFunc("GET /v1/beta/research-reviewer-codex/reviews/{id}", httpapi.RequireAuth(permissions.RoleViewer, h.handleGetReview))
}

func (h *handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	payload, err := h.feature.statusSnapshot(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func (h *handler) handleReview(w http.ResponseWriter, r *http.Request) {
	var params ReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}

	payload, err := h.feature.review(r.Context(), store.UserIDFromContext(r.Context()), params)
	if err != nil {
		status, code := httpStatusAndCode(err)
		httpapi.WriteError(w, status, code, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func (h *handler) handleGetReview(w http.ResponseWriter, r *http.Request) {
	reviewID := r.PathValue("id")
	if reviewID == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "review id is required")
		return
	}

	payload, err := h.feature.getStoredReview(r.Context(), reviewID)
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
	case isReviewInputError(err):
		return http.StatusBadRequest, protocol.ErrInvalidRequest
	case isNotFoundError(err):
		return http.StatusNotFound, protocol.ErrNotFound
	default:
		return http.StatusInternalServerError, protocol.ErrInternal
	}
}
