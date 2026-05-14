package memereplace

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
	feature *MemeReplaceFeature
}

func (h *handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/beta/meme-replace/replace", httpapi.RequireAuth(permissions.RoleViewer, h.handleReplace))
	mux.HandleFunc("GET /v1/beta/meme-replace/runs", httpapi.RequireAuth(permissions.RoleViewer, h.handleRuns))
}

func (h *handler) handleReplace(w http.ResponseWriter, r *http.Request) {
	params, err := decodeHTTPReplaceRequest(r)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	payload, err := h.feature.replace(r.Context(), params, true)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, payload)
}

func (h *handler) handleRuns(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httpapi.WriteError(w, http.StatusBadRequest, protocol.ErrInvalidRequest, "invalid limit")
			return
		}
		if parsed > 0 && parsed <= 100 {
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

func decodeHTTPReplaceRequest(r *http.Request) (ReplaceRequest, error) {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "multipart/form-data") {
		var params ReplaceRequest
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			return params, fmt.Errorf("invalid JSON body")
		}
		params.Source = sourceOrDefault(params.Source, "http_json")
		return params, nil
	}

	if err := r.ParseMultipartForm(maxInputImageSize + 1024); err != nil {
		return ReplaceRequest{}, fmt.Errorf("invalid multipart form: %w", err)
	}
	data, mimeType, err := readMultipartImageField(r)
	if err != nil {
		return ReplaceRequest{}, err
	}
	return ReplaceRequest{
		ImageBase64: base64.StdEncoding.EncodeToString(data),
		ImageMIME:   mimeType,
		FitMode:     r.FormValue("fit_mode"),
		Source:      "http_upload",
	}, nil
}

func readMultipartImageField(r *http.Request) ([]byte, string, error) {
	if r.MultipartForm == nil {
		return nil, "", fmt.Errorf("multipart field image is required")
	}
	header := firstMultipartFileHeader(r.MultipartForm.File["image"])
	if header == nil {
		return nil, "", fmt.Errorf("multipart field image is required")
	}
	file, err := header.Open()
	if err != nil {
		return nil, "", fmt.Errorf("open image: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxInputImageSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, "", fmt.Errorf("read image: %w", readErr)
	}
	if closeErr != nil {
		return nil, "", fmt.Errorf("close image: %w", closeErr)
	}
	if len(data) > maxInputImageSize {
		return nil, "", fmt.Errorf("image file too large (%d bytes, max %d)", len(data), maxInputImageSize)
	}
	mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	return data, mimeType, nil
}

func firstMultipartFileHeader(headers []*multipart.FileHeader) *multipart.FileHeader {
	if len(headers) == 0 {
		return nil
	}
	return headers[0]
}
