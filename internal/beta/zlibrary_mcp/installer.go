package zlibrarymcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	envServerPath = "ZLIBRARY_MCP_PATH"
	envSkipSetup  = "ZLIBRARY_MCP_SKIP_SETUP"
)

type runtimeSpec struct {
	Command      string
	Args         []string
	WorkDir      string
	Env          map[string]string
	ServerDir    string
	DownloadRoot string
}

type commandRunner interface {
	Run(ctx context.Context, dir, name string, args []string, env map[string]string) error
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, dir, name string, args []string, env map[string]string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), mapToEnvSlice(env)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, trimCommandOutput(string(out)))
	}
	return nil
}

type serverInstaller struct {
	root         string
	repoURL      string
	setupTimeout time.Duration
	runner       commandRunner
}

func (i *serverInstaller) Ensure(ctx context.Context) (*runtimeSpec, error) {
	if i == nil {
		return nil, fmt.Errorf("installer is unavailable")
	}
	if i.runner == nil {
		i.runner = osCommandRunner{}
	}
	if i.repoURL == "" {
		i.repoURL = upstreamRepoURL
	}
	if i.setupTimeout <= 0 {
		i.setupTimeout = defaultSetupTimeout
	}

	serverDir := strings.TrimSpace(os.Getenv(envServerPath))
	if serverDir == "" {
		serverDir = filepath.Join(i.root, "zlibrary-mcp")
	} else {
		var err error
		serverDir, err = filepath.Abs(serverDir)
		if err != nil {
			return nil, err
		}
	}

	downloadRoot := filepath.Join(i.root, "downloads")
	if err := os.MkdirAll(downloadRoot, 0o700); err != nil {
		return nil, err
	}

	setupCtx, cancel := context.WithTimeout(ctx, i.setupTimeout)
	defer cancel()

	if strings.TrimSpace(os.Getenv(envSkipSetup)) != "1" {
		if err := i.ensureSource(setupCtx, serverDir); err != nil {
			return nil, err
		}
		if err := i.ensureDependencies(setupCtx, serverDir); err != nil {
			return nil, err
		}
	}

	entrypoint := filepath.Join(serverDir, "dist", "index.js")
	if _, err := os.Stat(entrypoint); err != nil {
		return nil, fmt.Errorf("zlibrary MCP entrypoint not ready at %s: %w", entrypoint, err)
	}

	return &runtimeSpec{
		Command:      "node",
		Args:         []string{entrypoint},
		WorkDir:      serverDir,
		Env:          zlibraryRuntimeEnv(),
		ServerDir:    serverDir,
		DownloadRoot: downloadRoot,
	}, nil
}

func (i *serverInstaller) ensureSource(ctx context.Context, serverDir string) error {
	if _, err := os.Stat(filepath.Join(serverDir, "package.json")); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if strings.TrimSpace(os.Getenv(envServerPath)) != "" {
		return fmt.Errorf("%s does not contain package.json: %s", envServerPath, serverDir)
	}
	if err := os.MkdirAll(filepath.Dir(serverDir), 0o700); err != nil {
		return err
	}
	return i.runner.Run(ctx, filepath.Dir(serverDir), "git", []string{"clone", "--depth", "1", i.repoURL, serverDir}, nil)
}

func (i *serverInstaller) ensureDependencies(ctx context.Context, serverDir string) error {
	if _, err := os.Stat(filepath.Join(serverDir, ".venv")); os.IsNotExist(err) {
		if err := i.runner.Run(ctx, serverDir, "bash", []string{"setup-uv.sh"}, nil); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(serverDir, "node_modules")); os.IsNotExist(err) {
		if err := i.runner.Run(ctx, serverDir, "npm", []string{"install"}, nil); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(serverDir, "dist", "index.js")); os.IsNotExist(err) {
		if err := i.runner.Run(ctx, serverDir, "npm", []string{"run", "build"}, nil); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return nil
}

func zlibraryRuntimeEnv() map[string]string {
	keys := []string{
		"ZLIBRARY_EMAIL",
		"ZLIBRARY_PASSWORD",
		"ZLIBRARY_MIRROR",
		"ZLIBRARY_EAPI_DOMAIN",
		"ZLIBRARY_DEBUG",
		"RETRY_MAX_RETRIES",
		"RETRY_INITIAL_DELAY",
		"RETRY_MAX_DELAY",
		"RETRY_FACTOR",
		"CIRCUIT_BREAKER_THRESHOLD",
		"CIRCUIT_BREAKER_TIMEOUT",
		"ANNAS_SECRET_KEY",
		"ANNAS_BASE_URL",
		"LIBGEN_MIRROR",
	}
	env := make(map[string]string)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	return env
}

func trimCommandOutput(out string) string {
	out = strings.TrimSpace(out)
	if len(out) <= 1200 {
		return out
	}
	return out[:1200] + "...[truncated]"
}

func mapToEnvSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}
