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

func TestWorktreeCleanupRemovesMergedCleanWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for worktree cleanup tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	repo := initCleanupTestRepo(t, ctx)
	worktree := filepath.Join(t.TempDir(), "finished")
	if _, err := runCommand(ctx, repo, "git", "branch", "finished"); err != nil {
		t.Fatalf("branch finished: %v", err)
	}
	if _, err := runCommand(ctx, repo, "git", "worktree", "add", worktree, "finished"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "done.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(ctx, worktree, "git", "add", "done.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runCommand(ctx, worktree, "git", "commit", "-m", "finish worktree branch"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	if _, err := runCommand(ctx, repo, "git", "merge", "--ff-only", "finished"); err != nil {
		t.Fatalf("merge branch: %v", err)
	}
	expectedWorktreePath := absCleanPath(worktree)

	feature := &SkynetWorkflowsFeature{targetRepo: repo}
	result, err := feature.cleanupFinishedWorktrees(ctx, worktreeCleanupOptions{
		TargetRepo: repo,
		BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("cleanupFinishedWorktrees: %v", err)
	}
	if len(result.Removed) != 1 {
		t.Fatalf("removed = %#v, skipped = %#v; want one removed", result.Removed, result.Skipped)
	}
	if result.Removed[0].Path != expectedWorktreePath {
		t.Fatalf("removed path = %q, want %q", result.Removed[0].Path, expectedWorktreePath)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists or stat failed unexpectedly: %v", err)
	}
}

func TestWorktreeCleanupSkipsDirtyWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for worktree cleanup tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	repo := initCleanupTestRepo(t, ctx)
	worktree := filepath.Join(t.TempDir(), "dirty")
	if _, err := runCommand(ctx, repo, "git", "branch", "dirty"); err != nil {
		t.Fatalf("branch dirty: %v", err)
	}
	if _, err := runCommand(ctx, repo, "git", "worktree", "add", worktree, "dirty"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	feature := &SkynetWorkflowsFeature{targetRepo: repo}
	result, err := feature.cleanupFinishedWorktrees(ctx, worktreeCleanupOptions{
		TargetRepo: repo,
		BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("cleanupFinishedWorktrees: %v", err)
	}
	if len(result.Removed) != 0 {
		t.Fatalf("removed dirty worktree: %#v", result.Removed)
	}
	foundDirtySkip := false
	for _, skipped := range result.Skipped {
		if strings.Contains(skipped.Reason, "dirty worktree") {
			foundDirtySkip = true
			break
		}
	}
	if !foundDirtySkip {
		t.Fatalf("skipped = %#v, want dirty worktree reason", result.Skipped)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("dirty worktree should remain: %v", err)
	}
}

func initCleanupTestRepo(t *testing.T, ctx context.Context) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(ctx, repo, "git", "init", "-b", "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if _, err := runCommand(ctx, repo, "git", "config", "user.email", "test@example.com"); err != nil {
		t.Fatalf("git config email: %v", err)
	}
	if _, err := runCommand(ctx, repo, "git", "config", "user.name", "Test User"); err != nil {
		t.Fatalf("git config name: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(ctx, repo, "git", "add", "README.md"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runCommand(ctx, repo, "git", "commit", "-m", "initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	return repo
}
