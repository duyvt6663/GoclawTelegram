package memereplace

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type imageInput struct {
	Image  image.Image
	MIME   string
	Source string
	Size   int64
}

func (f *MemeReplaceFeature) resolveImageInput(ctx context.Context, request ReplaceRequest) (*imageInput, error) {
	if strings.TrimSpace(request.ImagePath) != "" {
		return f.loadImageFromPath(ctx, request.ImagePath, request.ImageMIME, sourceOrDefault(request.Source, "image_path"))
	}
	if strings.TrimSpace(request.ImageBase64) != "" {
		data, err := decodeBase64Image(request.ImageBase64)
		if err != nil {
			return nil, err
		}
		return validateImageData(data, request.ImageMIME, "input", sourceOrDefault(request.Source, "base64"))
	}
	if paths := tools.RunMediaPathsFromCtx(ctx); len(paths) > 0 {
		for i := len(paths) - 1; i >= 0; i-- {
			input, err := f.loadImageFromPath(ctx, paths[i], "", sourceOrDefault(request.Source, "chat_attachment"))
			if err == nil {
				return input, nil
			}
		}
	}
	if images := tools.MediaImagesFromCtx(ctx); len(images) > 0 {
		for i := len(images) - 1; i >= 0; i-- {
			data, err := decodeBase64Image(images[i].Data)
			if err != nil {
				continue
			}
			input, err := validateImageData(data, images[i].MimeType, "chat-image", sourceOrDefault(request.Source, "chat_attachment"))
			if err == nil {
				return input, nil
			}
		}
	}
	return nil, fmt.Errorf("no image provided; attach an image or pass image_path/image_base64")
}

func (f *MemeReplaceFeature) loadImageFromPath(ctx context.Context, rawPath, mimeHint, source string) (*imageInput, error) {
	resolved, err := resolveAllowedImagePath(ctx, rawPath, f.workspace)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat image: %w", err)
	}
	if info.Size() > maxInputImageSize {
		return nil, fmt.Errorf("image file too large (%d bytes, max %d)", info.Size(), maxInputImageSize)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	return validateImageData(data, mimeHint, filepath.Base(resolved), sourceOrDefault(source, resolved))
}

func decodeBase64Image(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("image_base64 is required")
	}
	if idx := strings.Index(value, ","); idx >= 0 && strings.Contains(value[:idx], "base64") {
		value = value[idx+1:]
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("invalid image_base64")
	}
	return data, nil
}

func validateImageData(data []byte, mimeHint, fileName, source string) (*imageInput, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("image is empty")
	}
	if len(data) > maxInputImageSize {
		return nil, fmt.Errorf("image file too large (%d bytes, max %d)", len(data), maxInputImageSize)
	}
	mimeType := normalizeImageMIME(mimeHint, fileName, data)
	if !isSupportedImageMIME(mimeType) {
		return nil, fmt.Errorf("unsupported image format %q; supported formats: png, jpg, webp, gif", mimeType)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, fmt.Errorf("image dimensions are invalid")
	}
	return &imageInput{
		Image:  img,
		MIME:   mimeType,
		Source: sourceOrDefault(source, "image"),
		Size:   int64(len(data)),
	}, nil
}

func normalizeImageMIME(mimeHint, fileName string, data []byte) string {
	mimeHint = strings.ToLower(strings.TrimSpace(strings.Split(mimeHint, ";")[0]))
	switch mimeHint {
	case "image/jpg":
		return "image/jpeg"
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return mimeHint
	}
	detected := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	switch detected {
	case "image/jpg":
		return "image/jpeg"
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return detected
	}
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return detected
	}
}

func isSupportedImageMIME(mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
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
