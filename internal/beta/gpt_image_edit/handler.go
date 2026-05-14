package gptimageedit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return EditRequest{}, fmt.Errorf("invalid multipart form: %w", err)
	}
	images, mimes, err := readMultipartImageFields(r)
	if err != nil {
		return EditRequest{}, err
	}

	request := EditRequest{
		Prompt:       r.FormValue("prompt"),
		Operation:    r.FormValue("operation"),
		OutputFormat: r.FormValue("output_format"),
		Size:         r.FormValue("size"),
		Quality:      r.FormValue("quality"),
		Source:       "http_upload",
	}
	if len(images) == 1 {
		request.ImageBase64 = base64.StdEncoding.EncodeToString(images[0])
		request.ImageMIME = mimes[0]
	} else {
		request.ImageBase64s = make([]string, 0, len(images))
		request.ImageMIMEs = mimes
		for _, image := range images {
			request.ImageBase64s = append(request.ImageBase64s, base64.StdEncoding.EncodeToString(image))
		}
	}
	return request, nil
}

func readMultipartImageFields(r *http.Request) ([][]byte, []string, error) {
	if r.MultipartForm == nil {
		return nil, nil, fmt.Errorf("multipart field image is required")
	}
	headers := append([]*multipart.FileHeader{}, r.MultipartForm.File["image"]...)
	headers = append(headers, r.MultipartForm.File["image[]"]...)
	if len(headers) == 0 {
		return nil, nil, fmt.Errorf("multipart field image is required")
	}
	if len(headers) > maxInputImages {
		return nil, nil, fmt.Errorf("too many input images (%d max)", maxInputImages)
	}

	images := make([][]byte, 0, len(headers))
	mimes := make([]string, 0, len(headers))
	for _, header := range headers {
		file, err := header.Open()
		if err != nil {
			return nil, nil, fmt.Errorf("open image: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, nil, fmt.Errorf("read image: %w", readErr)
		}
		if closeErr != nil {
			return nil, nil, fmt.Errorf("close image: %w", closeErr)
		}
		if len(data) > maxImageBytes {
			return nil, nil, fmt.Errorf("image file too large (%d bytes, max %d)", len(data), maxImageBytes)
		}
		mimeType := header.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = http.DetectContentType(data)
		}
		images = append(images, data)
		mimes = append(mimes, mimeType)
	}
	return images, mimes, nil
}
