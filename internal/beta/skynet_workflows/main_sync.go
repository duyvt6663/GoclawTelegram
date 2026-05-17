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
	"sort"
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

	mainSyncDirtyPolicyFail  = "fail"
	mainSyncDirtyPolicyStash = "stash"
)

var safeMainSyncBranchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

type mainSyncResult struct {
	Status        string `json:"status"`
	Branch        string `json:"branch"`
	Commit        string `json:"commit,omitempty"`
	OldCommit     string `json:"old_commit,omitempty"`
	TargetRepo    string `json:"target_repo"`
	DeployRepo    string `json:"deploy_repo"`
	Port          string `json:"port,omitempty"`
	Session       string `json:"session,omitempty"`
	LogPath       string `json:"log_path,omitempty"`
	HealthURL     string `json:"health_url,omitempty"`
	Health        string `json:"health,omitempty"`
	DirtyPolicy   string `json:"dirty_policy,omitempty"`
	DirtyStatus   string `json:"dirty_status,omitempty"`
	DirtyRecovery string `json:"dirty_recovery,omitempty"`
	SyncMode      string `json:"sync_mode,omitempty"`
}

type mainSyncTool struct {
	feature *SkynetWorkflowsFeature
}

func (t *mainSyncTool) Name() string { return "skynet_main_sync" }

func (t *mainSyncTool) Description() string {
	return "Reconcile the local main deployment worktree to origin/<branch>, preserving dirty changes when requested, and optionally restart the local ResearchCrafters web app."
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
			"dirty_policy": map[string]any{
				"type":        "string",
				"enum":        []string{mainSyncDirtyPolicyStash, mainSyncDirtyPolicyFail},
				"description": "How to handle local deployment worktree changes. Defaults to stash so managed deployment drift can recover without data loss.",
			},
			"channel":   map[string]any{"type": "string"},
			"chat_id":   map[string]any{"type": "string"},
			"local_key": map[string]any{"type": "string"},
			"peer_kind": map[string]any{"type": "string"},
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
	dirtyPolicy, err := normalizeMainSyncDirtyPolicy(stringArg(args, "dirty_policy"))
	if err != nil {
		return mainSyncResult{}, err
	}
	force := boolArg(args, "force")

	if !force {
		duplicate, err := f.mainSyncAlreadyDone(ctx, args, branch)
		if err != nil {
			slog.Warn("skynet main sync dedupe check failed", "error", err)
		}
		if duplicate {
			return mainSyncResult{
				Status:      "skipped_duplicate",
				Branch:      branch,
				Commit:      stringArg(args, "commit"),
				TargetRepo:  targetRepo,
				DeployRepo:  deployRepo,
				Port:        port,
				Session:     session,
				DirtyPolicy: dirtyPolicy,
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
	dirtyStatus, dirtyRecovery, err := prepareDeployWorktree(runCtx, deployRepo, branch, dirtyPolicy)
	if err != nil {
		return mainSyncResult{}, err
	}
	oldCommit, _ := runCommand(runCtx, deployRepo, "git", "rev-parse", "HEAD")
	oldCommit = strings.TrimSpace(oldCommit)
	if _, err := runCommand(runCtx, deployRepo, "git", "fetch", "origin", branch); err != nil {
		return mainSyncResult{}, err
	}
	syncMode, err := syncDeployToOrigin(runCtx, deployRepo, branch)
	if err != nil {
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
		Status:        "synced",
		Branch:        branch,
		Commit:        newCommit,
		OldCommit:     oldCommit,
		TargetRepo:    targetRepo,
		DeployRepo:    deployRepo,
		Port:          port,
		Session:       session,
		DirtyPolicy:   dirtyPolicy,
		DirtyStatus:   dirtyStatus,
		DirtyRecovery: dirtyRecovery,
		SyncMode:      syncMode,
	}
	if oldCommit == newCommit && dirtyRecovery == "" && !force {
		result.Status = "already_current"
		if boolArg(args, "start_web") {
			result.HealthURL = "http://127.0.0.1:" + port + "/api/health"
			result.Health = probeMainWebHealth(runCtx, port)
			if !mainWebHealthAcceptable(result.Health) {
				logPath, health, err := restartMainWeb(runCtx, deployRepo, port, session)
				result.LogPath = logPath
				result.Health = health
				if err != nil {
					f.publishMainSyncLog(ctx, args, origin, result, err)
					return result, err
				}
				result.Status = "already_current_restarted"
			}
		}
		if err := f.markMainSyncDone(ctx, args, branch, newCommit); err != nil {
			slog.Warn("skynet main sync dedupe mark failed", "error", err)
		}
		f.publishMainSyncLog(ctx, args, origin, result, nil)
		return result, nil
	}
	if dirtyRecovery != "" {
		if oldCommit == newCommit {
			result.Status = "recovered_dirty"
		} else {
			result.Status = "synced_after_recovery"
		}
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
		result.Status += "_and_restarted"
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
	if _, err := runCommand(ctx, sourceRepo, "git", "worktree", "add", "--detach", deployRepo, "origin/"+branch); err == nil {
		return nil
	}
	_, err := runCommand(ctx, sourceRepo, "git", "worktree", "add", deployRepo, "origin/"+branch)
	return err
}

func ensureCleanWorktree(ctx context.Context, repo string) error {
	status, err := gitStatusPorcelain(ctx, repo)
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("deployment worktree %s has local changes; refusing to sync:\n%s", repo, strings.TrimSpace(status))
	}
	return nil
}

func prepareDeployWorktree(ctx context.Context, repo, branch, dirtyPolicy string) (string, string, error) {
	status, err := gitStatusPorcelain(ctx, repo)
	if err != nil {
		return "", "", err
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return "", "", nil
	}
	if dirtyPolicy == mainSyncDirtyPolicyFail {
		return status, "", fmt.Errorf("deployment worktree %s has local changes; refusing to sync:\n%s", repo, status)
	}
	recovery, err := stashDirtyWorktree(ctx, repo, branch)
	if err != nil {
		return status, "", err
	}
	remaining, err := gitStatusPorcelain(ctx, repo)
	if err != nil {
		return status, recovery, err
	}
	if strings.TrimSpace(remaining) != "" {
		return status, recovery, fmt.Errorf("deployment worktree %s still has local changes after stash:\n%s", repo, strings.TrimSpace(remaining))
	}
	return status, recovery, nil
}

func gitStatusPorcelain(ctx context.Context, repo string) (string, error) {
	return runCommand(ctx, repo, "git", "status", "--porcelain")
}

func stashDirtyWorktree(ctx context.Context, repo, branch string) (string, error) {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	message := fmt.Sprintf("skynet-main-sync-%s-%s", configKeyPart(branch), stamp)
	out, err := runCommand(ctx, repo, "git", "stash", "push", "--include-untracked", "-m", message)
	if err != nil {
		return "", err
	}
	list, listErr := runCommand(ctx, repo, "git", "stash", "list", "--max-count=1")
	if listErr == nil && strings.TrimSpace(list) != "" {
		return strings.TrimSpace(list), nil
	}
	return strings.TrimSpace(out), nil
}

func syncDeployToOrigin(ctx context.Context, repo, branch string) (string, error) {
	target := "origin/" + branch
	if _, err := runCommand(ctx, repo, "git", "switch", "--detach", target); err == nil {
		return "detached:" + target, nil
	} else {
		switchErr := err
		if _, checkoutErr := runCommand(ctx, repo, "git", "checkout", "--detach", target); checkoutErr != nil {
			return "", fmt.Errorf("git switch to %s failed: %w; git checkout fallback failed: %w", target, switchErr, checkoutErr)
		}
	}
	return "detached:" + target, nil
}

func normalizeMainSyncDirtyPolicy(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	switch normalized {
	case "", mainSyncDirtyPolicyStash, "auto_stash", "preserve", "preserve_stash":
		return mainSyncDirtyPolicyStash, nil
	case mainSyncDirtyPolicyFail, "manual", "refuse":
		return mainSyncDirtyPolicyFail, nil
	default:
		return "", fmt.Errorf("unsupported main sync dirty_policy %q; expected %q or %q", value, mainSyncDirtyPolicyStash, mainSyncDirtyPolicyFail)
	}
}

func restartMainWeb(ctx context.Context, deployRepo, port, session string) (string, string, error) {
	logPath := filepath.Join(os.TempDir(), session+".log")
	_, _ = runCommand(ctx, "", "screen", "-S", session, "-X", "quit")
	time.Sleep(1500 * time.Millisecond)
	if pids := listeningPIDs(ctx, port); pids != "" {
		stoppedGroups, unmanagedPIDs, stopErr := stopManagedMainWebListeners(ctx, deployRepo, port, session, logPath)
		if stopErr != nil {
			return logPath, "stop_failed", stopErr
		}
		if unmanagedPIDs != "" {
			return logPath, "blocked_port_in_use", fmt.Errorf("port %s is already in use by unmanaged process(es): %s", port, unmanagedPIDs)
		}
		if stoppedGroups != "" {
			if remaining := waitForPortRelease(ctx, port, 15*time.Second); remaining != "" {
				return logPath, "blocked_port_in_use", fmt.Errorf("port %s is still in use after stopping managed process group(s) %s: %s", port, stoppedGroups, remaining)
			}
		} else {
			return logPath, "blocked_port_in_use", fmt.Errorf("port %s is already in use by unmanaged process(es): %s", port, pids)
		}
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

func probeMainWebHealth(ctx context.Context, port string) string {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	healthURL := "http://127.0.0.1:" + port + "/api/health"
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, healthURL, nil)
	if err != nil {
		return "health_request_failed"
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "unreachable"
	}
	_ = resp.Body.Close()
	return fmt.Sprintf("http_%d", resp.StatusCode)
}

func mainWebHealthAcceptable(health string) bool {
	if !strings.HasPrefix(health, "http_") {
		return false
	}
	return !strings.HasPrefix(health, "http_5")
}

func listeningPIDs(ctx context.Context, port string) string {
	return strings.Join(listeningPIDList(ctx, port), ",")
}

func listeningPIDList(ctx context.Context, port string) []string {
	out, err := runCommand(ctx, "", "lsof", "-tiTCP:"+port, "-sTCP:LISTEN")
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

func stopManagedMainWebListeners(ctx context.Context, deployRepo, port, session, logPath string) (string, string, error) {
	listeners := listeningPIDList(ctx, port)
	if len(listeners) == 0 {
		return "", "", nil
	}
	snapshot, err := mainSyncProcessSnapshot(ctx)
	if err != nil {
		return "", "", err
	}
	groups := make(map[string]bool)
	var unmanaged []string
	for _, pid := range listeners {
		pgid, ok := managedMainWebProcessGroupFromSnapshot(pid, snapshot, deployRepo, port, session, logPath)
		if !ok || !safeProcessID(pgid) {
			unmanaged = append(unmanaged, pid)
			continue
		}
		groups[pgid] = true
	}
	if len(unmanaged) > 0 {
		sort.Strings(unmanaged)
		return "", strings.Join(unmanaged, ","), nil
	}
	groupList := sortedProcessIDs(groups)
	for _, pgid := range groupList {
		if _, err := runCommand(ctx, "", "kill", "-TERM", "--", "-"+pgid); err != nil {
			if _, killErr := runCommand(ctx, "", "kill", "-KILL", "--", "-"+pgid); killErr != nil {
				return strings.Join(groupList, ","), "", fmt.Errorf("terminate managed process group %s failed: %w; kill fallback failed: %w", pgid, err, killErr)
			}
		}
	}
	return strings.Join(groupList, ","), "", nil
}

type mainSyncProcess struct {
	PID     string
	PPID    string
	PGID    string
	Command string
}

func mainSyncProcessSnapshot(ctx context.Context) (map[string]mainSyncProcess, error) {
	out, err := runCommand(ctx, "", "ps", "-axo", "pid=,ppid=,pgid=,command=")
	if err != nil {
		return nil, err
	}
	processes := make(map[string]mainSyncProcess)
	for _, line := range strings.Split(out, "\n") {
		proc, ok := parseMainSyncProcessLine(line)
		if ok {
			processes[proc.PID] = proc
		}
	}
	return processes, nil
}

func parseMainSyncProcessLine(line string) (mainSyncProcess, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 4 {
		return mainSyncProcess{}, false
	}
	if !safeProcessID(fields[0]) || !safeProcessID(fields[1]) || !safeProcessID(fields[2]) {
		return mainSyncProcess{}, false
	}
	return mainSyncProcess{
		PID:     fields[0],
		PPID:    fields[1],
		PGID:    fields[2],
		Command: strings.Join(fields[3:], " "),
	}, true
}

func managedMainWebProcessGroupFromSnapshot(listenerPID string, snapshot map[string]mainSyncProcess, deployRepo, port, session, logPath string) (string, bool) {
	proc, ok := snapshot[listenerPID]
	if !ok {
		return "", false
	}
	listenerPGID := proc.PGID
	seen := make(map[string]bool)
	for depth, pid := 0, listenerPID; depth < 32 && pid != ""; depth++ {
		if seen[pid] {
			break
		}
		seen[pid] = true
		proc, ok := snapshot[pid]
		if !ok {
			break
		}
		if isManagedMainWebCommand(proc.Command, deployRepo, port, session, logPath) {
			return listenerPGID, true
		}
		if proc.PPID == proc.PID || proc.PPID == "0" {
			break
		}
		pid = proc.PPID
	}
	for _, proc := range snapshot {
		if proc.PGID == listenerPGID && isManagedMainWebCommand(proc.Command, deployRepo, port, session, logPath) {
			return listenerPGID, true
		}
	}
	return "", false
}

func isManagedMainWebCommand(command, deployRepo, port, session, logPath string) bool {
	if deployRepo == "" || port == "" || logPath == "" {
		return false
	}
	return strings.Contains(command, deployRepo) &&
		strings.Contains(command, "host-local.sh") &&
		strings.Contains(command, "RC_PORT="+shellQuote(port)) &&
		strings.Contains(command, logPath) &&
		(session == "" || strings.Contains(logPath, session))
}

func waitForPortRelease(ctx context.Context, port string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		pids := listeningPIDs(ctx, port)
		if pids == "" {
			return ""
		}
		if time.Now().After(deadline) {
			return pids
		}
		select {
		case <-ctx.Done():
			return pids
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func safeProcessID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != "0"
}

func sortedProcessIDs(values map[string]bool) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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
		{label: "Sync mode", value: result.SyncMode},
		{label: "Dirty policy", value: result.DirtyPolicy},
		{label: "Dirty recovery", value: result.DirtyRecovery},
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
