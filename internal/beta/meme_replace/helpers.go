package memereplace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	fitModeCover   = "cover"
	fitModeContain = "contain"
)

func normalizeReplaceRequest(request ReplaceRequest) (ReplaceRequest, error) {
	request.ImagePath = strings.TrimSpace(request.ImagePath)
	request.ImageBase64 = strings.TrimSpace(request.ImageBase64)
	request.ImageMIME = strings.TrimSpace(request.ImageMIME)
	request.FitMode = normalizeFitMode(request.FitMode)
	request.Source = strings.TrimSpace(request.Source)
	request.Channel = strings.TrimSpace(request.Channel)
	request.ChatID = strings.TrimSpace(request.ChatID)
	return request, nil
}

func normalizeFitMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case fitModeContain:
		return fitModeContain
	default:
		return fitModeCover
	}
}

func (f *MemeReplaceFeature) saveOutput(ctx context.Context, data []byte) (string, error) {
	root := f.resolvedStorageRoot(ctx)
	dir := filepath.Join(root, "outputs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	path := filepath.Join(dir, "meme-replace-"+newRunID()+".png")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write meme output: %w", err)
	}
	return path, nil
}

func (f *MemeReplaceFeature) resolvedStorageRoot(ctx context.Context) string {
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		tenantID = store.MasterTenantID
	}
	tenantSlug := store.TenantSlugFromContext(ctx)
	if tenantSlug == "" {
		tenantSlug = tenantID.String()
	}

	candidates := make([]string, 0, 3)
	if root := strings.TrimSpace(config.ExpandHome(f.dataDir)); root != "" {
		candidates = append(candidates, filepath.Join(config.TenantDataDir(root, tenantID, tenantSlug), "beta_cache", featureName))
	}
	if root := strings.TrimSpace(f.workspace); root != "" {
		candidates = append(candidates, filepath.Join(config.TenantWorkspace(root, tenantID, tenantSlug), "beta_cache", featureName))
	}
	candidates = append(candidates, filepath.Join(os.TempDir(), "goclaw", "beta_cache", featureName, tenantID.String()))

	for _, candidate := range candidates {
		if storageDirWritable(filepath.Join(candidate, "outputs")) {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

func storageDirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".write-test-"+uuid.NewString())
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

func isPathInside(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

func tenantKey(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return strings.TrimSpace(id.String())
}

func tenantKeyFromCtx(ctx context.Context) string {
	return tenantKey(store.TenantIDFromContext(ctx))
}

func newRunID() string {
	return uuid.NewString()
}

func sourceOrDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func trimForStorage(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func cleanUserFacingError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len([]rune(msg)) > 220 {
		msg = string([]rune(msg)[:220]) + "..."
	}
	return msg
}
