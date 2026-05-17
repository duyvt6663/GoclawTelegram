package skynetworkflows

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncMainDeploymentStashesDirtyDriftAndChecksOutOriginMain(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for main sync tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	source, deploy := initMainSyncTestRepos(t, ctx)

	if _, err := runCommand(ctx, deploy, "git", "switch", "-c", "stale-feature"); err != nil {
		t.Fatalf("switch deploy feature branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deploy, "local-drift.txt"), []byte("local drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(source, "flagship.txt"), []byte("flagship\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(ctx, source, "git", "add", "flagship.txt"); err != nil {
		t.Fatalf("add flagship: %v", err)
	}
	if _, err := runCommand(ctx, source, "git", "commit", "-m", "add flagship landing"); err != nil {
		t.Fatalf("commit flagship: %v", err)
	}
	if _, err := runCommand(ctx, source, "git", "push", "origin", "main"); err != nil {
		t.Fatalf("push source main: %v", err)
	}
	expectedCommit, err := runCommand(ctx, source, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("source rev-parse: %v", err)
	}
	expectedCommit = strings.TrimSpace(expectedCommit)

	feature := &SkynetWorkflowsFeature{}
	result, err := feature.syncMainDeployment(ctx, map[string]any{
		"repository":   "example/ResearchCrafters",
		"branch":       "main",
		"commit":       expectedCommit,
		"target_repo":  source,
		"deploy_repo":  deploy,
		"dirty_policy": mainSyncDirtyPolicyStash,
		"start_web":    false,
	}, workflowOrigin{})
	if err != nil {
		t.Fatalf("syncMainDeployment: %v", err)
	}

	if result.Status != "synced_after_recovery" {
		t.Fatalf("Status = %q, want synced_after_recovery; result = %#v", result.Status, result)
	}
	if result.DirtyPolicy != mainSyncDirtyPolicyStash {
		t.Fatalf("DirtyPolicy = %q", result.DirtyPolicy)
	}
	if result.DirtyStatus == "" || !strings.Contains(result.DirtyStatus, "local-drift.txt") {
		t.Fatalf("DirtyStatus = %q, want local drift evidence", result.DirtyStatus)
	}
	if !strings.Contains(result.DirtyRecovery, "skynet-main-sync-main") {
		t.Fatalf("DirtyRecovery = %q, want skynet stash ref", result.DirtyRecovery)
	}
	if result.SyncMode != "detached:origin/main" {
		t.Fatalf("SyncMode = %q", result.SyncMode)
	}

	head, err := runCommand(ctx, deploy, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("deploy rev-parse: %v", err)
	}
	if strings.TrimSpace(head) != expectedCommit {
		t.Fatalf("deploy HEAD = %s, want %s", strings.TrimSpace(head), expectedCommit)
	}
	currentBranch, err := runCommand(ctx, deploy, "git", "branch", "--show-current")
	if err != nil {
		t.Fatalf("deploy branch: %v", err)
	}
	if strings.TrimSpace(currentBranch) != "" {
		t.Fatalf("deploy branch = %q, want detached HEAD", strings.TrimSpace(currentBranch))
	}
	status, err := runCommand(ctx, deploy, "git", "status", "--porcelain")
	if err != nil {
		t.Fatalf("deploy status: %v", err)
	}
	if strings.TrimSpace(status) != "" {
		t.Fatalf("deploy status = %q, want clean", strings.TrimSpace(status))
	}
	stashList, err := runCommand(ctx, deploy, "git", "stash", "list")
	if err != nil {
		t.Fatalf("stash list: %v", err)
	}
	if !strings.Contains(stashList, "skynet-main-sync-main") {
		t.Fatalf("stash list missing recovery stash:\n%s", stashList)
	}
}

func TestSyncMainDeploymentAlreadyCurrentSkipsRestart(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for main sync tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	source, deploy := initMainSyncTestRepos(t, ctx)
	currentCommit, err := runCommand(ctx, source, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("source rev-parse: %v", err)
	}

	feature := &SkynetWorkflowsFeature{}
	result, err := feature.syncMainDeployment(ctx, map[string]any{
		"repository":   "example/ResearchCrafters",
		"branch":       "main",
		"target_repo":  source,
		"deploy_repo":  deploy,
		"dirty_policy": mainSyncDirtyPolicyStash,
		"start_web":    false,
	}, workflowOrigin{})
	if err != nil {
		t.Fatalf("syncMainDeployment: %v", err)
	}

	if result.Status != "already_current" {
		t.Fatalf("Status = %q, want already_current; result = %#v", result.Status, result)
	}
	if result.LogPath != "" || result.Health != "" {
		t.Fatalf("restart fields should be empty for already_current result: %#v", result)
	}
	if result.Commit != strings.TrimSpace(currentCommit) {
		t.Fatalf("Commit = %q, want %q", result.Commit, strings.TrimSpace(currentCommit))
	}
}

func TestSyncMainDeploymentCreatesDetachedDeployWorktreeWhenMissing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for main sync tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	source, _ := initMainSyncTestRepos(t, ctx)
	deploy := filepath.Join(t.TempDir(), "managed-main")

	feature := &SkynetWorkflowsFeature{}
	result, err := feature.syncMainDeployment(ctx, map[string]any{
		"repository":   "example/ResearchCrafters",
		"branch":       "main",
		"target_repo":  source,
		"deploy_repo":  deploy,
		"dirty_policy": mainSyncDirtyPolicyStash,
	}, workflowOrigin{})
	if err != nil {
		t.Fatalf("syncMainDeployment: %v", err)
	}
	if result.Status != "already_current" {
		t.Fatalf("Status = %q, want already_current; result = %#v", result.Status, result)
	}
	currentBranch, err := runCommand(ctx, deploy, "git", "branch", "--show-current")
	if err != nil {
		t.Fatalf("deploy branch: %v", err)
	}
	if strings.TrimSpace(currentBranch) != "" {
		t.Fatalf("deploy branch = %q, want detached HEAD", strings.TrimSpace(currentBranch))
	}
}

func TestManagedMainWebProcessGroupFromSnapshotFindsOrphanedScreenChild(t *testing.T) {
	deployRepo := "/Users/example/github/ResearchCrafters-main"
	logPath := "/tmp/researchcrafters-host-main.log"
	snapshot := map[string]mainSyncProcess{
		"38335": {
			PID:     "38335",
			PPID:    "1",
			PGID:    "38335",
			Command: "login -pflq user /bin/bash -lc cd '/Users/example/github/ResearchCrafters-main' && RC_HOST=127.0.0.1 RC_PORT='3000' ./infra/scripts/host-local.sh >> '/tmp/researchcrafters-host-main.log' 2>&1",
		},
		"38336": {
			PID:     "38336",
			PPID:    "38335",
			PGID:    "38335",
			Command: "bash -lc cd '/Users/example/github/ResearchCrafters-main' && RC_HOST=127.0.0.1 RC_PORT='3000' ./infra/scripts/host-local.sh >> '/tmp/researchcrafters-host-main.log' 2>&1",
		},
		"38339": {
			PID:     "38339",
			PPID:    "38336",
			PGID:    "38335",
			Command: "node /Users/example/.nvm/versions/node/v22/bin/pnpm --filter @researchcrafters/web exec next start -H 127.0.0.1 -p 3000",
		},
		"38819": {
			PID:     "38819",
			PPID:    "38339",
			PGID:    "38335",
			Command: "next-server (v15.5.16)",
		},
	}

	pgid, ok := managedMainWebProcessGroupFromSnapshot("38819", snapshot, deployRepo, "3000", "researchcrafters-host-main", logPath)
	if !ok {
		t.Fatal("managedMainWebProcessGroupFromSnapshot returned ok=false")
	}
	if pgid != "38335" {
		t.Fatalf("pgid = %q, want 38335", pgid)
	}
}

func TestManagedMainWebProcessGroupFromSnapshotRejectsUnrelatedListener(t *testing.T) {
	snapshot := map[string]mainSyncProcess{
		"100": {
			PID:     "100",
			PPID:    "1",
			PGID:    "100",
			Command: "node unrelated-server.js",
		},
	}

	if pgid, ok := managedMainWebProcessGroupFromSnapshot("100", snapshot, "/tmp/ResearchCrafters-main", "3000", "researchcrafters-host-main", "/tmp/researchcrafters-host-main.log"); ok {
		t.Fatalf("managedMainWebProcessGroupFromSnapshot = (%q, true), want unmanaged", pgid)
	}
}

func TestParseMainSyncProcessLinePreservesCommand(t *testing.T) {
	line := "38819 38339 38335 next-server (v15.5.16)"

	proc, ok := parseMainSyncProcessLine(line)
	if !ok {
		t.Fatal("parseMainSyncProcessLine returned ok=false")
	}
	if proc.PID != "38819" || proc.PPID != "38339" || proc.PGID != "38335" {
		t.Fatalf("process IDs = %#v", proc)
	}
	if proc.Command != "next-server (v15.5.16)" {
		t.Fatalf("Command = %q", proc.Command)
	}
}

func initMainSyncTestRepos(t *testing.T, ctx context.Context) (string, string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	source := filepath.Join(root, "source")
	deploy := filepath.Join(root, "deploy")

	if _, err := runCommand(ctx, "", "git", "init", "--bare", remote); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(ctx, source, "git", "init", "-b", "main"); err != nil {
		t.Fatalf("init source: %v", err)
	}
	if _, err := runCommand(ctx, source, "git", "config", "user.email", "test@example.com"); err != nil {
		t.Fatalf("config source email: %v", err)
	}
	if _, err := runCommand(ctx, source, "git", "config", "user.name", "Test User"); err != nil {
		t.Fatalf("config source name: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(ctx, source, "git", "add", "README.md"); err != nil {
		t.Fatalf("source add: %v", err)
	}
	if _, err := runCommand(ctx, source, "git", "commit", "-m", "initial commit"); err != nil {
		t.Fatalf("source commit: %v", err)
	}
	if _, err := runCommand(ctx, source, "git", "remote", "add", "origin", remote); err != nil {
		t.Fatalf("source remote add: %v", err)
	}
	if _, err := runCommand(ctx, source, "git", "push", "-u", "origin", "main"); err != nil {
		t.Fatalf("source push: %v", err)
	}
	if _, err := runCommand(ctx, remote, "git", "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		t.Fatalf("remote HEAD: %v", err)
	}
	if _, err := runCommand(ctx, root, "git", "clone", remote, deploy); err != nil {
		t.Fatalf("clone deploy: %v", err)
	}
	return source, deploy
}
