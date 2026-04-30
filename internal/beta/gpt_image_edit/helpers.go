package gptimageedit

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

var getenv = os.Getenv

func normalizeEditRequest(request EditRequest) (EditRequest, error) {
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.Prompt == "" {
		return request, fmt.Errorf("prompt is required")
	}
	if len([]rune(request.Prompt)) > maxPromptRunes {
		return request, fmt.Errorf("prompt is too long (%d rune max)", maxPromptRunes)
	}

	request.Operation = normalizeOperation(request.Operation)
	request.OutputFormat = normalizeOutputFormat(request.OutputFormat)
	request.ImagePath = strings.TrimSpace(request.ImagePath)
	request.ImageBase64 = strings.TrimSpace(request.ImageBase64)
	request.ImageMIME = strings.TrimSpace(request.ImageMIME)
	request.Size = strings.TrimSpace(request.Size)
	request.Quality = normalizeQuality(request.Quality)
	request.Source = strings.TrimSpace(request.Source)
	request.Channel = strings.TrimSpace(request.Channel)
	request.ChatID = strings.TrimSpace(request.ChatID)
	return request, nil
}

func normalizeOperation(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "_")
	switch value {
	case "remove_object", "object_removal", "replace_object", "inpaint", "inpainting", "style_transfer", "background_change", "text_edit", "text_edits", "upscale", "upscaling":
		if value == "object_removal" {
			return "remove_object"
		}
		if value == "inpainting" {
			return "inpaint"
		}
		if value == "text_edits" {
			return "text_edit"
		}
		if value == "upscaling" {
			return "upscale"
		}
		return value
	default:
		return "auto"
	}
}

func normalizeOutputFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpg", "jpeg":
		return "jpeg"
	case "webp":
		return "webp"
	default:
		return defaultOutputFormat
	}
}

func normalizeQuality(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func (f *GPTImageEditFeature) resolveImageInput(ctx context.Context, request EditRequest) (*imageInput, error) {
	if request.ImagePath != "" {
		return f.loadImageFromPath(ctx, request.ImagePath, request.ImageMIME, request.Source)
	}
	if request.ImageBase64 != "" {
		data, err := decodeBase64Image(request.ImageBase64)
		if err != nil {
			return nil, err
		}
		return validateImageData(data, request.ImageMIME, "input."+extensionForMIME(request.ImageMIME), sourceOrDefault(request.Source, "base64"))
	}
	if paths := tools.RunMediaPathsFromCtx(ctx); len(paths) > 0 {
		for _, path := range paths {
			input, err := f.loadImageFromPath(ctx, path, "", sourceOrDefault(request.Source, "chat_attachment"))
			if err == nil {
				return input, nil
			}
		}
	}
	if images := tools.MediaImagesFromCtx(ctx); len(images) > 0 {
		data, err := decodeBase64Image(images[len(images)-1].Data)
		if err != nil {
			return nil, err
		}
		return validateImageData(data, images[len(images)-1].MimeType, "chat-image."+extensionForMIME(images[len(images)-1].MimeType), sourceOrDefault(request.Source, "chat_attachment"))
	}
	return nil, fmt.Errorf("no editable image found; attach a png, jpg, or webp image or pass image_path/image_base64")
}

func (f *GPTImageEditFeature) loadImageFromPath(ctx context.Context, rawPath, mimeHint, source string) (*imageInput, error) {
	resolved, err := resolveAllowedImagePath(ctx, rawPath, f.workspace)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat image: %w", err)
	}
	if info.Size() > maxImageBytes {
		return nil, fmt.Errorf("image file too large (%d bytes, max %d)", info.Size(), maxImageBytes)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	input, err := validateImageData(data, mimeHint, filepath.Base(resolved), sourceOrDefault(source, resolved))
	if err != nil {
		return nil, err
	}
	input.FileName = filepath.Base(resolved)
	input.Source = sourceOrDefault(source, resolved)
	return input, nil
}

func resolveAllowedImagePath(ctx context.Context, rawPath, fallbackWorkspace string) (string, error) {
	rawPath = strings.TrimSpace(strings.TrimPrefix(rawPath, "MEDIA:"))
	if rawPath == "" {
		return "", fmt.Errorf("image_path is required")
	}

	workspace := tools.ToolWorkspaceFromCtx(ctx)
	if workspace == "" {
		workspace = fallbackWorkspace
	}
	if !filepath.IsAbs(rawPath) {
		if workspace == "" {
			return "", fmt.Errorf("relative image_path requires a workspace")
		}
		rawPath = filepath.Join(workspace, rawPath)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(rawPath))
	if err != nil {
		resolved = filepath.Clean(rawPath)
	}
	if workspace == "" {
		return resolved, nil
	}

	allowedRoots := []string{workspace, tools.ToolTeamWorkspaceFromCtx(ctx), os.TempDir()}
	for _, path := range tools.RunMediaPathsFromCtx(ctx) {
		if strings.TrimSpace(path) != "" {
			allowedRoots = append(allowedRoots, filepath.Dir(path))
		}
	}
	for _, root := range allowedRoots {
		if root == "" {
			continue
		}
		if resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root)); err == nil {
			root = resolvedRoot
		}
		if isPathInside(resolved, root) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("image_path must be inside the workspace, team workspace, current media attachments, or temp directory")
}

func validateImageData(data []byte, mimeHint, fileName, source string) (*imageInput, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("image is empty")
	}
	if len(data) > maxImageBytes {
		return nil, fmt.Errorf("image file too large (%d bytes, max %d)", len(data), maxImageBytes)
	}
	mimeType := normalizeImageMIME(mimeHint, fileName, data)
	if !isAllowedImageMIME(mimeType) {
		return nil, fmt.Errorf("unsupported image format %q; supported formats: png, jpg, webp", mimeType)
	}
	return &imageInput{
		Data:     data,
		MIME:     mimeType,
		FileName: ensureImageFileName(fileName, mimeType),
		Source:   sourceOrDefault(source, fileName),
		Size:     int64(len(data)),
	}, nil
}

func normalizeImageMIME(mimeHint, fileName string, data []byte) string {
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(mimeHint, ";")[0]))
	if mimeType == "image/jpg" {
		mimeType = "image/jpeg"
	}
	if isAllowedImageMIME(mimeType) {
		return mimeType
	}
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	}
	if len(data) > 0 {
		detected := strings.ToLower(http.DetectContentType(data))
		if detected == "image/jpg" {
			detected = "image/jpeg"
		}
		if isAllowedImageMIME(detected) {
			return detected
		}
	}
	return mimeType
}

func isAllowedImageMIME(mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png", "image/jpeg", "image/webp":
		return true
	default:
		return false
	}
}

func decodeBase64Image(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if idx := strings.Index(value, ","); strings.HasPrefix(value, "data:") && idx >= 0 {
		value = value[idx+1:]
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		if data, rawErr := base64.RawStdEncoding.DecodeString(value); rawErr == nil {
			return data, nil
		}
		return nil, fmt.Errorf("invalid base64 image data: %w", err)
	}
	return data, nil
}

func encodeBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func outputMime(format string) string {
	switch normalizeOutputFormat(format) {
	case "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

func outputExtension(format string) string {
	switch normalizeOutputFormat(format) {
	case "jpeg":
		return "jpg"
	case "webp":
		return "webp"
	default:
		return "png"
	}
}

func extensionForMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0])) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	default:
		return "png"
	}
}

func ensureImageFileName(fileName, mimeType string) string {
	fileName = strings.TrimSpace(filepath.Base(fileName))
	if fileName == "" || fileName == "." || fileName == string(filepath.Separator) {
		return "input." + extensionForMIME(mimeType)
	}
	if filepath.Ext(fileName) == "" {
		return fileName + "." + extensionForMIME(mimeType)
	}
	return fileName
}

func sourceOrDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func tenantKeyFromCtx(ctx context.Context) string {
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		tenantID = store.MasterTenantID
	}
	return tenantID.String()
}

func storageDirWritable(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probePath := filepath.Join(dir, ".write-probe-"+uuid.NewString())
	if err := os.WriteFile(probePath, []byte("ok"), 0o600); err != nil {
		return false
	}
	_ = os.Remove(probePath)
	return true
}

func isPathInside(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func trimForStorage(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len([]rune(value)) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}

func truncateBytes(data []byte, max int) string {
	if len(data) <= max {
		return string(data)
	}
	return string(data[:max]) + "..."
}
