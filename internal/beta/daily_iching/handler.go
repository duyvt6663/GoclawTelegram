package dailyiching

import (
	"encoding/json"
	"io"
	"net/http"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *DailyIChingFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/beta/daily-iching/configs", httpapi.RequireAuth("", h.handleList))
	mux.HandleFunc("GET /v1/beta/daily-iching/configs/{key}", httpapi.RequireAuth("", h.handleGet))
	mux.HandleFunc("PUT /v1/beta/daily-iching/configs/{key}", httpapi.RequireAuth(permissions.RoleOperator, h.handleUpsert))
	mux.HandleFunc("POST /v1/beta/daily-iching/configs/{key}/run", httpapi.RequireAuth(permissions.RoleOperator, h.handleRun))
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
		mode = "post"
	}

	response := map[string]any{
		"mode":       mode,
		"local_date": localDate,
	}
	switch mode {
	case "post":
		delivery, posted, err := h.feature.postNextLesson(r.Context(), cfg, localDate, triggerKindManual, false)
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
			return
		}
		response["posted"] = posted
		if delivery != nil {
			response["hexagram"] = delivery.Hexagram
		}
	case "next":
		delivery, posted, err := h.feature.postNextLesson(r.Context(), cfg, localDate, triggerKindManual, true)
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
			return
		}
		response["posted"] = posted
		if delivery != nil {
			response["hexagram"] = delivery.Hexagram
		}
	case "deeper":
		delivery, err := h.feature.postDeeperLesson(r.Context(), cfg, localDate, triggerKindManual)
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
			return
		}
		response["posted"] = delivery != nil
		if delivery != nil {
			response["hexagram"] = delivery.Hexagram
		}
	default:
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "mode must be post, next, or deeper")
		return
	}

	status, err := h.feature.statusForConfig(cfg, localDate)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	response["status"] = status
	httpapi.WriteJSON(w, http.StatusOK, response)
}
