package eaterychat

import (
	"net/http"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
)

type handler struct {
	feature *EateryChatFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/eatery-chat/status", httpapi.RequireAuth(permissions.RoleViewer, h.handleStatus))
}

func (h *handler) handleStatus(w http.ResponseWriter, _ *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"feature": featureName,
		"status":  "stub",
	})
}
