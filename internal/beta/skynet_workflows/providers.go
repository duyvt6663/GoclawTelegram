package skynetworkflows

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
)

func (f *SkynetWorkflowsFeature) ensureWorkflowProviders(ctx context.Context) error {
	if f.providerStore == nil {
		return nil
	}
	claudePath := f.resolveCLIPath(ctx, storepkg.ProviderClaudeCLI, "claude")
	if claudePath == "" {
		slog.Warn("beta skynet_workflows: Claude CLI provider not created; claude binary not found")
		return nil
	}

	targetRepo := f.resolveTargetRepo(ctx)
	if targetRepo == "" {
		targetRepo = safeWorkspace(f.workspace, "skynet-target")
	}
	sessionDir := filepath.Join(targetRepo, ".goclaw-skynet", "claude-sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return err
	}

	addDirs := make([]string, 0, 1)
	if homeDir, err := os.UserHomeDir(); err == nil {
		if claudeDir := filepath.Join(homeDir, ".claude"); dirExists(claudeDir) {
			addDirs = append(addDirs, claudeDir)
		}
	}

	return f.providerStore.CreateProvider(ctx, &storepkg.LLMProviderData{
		Name:         providerNameSkynetClaudeCLI,
		DisplayName:  "Skynet Claude CLI",
		ProviderType: storepkg.ProviderClaudeCLI,
		APIBase:      claudePath,
		Enabled:      true,
		Settings: mustJSON(map[string]any{
			"model":          modelClaudeOpus47,
			"effort":         reasoningHigh,
			"base_work_dir":  sessionDir,
			"workspace_root": targetRepo,
			"perm_mode":      "bypassPermissions",
			"add_dirs":       addDirs,
			"use_mcp_bridge": true,
		}),
	})
}

func (f *SkynetWorkflowsFeature) resolveCLIPath(ctx context.Context, providerKind, fallbackBinary string) string {
	if providerKind == storepkg.ProviderClaudeCLI && f.cfg != nil {
		if path := executablePath(f.cfg.Providers.ClaudeCLI.CLIPath); path != "" {
			return path
		}
	}
	if f.providerStore != nil {
		if providers, err := f.providerStore.ListProviders(ctx); err == nil {
			for _, provider := range providers {
				if !provider.Enabled || provider.ProviderType != providerKind {
					continue
				}
				if path := executablePath(provider.APIBase); path != "" {
					return path
				}
			}
		}
	}
	return executablePath(fallbackBinary)
}

func executablePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
		return ""
	}
	if resolved, err := exec.LookPath(path); err == nil {
		return resolved
	}
	return ""
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func claudeProviderMCPBridgeDisabled(settings json.RawMessage) bool {
	if len(settings) == 0 {
		return false
	}
	var parsed struct {
		UseMCPBridge *bool `json:"use_mcp_bridge"`
	}
	if err := json.Unmarshal(settings, &parsed); err != nil {
		return false
	}
	return parsed.UseMCPBridge != nil && !*parsed.UseMCPBridge
}
