package russianroulette

import (
	"encoding/json"
	"io"
	"net/http"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *RussianRouletteFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/russian-roulette/configs", httpapi.RequireAuth("", h.handleList))
	mux.HandleFunc("GET /v1/beta/russian-roulette/configs/{key}", httpapi.RequireAuth("", h.handleGet))
	mux.HandleFunc("PUT /v1/beta/russian-roulette/configs/{key}", httpapi.RequireAuth(permissions.RoleOperator, h.handleUpsert))
	mux.HandleFunc("GET /v1/beta/russian-roulette/configs/{key}/leaderboard", httpapi.RequireAuth("", h.handleLeaderboard))
	mux.HandleFunc("POST /v1/beta/russian-roulette/configs/{key}/join", httpapi.RequireAuth(permissions.RoleViewer, h.handleJoin))
	mux.HandleFunc("POST /v1/beta/russian-roulette/configs/{key}/start", httpapi.RequireAuth(permissions.RoleViewer, h.handleStart))
	mux.HandleFunc("POST /v1/beta/russian-roulette/configs/{key}/pull", httpapi.RequireAuth(permissions.RoleViewer, h.handlePull))
	mux.HandleFunc("POST /v1/beta/russian-roulette/configs/{key}/leave", httpapi.RequireAuth(permissions.RoleViewer, h.handleLeave))
}

func (h *handler) handleList(w http.ResponseWriter, r *http.Request) {
	statuses, err := h.feature.listStatuses(tenantKeyFromCtx(r.Context()), leaderboardDefaultLimit)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"configs": statuses})
}

func (h *handler) handleGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.feature.store.getConfigByKey(tenantKeyFromCtx(r.Context()), r.PathValue("key"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, protocol.ErrNotFound, err.Error())
		return
	}
	status, err := h.feature.statusForConfig(cfg, leaderboardDefaultLimit)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, status)
}

func (h *handler) handleUpsert(w http.ResponseWriter, r *http.Request) {
	var params upsertConfigParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	params.Key = r.PathValue("key")
	cfg, err := h.feature.upsertConfigForTenant(tenantKeyFromCtx(r.Context()), params.toConfig())
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"config": cfg})
}

func (h *handler) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.feature.store.getConfigByKey(tenantKeyFromCtx(r.Context()), r.PathValue("key"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, protocol.ErrNotFound, err.Error())
		return
	}
	stats, err := h.feature.leaderboardForConfig(cfg, leaderboardDefaultLimit)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"config":      cfg,
		"leaderboard": stats,
	})
}

func (h *handler) handleJoin(w http.ResponseWriter, r *http.Request) {
	h.handleAction(w, r, func(cfg *RouletteConfig, actor playerIdentity, params actionParams) (RouletteActionResponse, error) {
		return h.feature.joinRound(cfg, actor)
	})
}

func (h *handler) handleStart(w http.ResponseWriter, r *http.Request) {
	h.handleAction(w, r, func(cfg *RouletteConfig, actor playerIdentity, params actionParams) (RouletteActionResponse, error) {
		return h.feature.startRound(cfg, actor, params.ChamberSize)
	})
}

func (h *handler) handlePull(w http.ResponseWriter, r *http.Request) {
	h.handleAction(w, r, func(cfg *RouletteConfig, actor playerIdentity, params actionParams) (RouletteActionResponse, error) {
		return h.feature.pullTrigger(cfg, actor)
	})
}

func (h *handler) handleLeave(w http.ResponseWriter, r *http.Request) {
	h.handleAction(w, r, func(cfg *RouletteConfig, actor playerIdentity, params actionParams) (RouletteActionResponse, error) {
		return h.feature.leaveRound(cfg, actor)
	})
}

func (h *handler) handleAction(
	w http.ResponseWriter,
	r *http.Request,
	fn func(cfg *RouletteConfig, actor playerIdentity, params actionParams) (RouletteActionResponse, error),
) {
	cfg, err := h.feature.store.getConfigByKey(tenantKeyFromCtx(r.Context()), r.PathValue("key"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, protocol.ErrNotFound, err.Error())
		return
	}

	var params actionParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil && err != io.EOF {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	actor := params.identity(store.UserIDFromContext(r.Context()))
	if actor.ID == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "user_id is required")
		return
	}

	resp, err := fn(cfg, actor, params)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	if err := h.feature.announceAction(r.Context(), &resp); err != nil {
		resp.Warning = err.Error()
	}
	httpapi.WriteJSON(w, http.StatusOK, resp)
}
