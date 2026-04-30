package sharedeaterylist

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *SharedEateryListFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/shared-eatery-list/eateries", httpapi.RequireAuth(permissions.RoleViewer, h.handleList))
	mux.HandleFunc("POST /v1/beta/shared-eatery-list/eateries", httpapi.RequireAuth(permissions.RoleOperator, h.handleAdd))
	mux.HandleFunc("GET /v1/beta/shared-eatery-list/eateries/{id}", httpapi.RequireAuth(permissions.RoleViewer, h.handleGet))
	mux.HandleFunc("GET /v1/beta/shared-eatery-list/random", httpapi.RequireAuth(permissions.RoleViewer, h.handleRandom))
}

func (h *handler) handleAdd(w http.ResponseWriter, r *http.Request) {
	var params EateryInput
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}

	source := sourceMeta{
		ContributorID:    strings.TrimSpace(store.UserIDFromContext(r.Context())),
		ContributorLabel: strings.TrimSpace(store.UserIDFromContext(r.Context())),
	}
	result, err := h.feature.addEatery(r.Context(), tenantKeyFromCtx(r.Context()), params, source)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	status := http.StatusCreated
	if result.DuplicateDetected {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, result)
}

func (h *handler) handleList(w http.ResponseWriter, r *http.Request) {
	result, err := h.feature.listEateries(r.Context(), tenantKeyFromCtx(r.Context()), filterFromQuery(r))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}

func (h *handler) handleRandom(w http.ResponseWriter, r *http.Request) {
	result, err := h.feature.randomEatery(r.Context(), tenantKeyFromCtx(r.Context()), filterFromQuery(r))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}

func (h *handler) handleGet(w http.ResponseWriter, r *http.Request) {
	entry, err := h.feature.store.getEatery(tenantKeyFromCtx(r.Context()), r.PathValue("id"))
	if errors.Is(err, errEateryNotFound) {
		httpapi.WriteError(w, http.StatusNotFound, protocol.ErrNotFound, err.Error())
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"entry": entry})
}

func filterFromQuery(r *http.Request) EateryFilter {
	query := r.URL.Query()
	limit, _ := strconv.Atoi(query.Get("limit"))
	return EateryFilter{
		District:   query.Get("district"),
		Category:   query.Get("category"),
		PriceRange: query.Get("price_range"),
		Search:     query.Get("search"),
		Limit:      limit,
	}
}
