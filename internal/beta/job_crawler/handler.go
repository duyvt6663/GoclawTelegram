package jobcrawler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *JobCrawlerFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/job-crawler/configs", httpapi.RequireAuth("", h.handleList))
	mux.HandleFunc("GET /v1/beta/job-crawler/configs/{key}", httpapi.RequireAuth("", h.handleGet))
	mux.HandleFunc("PUT /v1/beta/job-crawler/configs/{key}", httpapi.RequireAuth(permissions.RoleOperator, h.handleUpsert))
	mux.HandleFunc("POST /v1/beta/job-crawler/configs/{key}/run", httpapi.RequireAuth(permissions.RoleOperator, h.handleRun))
	mux.HandleFunc("POST /v1/beta/job-crawler/configs/{key}/run-dynamic", httpapi.RequireAuth(permissions.RoleOperator, h.handleRunDynamic))
}

func (h *handler) handleList(w http.ResponseWriter, r *http.Request) {
	statuses, err := h.feature.listStatuses(tenantKeyFromCtx(r.Context()))
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
	status, err := h.feature.statusForConfig(cfg)
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

func (h *handler) handleRun(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.feature.store.getConfigByKey(tenantKeyFromCtx(r.Context()), r.PathValue("key"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, protocol.ErrNotFound, err.Error())
		return
	}

	var params struct{}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil && err != io.EOF {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}

	result, err := h.feature.runCrawler(r.Context(), cfg, triggerKindManual)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}

func (h *handler) handleRunDynamic(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.feature.store.getConfigByKey(tenantKeyFromCtx(r.Context()), r.PathValue("key"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, protocol.ErrNotFound, err.Error())
		return
	}

	var params struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil && err != io.EOF {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	params.Query = strings.TrimSpace(params.Query)
	if params.Query == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "query is required")
		return
	}

	result, err := h.feature.runDynamicCrawler(r.Context(), cfg, params.Query, params.Limit)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}
