package topictoolrouting

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *TopicToolRoutingFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/topic-tool-routing/configs", httpapi.RequireAuth("", h.handleList))
	mux.HandleFunc("GET /v1/beta/topic-tool-routing/configs/{key}", httpapi.RequireAuth("", h.handleGet))
	mux.HandleFunc("PUT /v1/beta/topic-tool-routing/configs/{key}", httpapi.RequireAuth(permissions.RoleOperator, h.handleUpsert))
	mux.HandleFunc("GET /v1/beta/topic-tool-routing/resolve", httpapi.RequireAuth("", h.handleResolve))
}

func (h *handler) handleList(w http.ResponseWriter, r *http.Request) {
	configs, err := h.feature.store.listConfigs(tenantKeyFromCtx(r.Context()))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"configs":             configs,
		"registered_features": registeredFeaturesSnapshot(),
	})
}

func (h *handler) handleGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.feature.store.getConfigByKey(tenantKeyFromCtx(r.Context()), r.PathValue("key"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, protocol.ErrNotFound, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"config":              cfg,
		"registered_features": registeredFeaturesSnapshot(),
	})
}

func (h *handler) handleUpsert(w http.ResponseWriter, r *http.Request) {
	var params upsertParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	params.Key = r.PathValue("key")

	cfg, err := h.feature.upsertConfigForTenant(tenantKeyFromCtx(r.Context()), upsertParamsToConfig(params))
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"config": cfg})
}

func (h *handler) handleResolve(w http.ResponseWriter, r *http.Request) {
	threadID := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("thread_id")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid thread_id")
			return
		}
		threadID = value
	}

	snapshot, err := h.feature.resolveSnapshot(r.Context(), topicrouting.TopicToolScope{
		Channel:  strings.TrimSpace(r.URL.Query().Get("channel")),
		ChatID:   strings.TrimSpace(r.URL.Query().Get("chat_id")),
		ThreadID: threadID,
		LocalKey: strings.TrimSpace(r.URL.Query().Get("local_key")),
	})
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, snapshot)
}
