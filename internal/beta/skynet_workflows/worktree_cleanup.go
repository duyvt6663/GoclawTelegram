package skynetworkflows

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type worktreeCleanupTool struct {
	feature *SkynetWorkflowsFeature
}

type worktreeCleanupOptions struct {
	TargetRepo     string `json:"target_repo,omitempty"`
	BaseBranch     string `json:"base_branch,omitempty"`
	DryRun         bool   `json:"dry_run,omitempty"`
	RemoveBranches bool   `json:"remove_branches,omitempty"`
}

type worktreeCleanupResult struct {
	Status      string                 `json:"status"`
	TargetRepo  string                 `json:"target_repo"`
	BaseBranch  string                 `json:"base_branch"`
	BaseRef     string                 `json:"base_ref"`
	DryRun      bool                   `json:"dry_run"`
	Removed     []worktreeCleanupEntry `json:"removed"`
	Skipped     []worktreeCleanupSkip  `json:"skipped"`
	Pruned      bool                   `json:"pruned"`
	FetchStatus string                 `json:"fetch_status,omitempty"`
}

type worktreeCleanupEntry struct {
	Path              string `json:"path"`
	Branch            string `json:"branch,omitempty"`
	Head              string `json:"head,omitempty"`
	BranchDeleted     bool   `json:"branch_deleted,omitempty"`
	BranchDeleteError string `json:"branch_delete_error,omitempty"`
}

type worktreeCleanupSkip struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Reason string `json:"reason"`
}

type gitWorktreeInfo struct {
	Path       string
	Head       string
	BranchRef  string
	Locked     bool
	LockReason string
	Detached   bool
	Bare       bool
}

func (t *worktreeCleanupTool) Name() string { return "skynet_worktree_cleanup" }

func (t *worktreeCleanupTool) Description() string {
	return "Clean finished git worktrees for the configured Skynet target repository. Only clean, unlocked worktrees whose HEAD is already contained in the base branch are removed."
}

func (t *worktreeCleanupTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":          map[string]any{"type": "string", "enum": []string{"run", "status"}, "description": "run removes eligible worktrees; status performs a dry run."},
			"target_repo":     map[string]any{"type": "string", "description": "Optional target repository path. Defaults to the configured Skynet target repo."},
			"base_branch":     map[string]any{"type": "string", "description": "Base branch that must contain a worktree HEAD before cleanup. Defaults to main."},
			"dry_run":         map[string]any{"type": "boolean", "description": "Inspect eligible worktrees without removing them."},
			"remove_branches": map[string]any{"type": "boolean", "description": "After removing a worktree, also delete its local branch with git branch -d when safe."},
		},
	}
}

func (t *worktreeCleanupTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("skynet worktree cleanup feature is not initialized")
	}
	action := strings.ToLower(stringArg(args, "action"))
	if action == "" {
		action = "run"
	}
	if action != "run" && action != "status" {
		return tools.ErrorResult("unsupported action: " + action)
	}
	if targetRepo := stringArg(args, "target_repo"); targetRepo != "" {
		if err := t.feature.setTargetRepo(ctx, targetRepo); err != nil {
			return tools.ErrorResult(err.Error())
		}
	}
	opts := worktreeCleanupOptions{
		TargetRepo:     stringArg(args, "target_repo"),
		BaseBranch:     stringArg(args, "base_branch"),
		DryRun:         boolArg(args, "dry_run") || action == "status",
		RemoveBranches: boolArg(args, "remove_branches"),
	}
	result, err := t.feature.cleanupFinishedWorktrees(ctx, opts)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return jsonResult(result)
}

func (f *SkynetWorkflowsFeature) cleanupFinishedWorktrees(ctx context.Context, opts worktreeCleanupOptions) (*worktreeCleanupResult, error) {
	targetRepo := strings.TrimSpace(opts.TargetRepo)
	if targetRepo == "" {
		targetRepo = f.resolveTargetRepo(ctx)
	}
	if targetRepo == "" {
		return nil, fmt.Errorf("target_repo is required")
	}
	targetRepo = filepath.Clean(targetRepo)
	if !safeGitBranchName(defaultString(opts.BaseBranch, "main")) {
		return nil, fmt.Errorf("unsafe base_branch: %q", opts.BaseBranch)
	}
	baseBranch := defaultString(opts.BaseBranch, "main")

	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if _, err := runCommand(runCtx, targetRepo, "git", "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, fmt.Errorf("target_repo is not a git worktree: %w", err)
	}

	result := &worktreeCleanupResult{
		Status:     "completed",
		TargetRepo: targetRepo,
		BaseBranch: baseBranch,
		DryRun:     opts.DryRun,
	}
	if _, err := runCommand(runCtx, targetRepo, "git", "fetch", "--prune", "origin", baseBranch); err != nil {
		result.FetchStatus = "skipped_or_failed: " + strings.TrimSpace(err.Error())
	} else {
		result.FetchStatus = "fetched origin/" + baseBranch
	}
	baseRef := "origin/" + baseBranch
	if _, err := runCommand(runCtx, targetRepo, "git", "rev-parse", "--verify", "--quiet", baseRef); err != nil {
		if _, localErr := runCommand(runCtx, targetRepo, "git", "rev-parse", "--verify", "--quiet", baseBranch); localErr != nil {
			return nil, fmt.Errorf("base branch %q is unavailable locally or as origin/%s", baseBranch, baseBranch)
		}
		baseRef = baseBranch
	}
	result.BaseRef = baseRef

	out, err := runCommand(runCtx, targetRepo, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	worktrees := parseGitWorktreePorcelain(out)
	targetAbs := absCleanPath(targetRepo)
	protected := map[string]string{}
	for _, path := range []string{
		f.configuredDeployRepo(ctx),
		siblingMainDeployRepo(targetRepo),
	} {
		if clean := absCleanPath(path); clean != "" {
			protected[clean] = path
		}
	}

	for _, wt := range worktrees {
		wtPath := filepath.Clean(wt.Path)
		branch := shortWorktreeBranch(wt.BranchRef)
		entry := worktreeCleanupEntry{
			Path:   wtPath,
			Branch: branch,
			Head:   wt.Head,
		}
		skip := func(reason string) {
			result.Skipped = append(result.Skipped, worktreeCleanupSkip{Path: wtPath, Branch: branch, Reason: reason})
		}

		if wtPath == "" {
			skip("missing path")
			continue
		}
		if sameCleanPath(wtPath, targetAbs) {
			skip("primary target worktree")
			continue
		}
		if protected[absCleanPath(wtPath)] != "" {
			skip("protected deployment worktree")
			continue
		}
		if wt.Locked {
			reason := "locked worktree"
			if wt.LockReason != "" {
				reason += ": " + wt.LockReason
			}
			skip(reason)
			continue
		}
		if wt.Bare {
			skip("bare worktree")
			continue
		}
		if wt.Detached || branch == "" {
			skip("detached worktree")
			continue
		}
		if isProtectedWorktreeBranch(branch, baseBranch) {
			skip("protected branch")
			continue
		}
		status, err := runCommand(runCtx, wtPath, "git", "status", "--porcelain")
		if err != nil {
			skip("status failed: " + strings.TrimSpace(err.Error()))
			continue
		}
		if strings.TrimSpace(status) != "" {
			skip("dirty worktree")
			continue
		}
		if _, err := runCommand(runCtx, wtPath, "git", "merge-base", "--is-ancestor", "HEAD", baseRef); err != nil {
			skip("head not contained in " + baseRef)
			continue
		}
		if opts.DryRun {
			result.Removed = append(result.Removed, entry)
			continue
		}
		if _, err := runCommand(runCtx, targetRepo, "git", "worktree", "remove", wtPath); err != nil {
			skip("remove failed: " + strings.TrimSpace(err.Error()))
			continue
		}
		if opts.RemoveBranches && branch != "" {
			if _, err := runCommand(runCtx, targetRepo, "git", "branch", "-d", branch); err != nil {
				entry.BranchDeleteError = strings.TrimSpace(err.Error())
			} else {
				entry.BranchDeleted = true
			}
		}
		result.Removed = append(result.Removed, entry)
	}

	if !opts.DryRun {
		if _, err := runCommand(runCtx, targetRepo, "git", "worktree", "prune"); err == nil {
			result.Pruned = true
		}
	}
	return result, nil
}

func parseGitWorktreePorcelain(text string) []gitWorktreeInfo {
	var out []gitWorktreeInfo
	var current *gitWorktreeInfo
	flush := func() {
		if current != nil && strings.TrimSpace(current.Path) != "" {
			out = append(out, *current)
		}
		current = nil
	}
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		key, value, hasValue := strings.Cut(line, " ")
		if key == "worktree" {
			flush()
			current = &gitWorktreeInfo{Path: strings.TrimSpace(value)}
			continue
		}
		if current == nil {
			continue
		}
		switch key {
		case "HEAD":
			current.Head = strings.TrimSpace(value)
		case "branch":
			current.BranchRef = strings.TrimSpace(value)
		case "locked":
			current.Locked = true
			if hasValue {
				current.LockReason = strings.TrimSpace(value)
			}
		case "detached":
			current.Detached = true
		case "bare":
			current.Bare = true
		}
	}
	flush()
	return out
}

func shortWorktreeBranch(branchRef string) string {
	branchRef = strings.TrimSpace(branchRef)
	if branchRef == "" {
		return ""
	}
	return strings.TrimPrefix(branchRef, "refs/heads/")
}

func isProtectedWorktreeBranch(branch, baseBranch string) bool {
	branch = strings.TrimSpace(branch)
	switch branch {
	case "", baseBranch, "main", "master":
		return true
	default:
		return false
	}
}

func safeGitBranchName(branch string) bool {
	return safeMainSyncBranch(branch)
}

func defaultString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func absCleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			return filepath.Clean(resolved)
		}
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

func sameCleanPath(a, b string) bool {
	return absCleanPath(a) == absCleanPath(b)
}
