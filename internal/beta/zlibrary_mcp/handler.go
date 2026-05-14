package zlibrarymcp

import (
	"encoding/json"
	"net/http"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *ZLibraryMCPFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/zlibrary-mcp/public-key", httpapi.RequireAuth(permissions.RoleViewer, h.handlePublicKey))
	mux.HandleFunc("GET /v1/beta/zlibrary-mcp/status", httpapi.RequireAuth(permissions.RoleViewer, h.handleStatus))
	mux.HandleFunc("POST /v1/beta/zlibrary-mcp/search", httpapi.RequireAuth(permissions.RoleViewer, h.handleSearch))
	mux.HandleFunc("POST /v1/beta/zlibrary-mcp/download", httpapi.RequireAuth(permissions.RoleOperator, h.handleDownload))
}

func (h *handler) handlePublicKey(w http.ResponseWriter, _ *http.Request) {
	payload, err := h.feature.publicKey()
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func (h *handler) handleStatus(w http.ResponseWriter, _ *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, h.feature.status())
}

func (h *handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	var params SearchBooksRequest
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	payload, err := h.feature.searchBooks(r.Context(), tenantKeyFromCtx(r.Context()), params)
	if err != nil {
		writeFeatureError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func (h *handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	params, err := decodeDownloadRequest(raw)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	payload, err := h.feature.downloadBook(r.Context(), tenantKeyFromCtx(r.Context()), params)
	if err != nil {
		writeFeatureError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func writeFeatureError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := protocol.ErrInternal
	if isInputError(err) {
		status = http.StatusBadRequest
		code = protocol.ErrInvalidRequest
	}
	httpapi.WriteError(w, status, code, err.Error())
}
