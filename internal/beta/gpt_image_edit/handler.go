package gptimageedit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type handler struct {
	feature *GPTImageEditFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/beta/gpt-image-edit/edit", httpapi.RequireAuth(permissions.RoleViewer, h.handleEdit))
	mux.HandleFunc("GET /v1/beta/gpt-image-edit/runs", httpapi.RequireAuth(permissions.RoleViewer, h.handleRuns))
}

func (h *handler) handleEdit(w http.ResponseWriter, r *http.Request) {
	params, err := decodeHTTPEditRequest(r)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	payload, err := h.feature.edit(r.Context(), params, true)
	if err != nil {
		status := http.StatusInternalServerError
		code := protocol.ErrInternal
		if isEditInputError(err) {
			status = http.StatusBadRequest
			code = protocol.ErrInvalidRequest
		}
		httpapi.WriteError(w, status, code, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func (h *handler) handleRuns(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}
	runs, err := h.feature.store.listRecentRuns(tenantKeyFromCtx(r.Context()), limit)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, protocol.ErrInternal, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func decodeHTTPEditRequest(r *http.Request) (EditRequest, error) {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "multipart/form-data") {
		var params EditRequest
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			return params, fmt.Errorf("invalid JSON body")
		}
		return params, nil
	}

	if err := r.ParseMultipartForm(maxImageBytes + 2<<20); err != nil {
		return EditRequest{}, fmt.Errorf("invalid multipart form: %w", err)
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		return EditRequest{}, fmt.Errorf("multipart field image is required")
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil {
		return EditRequest{}, fmt.Errorf("read image: %w", err)
	}
	if len(data) > maxImageBytes {
		return EditRequest{}, fmt.Errorf("image file too large (%d bytes, max %d)", len(data), maxImageBytes)
	}
	mimeType := ""
	if header != nil {
		mimeType = header.Header.Get("Content-Type")
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}

	return EditRequest{
		Prompt:       r.FormValue("prompt"),
		Operation:    r.FormValue("operation"),
		ImageBase64:  base64.StdEncoding.EncodeToString(data),
		ImageMIME:    mimeType,
		OutputFormat: r.FormValue("output_format"),
		Size:         r.FormValue("size"),
		Quality:      r.FormValue("quality"),
		Source:       "http_upload",
	}, nil
}
