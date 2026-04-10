package dailydiscipline

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
	feature *DailyDisciplineFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/daily-discipline/configs", httpapi.RequireAuth("", h.handleList))
	mux.HandleFunc("GET /v1/beta/daily-discipline/configs/{key}", httpapi.RequireAuth("", h.handleGet))
	mux.HandleFunc("PUT /v1/beta/daily-discipline/configs/{key}", httpapi.RequireAuth(permissions.RoleOperator, h.handleUpsert))
	mux.HandleFunc("GET /v1/beta/daily-discipline/configs/{key}/responses", httpapi.RequireAuth("", h.handleResponses))
	mux.HandleFunc("POST /v1/beta/daily-discipline/configs/{key}/responses", httpapi.RequireAuth(permissions.RoleOperator, h.handleSubmit))
	mux.HandleFunc("POST /v1/beta/daily-discipline/configs/{key}/run", httpapi.RequireAuth(permissions.RoleOperator, h.handleRun))
}

func (h *handler) handleList(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantKeyFromCtx(r.Context())
	configs, err := h.feature.store.listConfigs(tenantID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}

	statuses := make([]ConfigStatus, 0, len(configs))
	for i := range configs {
		localDate, err := resolveLocalDate(&configs[i], r.URL.Query().Get("date"))
		if err != nil {
			httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
			return
		}
		status, err := h.feature.statusForConfig(&configs[i], localDate)
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
			return
		}
		statuses = append(statuses, status)
	}

	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"configs": statuses})
}

func (h *handler) handleGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.feature.store.getConfigByKey(tenantKeyFromCtx(r.Context()), r.PathValue("key"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, protocol.ErrNotFound, err.Error())
		return
	}
	localDate, err := resolveLocalDate(cfg, r.URL.Query().Get("date"))
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	status, err := h.feature.statusForConfig(cfg, localDate)
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

func (h *handler) handleResponses(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.feature.store.getConfigByKey(tenantKeyFromCtx(r.Context()), r.PathValue("key"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, protocol.ErrNotFound, err.Error())
		return
	}
	localDate, err := resolveLocalDate(cfg, r.URL.Query().Get("date"))
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	responses, err := h.feature.responsesForDate(cfg, localDate)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"config":     cfg,
		"local_date": localDate,
		"responses":  responses,
	})
}

func (h *handler) handleSubmit(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.feature.store.getConfigByKey(tenantKeyFromCtx(r.Context()), r.PathValue("key"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, protocol.ErrNotFound, err.Error())
		return
	}

	var params submitResponseParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}
	params.Key = cfg.Key
	localDate, err := resolveLocalDate(cfg, params.Date)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	identity := params.identity(store.UserIDFromContext(r.Context()))
	if identity.ID == "" {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "user_id is required")
		return
	}
	wake, err := params.parseWake()
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	discipline, err := params.parseDiscipline()
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	activity, err := params.parseActivity()
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	response, err := h.feature.submitDetailedResponse(r.Context(), cfg, localDate, identity, wake, discipline, activity, optionalString(params.Note), "http")
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"response": response})
}

func (h *handler) handleRun(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.feature.store.getConfigByKey(tenantKeyFromCtx(r.Context()), r.PathValue("key"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, protocol.ErrNotFound, err.Error())
		return
	}

	var params struct {
		Mode string `json:"mode"`
		Date string `json:"date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil && err != io.EOF {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid JSON body")
		return
	}

	localDate, err := resolveLocalDate(cfg, params.Date)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	mode := params.Mode
	if mode == "" {
		mode = "survey"
	}
	switch mode {
	case "survey":
		err = h.feature.ensureSurveyPosted(r.Context(), cfg, localDate)
	case "summary":
		err = h.feature.ensureSummaryPosted(r.Context(), cfg, localDate)
	default:
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "mode must be survey or summary")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	status, err := h.feature.statusForConfig(cfg, localDate)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"mode":       mode,
		"local_date": localDate,
		"status":     status,
	})
}
