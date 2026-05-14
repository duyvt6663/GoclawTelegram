package zlibrarymcp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

func resolveStorageRoot(dataDir, workspace string) (string, error) {
	candidates := make([]string, 0, 3)
	if dataDir != "" {
		candidates = append(candidates, filepath.Join(dataDir, featureName))
	}
	if workspace != "" {
		candidates = append(candidates, filepath.Join(workspace, "beta_cache", featureName))
	}
	candidates = append(candidates, filepath.Join(os.TempDir(), "goclaw", "beta_cache", featureName))

	var lastErr error
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if err := probeWritableDir(candidate); err != nil {
			lastErr = err
			continue
		}
		return candidate, nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no storage candidates available")
}

func probeWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	testPath := filepath.Join(dir, ".write-test-"+uuid.NewString())
	if err := os.WriteFile(testPath, []byte("ok"), 0o600); err != nil {
		return err
	}
	return os.Remove(testPath)
}
