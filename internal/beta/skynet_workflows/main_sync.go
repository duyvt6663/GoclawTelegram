package skynetworkflows

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	defaultMainSyncBranch  = "main"
	defaultMainSyncPort    = "3000"
	defaultMainSyncSession = "researchcrafters-host-main"
)

var safeMainSyncBranchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

type mainSyncResult struct {
	Status     string `json:"status"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit,omitempty"`
	OldCommit  string `json:"old_commit,omitempty"`
	TargetRepo string `json:"target_repo"`
	DeployRepo string `json:"deploy_repo"`
	Port       string `json:"port,omitempty"`
	Session    string `json:"session,omitempty"`
	LogPath    string `json:"log_path,omitempty"`
	HealthURL  string `json:"health_url,omitempty"`
	Health     string `json:"health,omitempty"`
}

type mainSyncTool struct {
	feature *SkynetWorkflowsFeature
}

func (t *mainSyncTool) Name() string { return "skynet_main_sync" }

func (t *mainSyncTool) Description() string {
	return "Fast-forward a clean local main deployment worktree and optionally restart the local ResearchCrafters web app."
}

func (t *mainSyncTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":      map[string]any{"type": "string", "enum": []string{"trigger"}},
			"repository":  map[string]any{"type": "string"},
			"branch":      map[string]any{"type": "string", "description": "Branch to sync. Defaults to main."},
			"commit":      map[string]any{"type": "string", "description": "Expected upstream commit SHA from the push event."},
			"before":      map[string]any{"type": "string"},
			"compare_url": map[string]any{"type": "string"},
			"pusher":      map[string]any{"type": "string"},
			"target_repo": map[string]any{"type": "string", "description": "Repository used as the source remote/worktree host."},
			"deploy_repo": map[string]any{"type": "string", "description": "Clean deployment worktree. Defaults to a sibling <repo>-main path."},
			"port":        map[string]any{"type": "string", "description": "Local web port. Defaults to configured value or 3000."},
			"session":     map[string]any{"type": "string", "description": "screen session for the managed web server."},
			"start_web":   map[string]any{"type": "boolean", "description": "Restart the managed web server after syncing."},
			"force":       map[string]any{"type": "boolean", "description": "Run even if this branch/commit was already synced."},
			"channel":     map[string]any{"type": "string"},
			"chat_id":     map[string]any{"type": "string"},
			"local_key":   map[string]any{"type": "string"},
			"peer_kind":   map[string]any{"type": "string"},
		},
		"required": []string{"action"},
	}
}

func (t *mainSyncTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("skynet_workflows feature is not initialized")
	}
	if targetRepo := stringArg(args, "target_repo"); targetRepo != "" {
		if err := t.feature.setTargetRepo(ctx, targetRepo); err != nil {
			return tools.ErrorResult(err.Error())
		}
	}
	result, err := t.feature.syncMainDeployment(ctx, args, originFromToolContext(ctx, args))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return jsonResult(result)
}

func (f *SkynetWorkflowsFeature) syncMainDeployment(ctx context.Context, args map[string]any, origin workflowOrigin) (mainSyncResult, error) {
	if f == nil {
		return mainSyncResult{}, fmt.Errorf("skynet workflows feature is unavailable")
	}
	branch := strings.TrimSpace(stringArg(args, "branch"))
	if branch == "" {
		branch = defaultMainSyncBranch
	}
	if !safeMainSyncBranch(branch) {
		return mainSyncResult{}, fmt.Errorf("unsafe branch name: %q", branch)
	}
	targetRepo := strings.TrimSpace(stringArg(args, "target_repo"))
	if targetRepo == "" {
		targetRepo = f.resolveTargetRepo(ctx)
	}
	if targetRepo == "" {
		return mainSyncResult{}, fmt.Errorf("target_repo is required")
	}
	targetRepo = filepath.Clean(targetRepo)
	deployRepo := strings.TrimSpace(stringArg(args, "deploy_repo"))
	if deployRepo == "" {
		deployRepo = f.configuredDeployRepo(ctx)
	}
	if deployRepo == "" {
		deployRepo = siblingMainDeployRepo(targetRepo)
	}
	deployRepo = filepath.Clean(deployRepo)
	port := strings.TrimSpace(stringArg(args, "port"))
	if port == "" {
		port = f.configuredWebPort(ctx)
	}
	if port == "" {
		port = defaultMainSyncPort
	}
	if !safePort(port) {
		return mainSyncResult{}, fmt.Errorf("unsafe web port: %q", port)
	}
	session := strings.TrimSpace(stringArg(args, "session"))
	if session == "" {
		session = defaultMainSyncSession
	}
	if !safeSessionName(session) {
		return mainSyncResult{}, fmt.Errorf("unsafe screen session name: %q", session)
	}

	if !boolArg(args, "force") {
		duplicate, err := f.mainSyncAlreadyDone(ctx, args, branch)
		if err != nil {
			slog.Warn("skynet main sync dedupe check failed", "error", err)
		}
		if duplicate {
			return mainSyncResult{
				Status:     "skipped_duplicate",
				Branch:     branch,
				Commit:     stringArg(args, "commit"),
				TargetRepo: targetRepo,
				DeployRepo: deployRepo,
				Port:       port,
				Session:    session,
			}, nil
		}
	}

	lockDir := filepath.Join(os.TempDir(), "goclaw-skynet-main-sync-"+configKeyPart(targetRepo)+"-"+configKeyPart(branch)+".lock")
	if err := os.Mkdir(lockDir, 0o755); err != nil {
		if os.IsExist(err) {
			return mainSyncResult{}, fmt.Errorf("main sync is already running for %s %s", targetRepo, branch)
		}
		return mainSyncResult{}, err
	}
	defer os.Remove(lockDir)

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if err := ensureGitRepo(runCtx, targetRepo); err != nil {
		return mainSyncResult{}, err
	}
	if _, err := runCommand(runCtx, targetRepo, "git", "fetch", "origin", branch); err != nil {
		return mainSyncResult{}, err
	}
	if err := ensureDeployWorktree(runCtx, targetRepo, deployRepo, branch); err != nil {
		return mainSyncResult{}, err
	}
	if err := ensureCleanWorktree(runCtx, deployRepo); err != nil {
		return mainSyncResult{}, err
	}
	oldCommit, _ := runCommand(runCtx, deployRepo, "git", "rev-parse", "HEAD")
	oldCommit = strings.TrimSpace(oldCommit)
	currentBranch, _ := runCommand(runCtx, deployRepo, "git", "branch", "--show-current")
	if strings.TrimSpace(currentBranch) != branch {
		return mainSyncResult{}, fmt.Errorf("deployment worktree %s is on branch %q, expected %q", deployRepo, strings.TrimSpace(currentBranch), branch)
	}
	if _, err := runCommand(runCtx, deployRepo, "git", "fetch", "origin", branch); err != nil {
		return mainSyncResult{}, err
	}
	if _, err := runCommand(runCtx, deployRepo, "git", "merge", "--ff-only", "origin/"+branch); err != nil {
		return mainSyncResult{}, err
	}
	newCommit, err := runCommand(runCtx, deployRepo, "git", "rev-parse", "HEAD")
	if err != nil {
		return mainSyncResult{}, err
	}
	newCommit = strings.TrimSpace(newCommit)
	expectedCommit := strings.TrimSpace(stringArg(args, "commit"))
	if expectedCommit != "" && !strings.HasPrefix(newCommit, expectedCommit) && !strings.HasPrefix(expectedCommit, newCommit) {
		return mainSyncResult{}, fmt.Errorf("synced commit %s does not match expected push commit %s", newCommit, expectedCommit)
	}

	result := mainSyncResult{
		Status:     "synced",
		Branch:     branch,
		Commit:     newCommit,
		OldCommit:  oldCommit,
		TargetRepo: targetRepo,
		DeployRepo: deployRepo,
		Port:       port,
		Session:    session,
	}
	if boolArg(args, "start_web") {
		logPath, health, err := restartMainWeb(runCtx, deployRepo, port, session)
		result.LogPath = logPath
		result.HealthURL = "http://127.0.0.1:" + port + "/api/health"
		result.Health = health
		if err != nil {
			f.publishMainSyncLog(ctx, args, origin, result, err)
			return result, err
		}
		result.Status = "synced_and_restarted"
	}
	if err := f.markMainSyncDone(ctx, args, branch, newCommit); err != nil {
		slog.Warn("skynet main sync dedupe mark failed", "error", err)
	}
	f.publishMainSyncLog(ctx, args, origin, result, nil)
	return result, nil
}

func ensureGitRepo(ctx context.Context, path string) error {
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return fmt.Errorf("%s is not a git worktree: %w", path, err)
	}
	if _, err := runCommand(ctx, path, "git", "rev-parse", "--is-inside-work-tree"); err != nil {
		return err
	}
	return nil
}

func ensureDeployWorktree(ctx context.Context, sourceRepo, deployRepo, branch string) error {
	if _, err := os.Stat(filepath.Join(deployRepo, ".git")); err == nil {
		return ensureGitRepo(ctx, deployRepo)
	}
	if err := os.MkdirAll(filepath.Dir(deployRepo), 0o755); err != nil {
		return err
	}
	if _, err := runCommand(ctx, sourceRepo, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, err = runCommand(ctx, sourceRepo, "git", "worktree", "add", deployRepo, branch)
		return err
	}
	_, err := runCommand(ctx, sourceRepo, "git", "worktree", "add", "-b", branch, deployRepo, "origin/"+branch)
	return err
}

func ensureCleanWorktree(ctx context.Context, repo string) error {
	status, err := runCommand(ctx, repo, "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("deployment worktree %s has local changes; refusing to sync:\n%s", repo, strings.TrimSpace(status))
	}
	return nil
}

func restartMainWeb(ctx context.Context, deployRepo, port, session string) (string, string, error) {
	logPath := filepath.Join(os.TempDir(), session+".log")
	_, _ = runCommand(ctx, "", "screen", "-S", session, "-X", "quit")
	time.Sleep(1500 * time.Millisecond)
	if pids := listeningPIDs(ctx, port); pids != "" {
		return logPath, "blocked_port_in_use", fmt.Errorf("port %s is already in use by unmanaged process(es): %s", port, pids)
	}
	script := fmt.Sprintf("cd %s && RC_HOST=127.0.0.1 RC_PORT=%s ./infra/scripts/host-local.sh >> %s 2>&1",
		shellQuote(deployRepo),
		shellQuote(port),
		shellQuote(logPath),
	)
	if _, err := runCommand(ctx, "", "screen", "-dmS", session, "bash", "-lc", script); err != nil {
		return logPath, "start_failed", err
	}
	healthURL := "http://127.0.0.1:" + port + "/api/health"
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return logPath, "health_request_failed", err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return logPath, fmt.Sprintf("http_%d", resp.StatusCode), nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return logPath, "health_pending", nil
}

func listeningPIDs(ctx context.Context, port string) string {
	out, err := runCommand(ctx, "", "lsof", "-tiTCP:"+port, "-sTCP:LISTEN")
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(out), ",")
}

func runCommand(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		return text, fmt.Errorf("%s %s failed: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(text))
	}
	return text, nil
}

func (f *SkynetWorkflowsFeature) configuredDeployRepo(ctx context.Context) string {
	if value := strings.TrimSpace(os.Getenv("GOCLAW_SKYNET_DEPLOY_REPO")); value != "" {
		return value
	}
	if f.sysConfigs != nil {
		tenantCtx := storepkg.WithTenantID(ctx, storepkg.MasterTenantID)
		if value, err := f.sysConfigs.Get(tenantCtx, configKeyDeployRepo); err == nil {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (f *SkynetWorkflowsFeature) configuredWebPort(ctx context.Context) string {
	if value := strings.TrimSpace(os.Getenv("GOCLAW_SKYNET_WEB_PORT")); value != "" {
		return value
	}
	if f.sysConfigs != nil {
		tenantCtx := storepkg.WithTenantID(ctx, storepkg.MasterTenantID)
		if value, err := f.sysConfigs.Get(tenantCtx, configKeyWebPort); err == nil {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func siblingMainDeployRepo(targetRepo string) string {
	base := filepath.Base(filepath.Clean(targetRepo))
	parent := filepath.Dir(filepath.Clean(targetRepo))
	return filepath.Join(parent, base+"-main")
}

func safePort(port string) bool {
	if port == "" || len(port) > 5 {
		return false
	}
	for _, ch := range port {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func safeMainSyncBranch(branch string) bool {
	return safeMainSyncBranchRE.MatchString(branch) && !strings.Contains(branch, "..") && !strings.HasPrefix(branch, "-")
}

func safeSessionName(session string) bool {
	if session == "" || len(session) > 80 {
		return false
	}
	for _, ch := range session {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (f *SkynetWorkflowsFeature) mainSyncAlreadyDone(ctx context.Context, args map[string]any, branch string) (bool, error) {
	if f == nil || f.sysConfigs == nil {
		return false, nil
	}
	key := mainSyncConfigKey(args, branch)
	fingerprint := mainSyncFingerprint(args, branch)
	if key == "" || fingerprint == "" {
		return false, nil
	}
	value, err := f.sysConfigs.Get(ctx, key)
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(value) == fingerprint, nil
}

func (f *SkynetWorkflowsFeature) markMainSyncDone(ctx context.Context, args map[string]any, branch, commit string) error {
	if f == nil || f.sysConfigs == nil {
		return nil
	}
	key := mainSyncConfigKey(args, branch)
	if key == "" || commit == "" {
		return nil
	}
	return f.sysConfigs.Set(ctx, key, strings.Join([]string{
		stringArg(args, "repository"),
		branch,
		commit,
	}, "|"))
}

func mainSyncFingerprint(args map[string]any, branch string) string {
	commit := strings.TrimSpace(stringArg(args, "commit"))
	if commit == "" {
		return ""
	}
	return strings.Join([]string{
		stringArg(args, "repository"),
		branch,
		commit,
	}, "|")
}

func mainSyncConfigKey(args map[string]any, branch string) string {
	repo := configKeyPart(stringArg(args, "repository"))
	branchPart := configKeyPart(branch)
	if repo == "" && branchPart == "" {
		return ""
	}
	if repo == "" {
		repo = "unknown-repo"
	}
	if branchPart == "" {
		branchPart = "unknown-branch"
	}
	return "beta.skynet_workflows.main_sync." + repo + "." + branchPart
}

func (f *SkynetWorkflowsFeature) publishMainSyncLog(ctx context.Context, args map[string]any, origin workflowOrigin, result mainSyncResult, runErr error) {
	if f == nil || f.msgBus == nil {
		return
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		origin = f.configuredOrigin(ctx)
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		return
	}
	chatID := origin.ChatID
	if origin.LocalKey != "" {
		chatID = origin.LocalKey
	}
	metadata := map[string]string{
		"skynet_workflow": "true",
	}
	if origin.LocalKey != "" {
		metadata["local_key"] = origin.LocalKey
		if threadID := threadIDFromLocalKey(origin.LocalKey); threadID != "" {
			metadata[tools.MetaMessageThreadID] = threadID
		}
	}
	if !f.msgBus.TryPublishOutbound(bus.OutboundMessage{
		Channel:  origin.Channel,
		ChatID:   chatID,
		Content:  mainSyncLogMessage(args, result, runErr),
		Metadata: metadata,
	}) {
		slog.Warn("beta skynet_workflows: main sync log dropped; outbound buffer is full")
	}
}

func mainSyncLogMessage(args map[string]any, result mainSyncResult, runErr error) string {
	status := "UPDATED"
	if runErr != nil {
		status = "FAILED"
	}
	lines := []string{
		"[SKYNET MAIN SYNC " + status + "]",
	}
	for _, entry := range []struct {
		label string
		value string
	}{
		{label: "Repository", value: stringArg(args, "repository")},
		{label: "Branch", value: result.Branch},
		{label: "Commit", value: shortCommit(result.Commit)},
		{label: "Worktree", value: result.DeployRepo},
		{label: "Status", value: result.Status},
		{label: "Health", value: result.Health},
		{label: "Log", value: result.LogPath},
		{label: "Compare", value: stringArg(args, "compare_url")},
	} {
		if strings.TrimSpace(entry.value) != "" {
			lines = append(lines, entry.label+": "+entry.value)
		}
	}
	if runErr != nil {
		lines = append(lines, "", "Error: "+runErr.Error())
	}
	return strings.Join(lines, "\n")
}

func shortCommit(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
