package skynetworkflows

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	Path               string   `json:"path"`
	Branch             string   `json:"branch,omitempty"`
	Head               string   `json:"head,omitempty"`
	ResidueCleaned     []string `json:"residue_cleaned,omitempty"`
	StaleLockRecovered bool     `json:"stale_lock_recovered,omitempty"`
	LockReason         string   `json:"lock_reason,omitempty"`
	BranchDeleted      bool     `json:"branch_deleted,omitempty"`
	BranchDeleteError  string   `json:"branch_delete_error,omitempty"`
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

var worktreeLockPIDRE = regexp.MustCompile(`(?i)\bpid\s+(\d+)\b`)

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
		staleLock := false
		if wt.Locked {
			staleLock = isStaleWorktreeLock(runCtx, wt)
		}
		if wt.Locked && !staleLock {
			reason := "locked worktree"
			if wt.LockReason != "" {
				reason += ": " + wt.LockReason
			}
			skip(reason)
			continue
		}
		if staleLock {
			entry.StaleLockRecovered = true
			entry.LockReason = wt.LockReason
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
		if _, err := runCommand(runCtx, wtPath, "git", "merge-base", "--is-ancestor", "HEAD", baseRef); err != nil {
			skip("head not contained in " + baseRef)
			continue
		}
		status, err := worktreeCleanupStatus(runCtx, wtPath)
		if err != nil {
			skip("status failed: " + strings.TrimSpace(err.Error()))
			continue
		}
		if strings.TrimSpace(status) != "" {
			residue, ok := disposableWorktreeResidue(status)
			if !ok {
				skip("dirty worktree")
				continue
			}
			entry.ResidueCleaned = residue
		}
		if opts.DryRun {
			result.Removed = append(result.Removed, entry)
			continue
		}
		if staleLock {
			if _, err := runCommand(runCtx, targetRepo, "git", "worktree", "unlock", wtPath); err != nil {
				skip("unlock stale lock failed: " + strings.TrimSpace(err.Error()))
				continue
			}
		}
		if len(entry.ResidueCleaned) > 0 {
			if err := cleanDisposableWorktreeResidue(runCtx, wtPath, entry.ResidueCleaned); err != nil {
				skip("generated residue cleanup failed: " + strings.TrimSpace(err.Error()))
				continue
			}
			status, err := worktreeCleanupStatus(runCtx, wtPath)
			if err != nil {
				skip("status after generated residue cleanup failed: " + strings.TrimSpace(err.Error()))
				continue
			}
			if strings.TrimSpace(status) != "" {
				skip("dirty worktree after generated residue cleanup")
				continue
			}
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

func worktreeCleanupStatus(ctx context.Context, repo string) (string, error) {
	return runCommand(ctx, repo, "git", "status", "--porcelain", "--untracked-files=all")
}

func isStaleWorktreeLock(ctx context.Context, wt gitWorktreeInfo) bool {
	if !wt.Locked {
		return false
	}
	pids := worktreeLockPIDs(wt.LockReason)
	if len(pids) == 0 {
		return false
	}
	for _, pid := range pids {
		if processExists(ctx, pid) {
			return false
		}
	}
	return true
}

func worktreeLockPIDs(reason string) []string {
	matches := worktreeLockPIDRE.FindAllStringSubmatch(reason, -1)
	pids := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(match[1]); err == nil && pid > 0 {
			pids = append(pids, match[1])
		}
	}
	return pids
}

func processExists(ctx context.Context, pid string) bool {
	if pid == "" {
		return false
	}
	_, err := runCommand(ctx, "", "kill", "-0", pid)
	return err == nil
}

func disposableWorktreeResidue(status string) ([]string, bool) {
	paths := gitStatusPaths(status)
	if len(paths) == 0 {
		return nil, true
	}
	residue := make(map[string]bool)
	for _, path := range paths {
		root, ok := disposableWorktreeResidueRoot(path)
		if !ok {
			return nil, false
		}
		residue[root] = true
	}
	out := make([]string, 0, len(residue))
	for path := range residue {
		out = append(out, path)
	}
	sort.Strings(out)
	return out, true
}

func gitStatusPaths(status string) []string {
	var paths []string
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 4 {
			return append(paths, line)
		}
		pathPart := strings.TrimSpace(line[3:])
		if strings.Contains(pathPart, " -> ") {
			for _, part := range strings.Split(pathPart, " -> ") {
				paths = append(paths, unquoteGitStatusPath(part))
			}
			continue
		}
		paths = append(paths, unquoteGitStatusPath(pathPart))
	}
	return paths
}

func unquoteGitStatusPath(path string) string {
	path = strings.TrimSpace(path)
	if unquoted, err := strconv.Unquote(path); err == nil {
		return unquoted
	}
	return path
}

func isDisposableWorktreeResiduePath(path string) bool {
	_, ok := disposableWorktreeResidueRoot(path)
	return ok
}

func disposableWorktreeResidueRoot(path string) (string, bool) {
	clean := filepath.ToSlash(strings.Trim(path, "/"))
	if clean == "" || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", false
	}
	base := filepath.Base(clean)
	if base == "node_modules" || strings.Contains(clean, "/node_modules/") || strings.HasSuffix(clean, "/node_modules") {
		return disposablePathRoot(clean, "node_modules"), true
	}
	if base == "screenshots" || clean == "screenshots" || strings.HasPrefix(clean, "screenshots/") || strings.Contains(clean, "/screenshots/") {
		return disposablePathRoot(clean, "screenshots"), true
	}
	if strings.HasPrefix(base, ".pr-body") && strings.HasSuffix(base, ".md") {
		return clean, true
	}
	switch base {
	case ".next", ".turbo", "coverage", "playwright-report", "test-results":
		return disposablePathRoot(clean, base), true
	default:
		return "", false
	}
}

func disposablePathRoot(path, marker string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == marker {
			return strings.Join(parts[:i+1], "/")
		}
	}
	return path
}

func cleanDisposableWorktreeResidue(ctx context.Context, repo string, paths []string) error {
	if _, err := runCommand(ctx, repo, "git", "reset", "--hard", "HEAD"); err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"clean", "-fdx", "--"}, paths...)
	_, err := runCommand(ctx, repo, "git", args...)
	return err
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
