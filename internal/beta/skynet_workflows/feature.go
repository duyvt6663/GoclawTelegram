package skynetworkflows

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/bootstrap"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	featureName = "skynet_workflows"

	configKeyChannel    = "beta.skynet_workflows.channel"
	configKeyChatID     = "beta.skynet_workflows.chat_id"
	configKeyLocalKey   = "beta.skynet_workflows.local_key"
	configKeyPeerKind   = "beta.skynet_workflows.peer_kind"
	configKeyTargetRepo = "beta.skynet_workflows.target_repo"
	configKeyDeployRepo = "beta.skynet_workflows.main_deploy_repo"
	configKeyWebPort    = "beta.skynet_workflows.web_port"

	agentKeySkynet             = "skynet"
	agentKeyBuilderBot         = "builder-bot"
	agentKeyCIFixer            = "skynet-ci-fixer"
	agentKeyPRConflictResolver = "skynet-pr-conflict-resolver"
	agentKeyBacklogIterator    = "skynet-backlog-iterator"
	agentKeyExperimentIterator = "skynet-experiment-iterator"
	agentKeyExperimentBacklog  = "skynet-experiment-to-backlog"
	agentKeyQAIterator         = "skynet-qa-iterator"
	agentKeyPRComposer         = "skynet-pr-composer"
	agentKeyFeedbackPlanner    = "skynet-feedback-planner"
	agentKeyMainSyncer         = "skynet-main-syncer"
	agentKeyWorktreeJanitor    = "skynet-worktree-janitor"
	agentKeyRefactorScout      = "skynet-refactor-scout"
	agentKeyERPUXWalker        = "skynet-erp-ux-walker"

	providerNameSkynetClaudeCLI = "skynet-claude-cli"

	modelClaudeSonnet = "sonnet"
	modelClaudeOpus47 = "claude-opus-4-7"
	modelCodex54      = "gpt-5.4"
	reasoningHigh     = "high"
	reasoningXHigh    = "xhigh"
)

var skynetWorkflowTools = []string{
	"skynet_workflows",
	"skynet_backlog",
	"skynet_experiments",
	"skynet_qa",
	"skynet_pr",
	"skynet_ci_failure",
	"skynet_pr_conflict",
	"skynet_main_sync",
	"skynet_feedback_plan",
	"skynet_change_requests",
	"skynet_worktree_cleanup",
}

type SkynetWorkflowsFeature struct {
	cfg           *config.Config
	store         *featureStore
	agentStore    storepkg.AgentStore
	providerStore storepkg.ProviderStore
	cronStore     storepkg.CronStore
	sysConfigs    storepkg.SystemConfigStore
	msgBus        *bus.MessageBus
	workspace     string
	targetRepo    string
}

type workflowOrigin struct {
	Channel  string `json:"channel,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
	LocalKey string `json:"local_key,omitempty"`
	PeerKind string `json:"peer_kind,omitempty"`
}

type ensuredWorkflowAgent struct {
	ID                uuid.UUID `json:"id"`
	AgentKey          string    `json:"agent_key"`
	DisplayName       string    `json:"display_name"`
	Provider          string    `json:"provider"`
	Model             string    `json:"model"`
	ReasoningEffort   string    `json:"reasoning_effort"`
	Workspace         string    `json:"workspace"`
	MaxToolIterations int       `json:"max_tool_iterations"`
}

type workflowAgentSpec struct {
	Key               string
	DisplayName       string
	Frontmatter       string
	ProviderKind      string
	Model             string
	ReasoningEffort   string
	MaxToolIterations int
	Workspace         string
	Tools             []string
	Role              string
}

func (f *SkynetWorkflowsFeature) Name() string { return featureName }

func (f *SkynetWorkflowsFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}
	if deps.Stores.Agents == nil {
		return fmt.Errorf("%s requires an agent store", featureName)
	}

	f.cfg = deps.Config
	f.store = &featureStore{db: deps.Stores.DB}
	f.agentStore = deps.Stores.Agents
	f.providerStore = deps.Stores.Providers
	f.cronStore = deps.Stores.Cron
	f.sysConfigs = deps.Stores.SystemConfigs
	f.msgBus = deps.MessageBus
	f.workspace = deps.Workspace
	f.targetRepo = f.resolveTargetRepo(context.Background())

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&workflowControlTool{feature: f})
		deps.ToolRegistry.Register(&boardTool{feature: f, name: "skynet_backlog", kind: kindBacklog})
		deps.ToolRegistry.Register(&boardTool{feature: f, name: "skynet_experiments", kind: kindExperiment})
		deps.ToolRegistry.Register(&boardTool{feature: f, name: "skynet_qa", kind: kindQA})
		deps.ToolRegistry.Register(&boardTool{feature: f, name: "skynet_pr", kind: kindPR})
		deps.ToolRegistry.Register(&boardTool{feature: f, name: "skynet_change_requests", kind: kindChangeReq})
		deps.ToolRegistry.Register(&ciFailureTool{feature: f})
		deps.ToolRegistry.Register(&prConflictTool{feature: f})
		deps.ToolRegistry.Register(&mainSyncTool{feature: f})
		deps.ToolRegistry.Register(&feedbackPlanTool{feature: f})
		deps.ToolRegistry.Register(&worktreeCleanupTool{feature: f})
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	initCtx := storepkg.WithTenantID(context.Background(), storepkg.MasterTenantID)
	if err := f.ensureWorkflowProviders(initCtx); err != nil {
		return fmt.Errorf("%s providers: %w", featureName, err)
	}
	if _, err := f.ensureAgents(initCtx); err != nil {
		return fmt.Errorf("%s agents: %w", featureName, err)
	}
	if err := f.ensureCronJobs(initCtx); err != nil {
		return fmt.Errorf("%s cron jobs: %w", featureName, err)
	}
	if err := f.ensureSkynetBotAccess(initCtx); err != nil {
		slog.Warn("beta skynet_workflows: skynet access setup skipped", "error", err)
	}

	slog.Info("beta skynet_workflows initialized", "target_repo", f.targetRepo)
	return nil
}

func (f *SkynetWorkflowsFeature) resolveTargetRepo(ctx context.Context) string {
	if value := strings.TrimSpace(os.Getenv("GOCLAW_SKYNET_TARGET_REPO")); value != "" {
		return value
	}
	if f.sysConfigs != nil {
		tenantCtx := storepkg.WithTenantID(ctx, storepkg.MasterTenantID)
		if value, err := f.sysConfigs.Get(tenantCtx, configKeyTargetRepo); err == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if strings.TrimSpace(f.targetRepo) != "" {
		return strings.TrimSpace(f.targetRepo)
	}
	if f.workspace != "" {
		return f.workspace
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

func (f *SkynetWorkflowsFeature) setTargetRepo(ctx context.Context, targetRepo string) error {
	targetRepo = strings.TrimSpace(targetRepo)
	if targetRepo == "" {
		return nil
	}
	f.targetRepo = targetRepo
	if f.sysConfigs != nil {
		if err := f.sysConfigs.Set(ctx, configKeyTargetRepo, targetRepo); err != nil {
			return err
		}
	}
	return nil
}

func (f *SkynetWorkflowsFeature) setDeployRepo(ctx context.Context, deployRepo string) error {
	deployRepo = strings.TrimSpace(deployRepo)
	if deployRepo == "" {
		return nil
	}
	if f.sysConfigs != nil {
		if err := f.sysConfigs.Set(ctx, configKeyDeployRepo, deployRepo); err != nil {
			return err
		}
	}
	return nil
}

func (f *SkynetWorkflowsFeature) setWebPort(ctx context.Context, webPort string) error {
	webPort = strings.TrimSpace(webPort)
	if webPort == "" {
		return nil
	}
	if !safePort(webPort) {
		return fmt.Errorf("unsafe web port: %q", webPort)
	}
	if f.sysConfigs != nil {
		if err := f.sysConfigs.Set(ctx, configKeyWebPort, webPort); err != nil {
			return err
		}
	}
	return nil
}

func (f *SkynetWorkflowsFeature) workflowAgentSpecs(ctx context.Context) []workflowAgentSpec {
	targetRepo := f.resolveTargetRepo(ctx)
	codingTools := append([]string{}, skynetWorkflowTools...)
	codingTools = append(codingTools, "message")
	uxReviewTools := append([]string{}, codingTools...)
	uxReviewTools = append(uxReviewTools, "browser", "read_file", "list_files", "exec")
	refactorScoutTools := append([]string{}, codingTools...)
	refactorScoutTools = append(refactorScoutTools, "read_file", "list_files", "exec", "memory_search", "memory_get")
	mainSyncTools := []string{"skynet_workflows", "skynet_main_sync", "message"}
	targetWorkspace := targetRepo
	if targetWorkspace == "" {
		targetWorkspace = safeWorkspace(f.workspace, "skynet-target")
	}

	return []workflowAgentSpec{
		{
			Key:               agentKeyCIFixer,
			DisplayName:       "Skynet CI Fixer",
			Frontmatter:       "CI/CD failure responder that inspects failing runs, patches the target repository, verifies locally, and reports the result.",
			ProviderKind:      storepkg.ProviderClaudeCLI,
			Model:             modelClaudeSonnet,
			ReasoningEffort:   reasoningHigh,
			MaxToolIterations: 24,
			Workspace:         targetWorkspace,
			Tools:             codingTools,
			Role:              "ci-fixer",
		},
		{
			Key:               agentKeyPRConflictResolver,
			DisplayName:       "Skynet PR Conflict Resolver",
			Frontmatter:       "Pull-request conflict responder that updates conflicted PR branches against the base branch, resolves merge conflicts, verifies, pushes, and reports the result.",
			ProviderKind:      storepkg.ProviderClaudeCLI,
			Model:             modelClaudeSonnet,
			ReasoningEffort:   reasoningHigh,
			MaxToolIterations: 24,
			Workspace:         targetWorkspace,
			Tools:             codingTools,
			Role:              "pr-conflict-resolver",
		},
		{
			Key:               agentKeyBacklogIterator,
			DisplayName:       "Skynet Backlog Iterator",
			Frontmatter:       "Backlog worker that claims one pending implementation task, validates readiness, refines broad work into child tasks, implements, tests, and moves completed coding work into QA.",
			ProviderKind:      storepkg.ProviderClaudeCLI,
			Model:             modelClaudeOpus47,
			ReasoningEffort:   reasoningHigh,
			MaxToolIterations: 30,
			Workspace:         targetWorkspace,
			Tools:             codingTools,
			Role:              "backlog-iterator",
		},
		{
			Key:               agentKeyExperimentIterator,
			DisplayName:       "Skynet Experiment Iterator",
			Frontmatter:       "Experiment worker that claims one UX/product experiment, builds a sandbox validation, and asks the channel for review.",
			ProviderKind:      storepkg.ProviderClaudeCLI,
			Model:             modelClaudeOpus47,
			ReasoningEffort:   reasoningHigh,
			MaxToolIterations: 30,
			Workspace:         targetWorkspace,
			Tools:             codingTools,
			Role:              "experiment-iterator",
		},
		{
			Key:               agentKeyExperimentBacklog,
			DisplayName:       "Skynet Experiment Transition Planner",
			Frontmatter:       "Transition planner that converts accepted experiments into concrete backlog implementation plans.",
			ProviderKind:      storepkg.ProviderCodexCLI,
			Model:             modelCodex54,
			ReasoningEffort:   reasoningHigh,
			MaxToolIterations: 18,
			Workspace:         targetWorkspace,
			Tools:             skynetWorkflowTools,
			Role:              "experiment-to-backlog",
		},
		{
			Key:               agentKeyQAIterator,
			DisplayName:       "Skynet QA Iterator",
			Frontmatter:       "QA worker that claims one pending QA item, verifies behavior, sends passes to PR composition, or sends actionable failures back to backlog.",
			ProviderKind:      storepkg.ProviderClaudeCLI,
			Model:             modelClaudeOpus47,
			ReasoningEffort:   reasoningHigh,
			MaxToolIterations: 24,
			Workspace:         targetWorkspace,
			Tools:             codingTools,
			Role:              "qa-iterator",
		},
		{
			Key:               agentKeyPRComposer,
			DisplayName:       "Skynet PR Composer",
			Frontmatter:       "Post-QA PR composer that gathers passed work, cherry-picks or stages coherent changes, verifies, and prepares a comprehensive pull request.",
			ProviderKind:      storepkg.ProviderClaudeCLI,
			Model:             modelClaudeOpus47,
			ReasoningEffort:   reasoningHigh,
			MaxToolIterations: 30,
			Workspace:         targetWorkspace,
			Tools:             codingTools,
			Role:              "pr-composer",
		},
		{
			Key:               agentKeyFeedbackPlanner,
			DisplayName:       "Skynet Feedback Planner",
			Frontmatter:       "Feedback planner that inspects the local deployment and converts user feedback into actionable backlog items.",
			ProviderKind:      storepkg.ProviderCodexCLI,
			Model:             modelCodex54,
			ReasoningEffort:   reasoningHigh,
			MaxToolIterations: 20,
			Workspace:         targetWorkspace,
			Tools:             codingTools,
			Role:              "feedback-planner",
		},
		{
			Key:               agentKeyMainSyncer,
			DisplayName:       "Skynet Main Syncer",
			Frontmatter:       "Main deployment reconciler that retries local main sync after missed or failed merge webhooks and preserves dirty deployment drift before updating.",
			ProviderKind:      storepkg.ProviderClaudeCLI,
			Model:             modelClaudeSonnet,
			ReasoningEffort:   reasoningHigh,
			MaxToolIterations: 8,
			Workspace:         targetWorkspace,
			Tools:             mainSyncTools,
			Role:              "main-syncer",
		},
		{
			Key:               agentKeyWorktreeJanitor,
			DisplayName:       "Skynet Worktree Janitor",
			Frontmatter:       "Repository hygiene worker that removes clean git worktrees whose commits are already contained in the configured base branch.",
			ProviderKind:      storepkg.ProviderClaudeCLI,
			Model:             modelClaudeSonnet,
			ReasoningEffort:   reasoningHigh,
			MaxToolIterations: 8,
			Workspace:         targetWorkspace,
			Tools:             []string{"skynet_worktree_cleanup", "message"},
			Role:              "worktree-janitor",
		},
		{
			Key:               agentKeyRefactorScout,
			DisplayName:       "Skynet Refactor Scout",
			Frontmatter:       "Scoped maintainability reviewer that samples target repo modules, tracks recurring anti-patterns, and submits human-reviewed refactor change requests.",
			ProviderKind:      storepkg.ProviderClaudeCLI,
			Model:             modelClaudeOpus47,
			ReasoningEffort:   reasoningHigh,
			MaxToolIterations: 30,
			Workspace:         targetWorkspace,
			Tools:             refactorScoutTools,
			Role:              "refactor-scout",
		},
		{
			Key:               agentKeyERPUXWalker,
			DisplayName:       "Skynet ERP UX Walker",
			Frontmatter:       "Daily ERP walkthrough reviewer that exercises one deployed ERP end-to-end, evaluates UI/UX and system gaps, and submits human-reviewed change requests.",
			ProviderKind:      storepkg.ProviderClaudeCLI,
			Model:             modelClaudeOpus47,
			ReasoningEffort:   reasoningXHigh,
			MaxToolIterations: 45,
			Workspace:         targetWorkspace,
			Tools:             uxReviewTools,
			Role:              "erp-ux-walker",
		},
	}
}

func (f *SkynetWorkflowsFeature) ensureAgents(ctx context.Context) ([]ensuredWorkflowAgent, error) {
	agents := make([]ensuredWorkflowAgent, 0)
	for _, spec := range f.workflowAgentSpecs(ctx) {
		agentInfo, err := f.ensureAgent(ctx, spec)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agentInfo)
	}
	return agents, nil
}

func (f *SkynetWorkflowsFeature) ensureAgent(ctx context.Context, spec workflowAgentSpec) (ensuredWorkflowAgent, error) {
	provider := f.pickProvider(ctx, spec.ProviderKind)
	workspace := strings.TrimSpace(spec.Workspace)
	if workspace == "" {
		workspace = safeWorkspace(f.workspace, spec.Key)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return ensuredWorkflowAgent{}, err
	}

	tenantID := storepkg.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		tenantID = storepkg.MasterTenantID
	}
	toolsConfig := mustJSON(map[string]any{
		"profile":   "coding",
		"alsoAllow": uniqueSorted(spec.Tools),
	})
	otherConfig := workflowOtherConfig(spec)

	existing, err := f.agentStore.GetByKey(ctx, spec.Key)
	if err != nil && !isNotFoundError(err) {
		return ensuredWorkflowAgent{}, err
	}

	var agentData *storepkg.AgentData
	if existing == nil {
		agentData = &storepkg.AgentData{
			TenantID:            tenantID,
			AgentKey:            spec.Key,
			DisplayName:         spec.DisplayName,
			Frontmatter:         spec.Frontmatter,
			OwnerID:             "system",
			Provider:            provider.Name,
			Model:               spec.Model,
			ContextWindow:       config.DefaultContextWindow,
			MaxToolIterations:   spec.MaxToolIterations,
			Workspace:           workspace,
			RestrictToWorkspace: true,
			AgentType:           storepkg.AgentTypePredefined,
			Status:              storepkg.AgentStatusActive,
			ToolsConfig:         toolsConfig,
			OtherConfig:         otherConfig,
		}
		if err := f.agentStore.Create(ctx, agentData); err != nil {
			return ensuredWorkflowAgent{}, err
		}
	} else {
		agentData = existing
		updates := map[string]any{}
		if strings.TrimSpace(existing.DisplayName) != spec.DisplayName {
			updates["display_name"] = spec.DisplayName
		}
		if strings.TrimSpace(existing.Frontmatter) != spec.Frontmatter {
			updates["frontmatter"] = spec.Frontmatter
		}
		if strings.TrimSpace(existing.Provider) != provider.Name {
			updates["provider"] = provider.Name
		}
		if strings.TrimSpace(existing.Model) != spec.Model {
			updates["model"] = spec.Model
		}
		if existing.ContextWindow == 0 {
			updates["context_window"] = config.DefaultContextWindow
		}
		if existing.MaxToolIterations != spec.MaxToolIterations {
			updates["max_tool_iterations"] = spec.MaxToolIterations
		}
		if strings.TrimSpace(existing.Workspace) != workspace {
			updates["workspace"] = workspace
		}
		if !existing.RestrictToWorkspace {
			updates["restrict_to_workspace"] = true
		}
		if strings.TrimSpace(existing.AgentType) != storepkg.AgentTypePredefined {
			updates["agent_type"] = storepkg.AgentTypePredefined
		}
		if strings.TrimSpace(existing.Status) != storepkg.AgentStatusActive {
			updates["status"] = storepkg.AgentStatusActive
		}
		mergedTools, changed, err := mergeToolsConfig(existing.ToolsConfig, spec.Tools)
		if err != nil {
			return ensuredWorkflowAgent{}, err
		}
		if changed {
			updates["tools_config"] = mergedTools
		}
		if !jsonBytesEqual(existing.OtherConfig, otherConfig) {
			updates["other_config"] = otherConfig
		}
		if len(updates) > 0 {
			if err := f.agentStore.Update(ctx, existing.ID, updates); err != nil {
				return ensuredWorkflowAgent{}, err
			}
			if reloaded, err := f.agentStore.GetByKey(ctx, spec.Key); err == nil && reloaded != nil {
				agentData = reloaded
			}
		}
	}

	if _, err := bootstrap.SeedToStore(ctx, f.agentStore, agentData.ID, storepkg.AgentTypePredefined); err != nil {
		return ensuredWorkflowAgent{}, err
	}
	for name, content := range f.contextFilesForSpec(spec, workspace) {
		if err := f.agentStore.SetAgentContextFile(ctx, agentData.ID, name, content); err != nil {
			return ensuredWorkflowAgent{}, err
		}
	}

	return ensuredWorkflowAgent{
		ID:                agentData.ID,
		AgentKey:          agentData.AgentKey,
		DisplayName:       spec.DisplayName,
		Provider:          provider.Name,
		Model:             spec.Model,
		ReasoningEffort:   spec.ReasoningEffort,
		Workspace:         workspace,
		MaxToolIterations: spec.MaxToolIterations,
	}, nil
}

func workflowOtherConfig(spec workflowAgentSpec) json.RawMessage {
	return mustJSON(map[string]any{
		"reasoning": map[string]any{
			"override_mode": "custom",
			"effort":        spec.ReasoningEffort,
			"fallback":      "provider_default",
		},
		"skynet_workflow": map[string]any{
			"role":       spec.Role,
			"managed_by": featureName,
		},
		"workspace_sharing": map[string]any{
			"shared_dm":    true,
			"shared_group": true,
		},
	})
}

func mcpToolName(toolName string) string {
	return "mcp__goclaw-bridge__" + toolName
}

func workflowToolNameHint() string {
	return fmt.Sprintf(`Claude CLI exposes GoClaw workflow tools through the MCP bridge. In Claude CLI sessions, call the MCP tool name if the plain name is not directly available:
- %s for skynet_backlog
- %s for skynet_experiments
- %s for skynet_qa
- %s for skynet_pr
- %s for skynet_ci_failure
- %s for skynet_pr_conflict
- %s for skynet_main_sync
- %s for skynet_feedback_plan
- %s for skynet_change_requests
- %s for skynet_worktree_cleanup
`, mcpToolName("skynet_backlog"), mcpToolName("skynet_experiments"), mcpToolName("skynet_qa"), mcpToolName("skynet_pr"), mcpToolName("skynet_ci_failure"), mcpToolName("skynet_pr_conflict"), mcpToolName("skynet_main_sync"), mcpToolName("skynet_feedback_plan"), mcpToolName("skynet_change_requests"), mcpToolName("skynet_worktree_cleanup"))
}

func (f *SkynetWorkflowsFeature) contextFilesForSpec(spec workflowAgentSpec, workspace string) map[string]string {
	targetRepo := f.resolveTargetRepo(context.Background())
	common := fmt.Sprintf(`Target repository: %s
Workspace: %s
Managed workflow feature: %s

Tools:
- Use skynet_backlog for implementation queue items.
- Use skynet_experiments for experiment queue items and channel review transitions.
- Use skynet_change_requests for human-reviewed change requests; CRs must be approved before they route to experiments or backlog.
- Use skynet_qa for QA queue items and QA-to-backlog transitions.
- Use skynet_pr for post-QA PR composition and completion tracking.
- Use skynet_ci_failure only for CI/CD failure intake.
- Use skynet_pr_conflict only for GitHub PR merge-conflict intake.
- Use skynet_main_sync only for local main deployment refresh after main branch updates; default dirty_policy "stash" preserves deploy drift before recovery.
- Use skynet_feedback_plan only for user feedback planning intake.
- Use skynet_worktree_cleanup only for finished-worktree cleanup.

%s

Operational rules:
- Claim one queue item at a time.
- Backlog queue claims are priority-aware, not FIFO. Higher priority metadata is claimed first; old low-priority backlog items receive aging boosts so they cannot starve.
- Validate unclear requests before broad edits; implement only when the item is actionable.
- Do not treat planning/decomposition as implementation failure. If a backlog item is a milestone, rollup, stale note, or lacks acceptance criteria, use the queue's refinement transition and create implementation-sized child bullets.
- Keep changes scoped to the target repository and verify with the local test/build commands you can run.
- Update the queue item status with the matching Skynet tool before ending the run.
- GoClaw mirrors active workflow-only change-request/backlog/experiment/QA/PR items into backlog/99-skynet-workflow-queue.md in the target repo for visibility. Treat it as generated reference; use Skynet tools for state changes.
`, targetRepo, workspace, featureName, workflowToolNameHint())

	role := spec.Role
	var roleRules string
	switch role {
	case "ci-fixer":
		roleRules = `CI fixer flow:
1. Inspect the failure payload and the target repository.
2. Reproduce or narrow the failure locally when practical.
3. Patch only the files needed for the CI failure, preserving unrelated dirty work.
4. Run focused verification for the failing check.
5. If verification passes and git remotes/auth allow it, stage only the scoped CI fix files, commit them, and push the PR branch. If push is blocked, leave the commit ready and report the exact blocker plus command to run.
6. Summarize files changed, verification, branch, and pushed commit. If the fix cannot be completed safely, add a backlog item with the exact blocker and evidence.
`
	case "pr-conflict-resolver":
		roleRules = `PR conflict resolver flow:
1. Inspect the conflict payload, target repository, PR branch, and base branch.
2. Never resolve conflicts directly inside a dirty target checkout. Create a per-PR clean git worktree next to the target repository, for example ../ResearchCrafters-conflict-pr-12, and do all branch switching there.
3. Fetch the latest remotes in that worktree, check out the PR branch, then merge or rebase the latest base branch according to the repository's existing practice.
4. Resolve only merge conflicts and direct fallout from that merge. Preserve feature intent from the PR branch and process/harness updates from the base branch.
5. Run focused verification for touched areas plus a lightweight repository health check when practical.
6. Commit and push the conflict-resolution branch when auth/remotes allow it. If push is blocked, leave the commit ready and report exact commands/blockers.
7. Summarize conflict files, resolution choices, verification, branch, pushed commit, and worktree path. If resolution cannot be completed safely, add a backlog item with the exact blocker and evidence.
`
	case "backlog-iterator":
		roleRules = `Backlog flow:
1. Call skynet_backlog with action "next".
2. If no item is returned, stop.
3. Review priority metadata (priority, priority_feature, priority_reason) and any "related_items" returned by the tool. If you learn that nearby pending backlog items should move up or down because of the active feature/module, call skynet_backlog with action "prioritize", item_id/item_ids, priority, feature, and priority_reason before claiming related work.
4. If nearby pending bullets are tightly related and can be completed safely in one focused change, call skynet_backlog with action "claim_related" and "item_ids" before editing. Otherwise ignore them.
5. Validate readiness by reading linked docs, experiments/archive notes, prior QA, and current code paths.
6. If the item is a broad rollup, milestone, stale, missing acceptance criteria, or needs an experiment/spike first, call skynet_backlog with action "refine". Put concrete implementation-sized child bullets in "text" and explain the dependency/order in "result"; prefix pure experiment-validation children with "Experiment leaf:" so they route to skynet_experiments. Include priority/feature when children should inherit or override the parent queue priority. Do not use "fail" for refinement.
7. If the item or claimed batch is actionable, implement, run focused verification, write or update the repo-root QA report, then call skynet_backlog with action "complete" using "item_ids" for a batch or "item_id" for one item. The complete action queues QA automatically; use "fail" only for an attempted implementation that cannot be completed safely.
`
	case "experiment-iterator":
		roleRules = `Experiment flow:
1. Call skynet_experiments with action "next".
2. For repo-synced experiments, read the linked README, Mock.tsx, and registry entry before editing.
3. Build or update the sandbox experiment, validate interaction manually or with tests, and do not promote production code unless the item explicitly asks for promotion.
4. Call skynet_experiments with action "request_review" and include the experiment path, validation result, and clear review prompt.
5. If review feedback arrives later, iterate on that same experiment instead of starting a new production feature. If the user explicitly accepts the experiment, call skynet_experiments with action "approve_to_backlog".
`
	case "experiment-to-backlog":
		roleRules = `Experiment transition flow:
1. Convert an accepted experiment into concrete backlog bullets.
2. Include implementation scope, files/modules likely involved, validation plan, and rollout notes.
3. Call skynet_backlog with action "add"; do not implement the feature in this role.
`
	case "qa-iterator":
		roleRules = `QA flow:
1. Call skynet_qa with action "next".
2. Verify the described behavior against the target repository or deployment.
3. If it passes, call skynet_qa with action "pass"; this queues PR composition work automatically.
4. If it fails, call skynet_qa with action "fail_to_backlog" and include exact reproduction steps and expected/actual behavior.
`
	case "pr-composer":
		roleRules = `PR composition flow:
1. Call skynet_pr with action "next".
2. If no item is returned, stop.
3. Read the QA evidence and inspect the target repository git status, branches, commits, worktrees, stashes, and relevant queue context.
4. Do not fail as missing work before checking current worktrees, git log --all for expected files, branch containment for referenced commits, git stash list, tracked stash diffs, and untracked stash parents such as refs/stash^3. Use git ls-tree -r refs/stash^3 and git show refs/stash^3:<path> when untracked artifacts may hold the completed work. If scoped artifacts are found in a stash, recover only those files into a clean PR branch and continue.
5. Build a coherent PR branch from the completed work. Prefer cherry-picking finished commits when they exist; otherwise stage and commit only files belonging to the passed QA scope. Do not include unrelated dirty files or revert user work.
6. Run focused verification for the PR contents.
7. Create a GitHub PR if existing auth/remotes allow it. If PR creation is blocked, leave a branch/commit plus PR title/body and the exact command or blocker.
8. PR body rules:
   - Never backslash-escape Markdown backtick characters in PR titles or bodies. Preserve inline code and fenced code blocks as normal GitHub Markdown.
   - Prefer writing the PR body to a temporary Markdown file and using gh pr create --body-file so shell quoting does not force Markdown escaping or command substitution.
   - For important architecture changes, including agent orchestration, data model, storage, auth, routing, workflow/CI, or cross-service/module flow, include an Architecture section with Before and After Mermaid diagrams.
   - Validate diagram grammar before publishing: choose a valid directive such as flowchart LR, graph TD, sequenceDiagram, stateDiagram-v2, or gantt; use simple alphanumeric/underscore node IDs; quote labels with punctuation; close brackets/arrows; add dateFormat for gantt; and keep sequence participants/messages syntactically valid. If unsure, use a simple flowchart LR.
   - For frontend feature PRs, render the affected route/component and capture visual evidence with a browser or Playwright snapshot/screenshot, or an equivalent rendered artifact. Include the artifact path/link, viewport, and any render caveat in the PR body.
9. Call skynet_pr with action "complete" and include branch, commits, files, verification, PR URL/body, architecture diagrams if required, and frontend snapshot evidence if applicable. Use "fail" only with concrete blocker evidence and the missing-work search checklist results.
`
	case "feedback-planner":
		roleRules = `Feedback planning flow:
1. Inspect the local deployment or target repo enough to understand the feedback.
2. Turn the feedback into precise backlog bullets with validation criteria.
3. Call skynet_backlog with action "add"; do not implement in this role.
`
	case "main-syncer":
		roleRules = `Main sync flow:
1. Call skynet_workflows with action "status" to confirm the target and deployment repositories.
2. Call skynet_main_sync with action "trigger", branch "main", start_web true, and dirty_policy "stash".
3. Treat statuses "already_current", "already_current_restarted", "synced_and_restarted", "synced_after_recovery_and_restarted", and "recovered_dirty_and_restarted" as successful outcomes.
4. If dirty_recovery is returned, include the stash reference in the report so a human can inspect or recover preserved local changes later.
5. Do not manually edit, checkout, reset, or delete deployment files. The tool owns deployment recovery.
`
	case "worktree-janitor":
		roleRules = `Worktree janitor flow:
1. Call skynet_worktree_cleanup with action "run", base_branch "main", and the configured target repository.
2. Do not remove files or directories manually. The cleanup tool only removes clean, unlocked worktrees whose HEAD is already contained in the base branch.
3. Report removed and skipped worktrees. Skipped dirty, locked, protected, unmerged, or detached worktrees are not failures.
`
	case "refactor-scout":
		roleRules = `Refactor scout flow:
1. Call skynet_workflows with action "status" to confirm the target repository. Search prior agent memory with memory_search for refactor, anti-pattern, code health, and the module names you are considering; use memory_get only for relevant hits.
2. Inspect active work before choosing scope: list skynet_change_requests in review, skynet_backlog pending items, skynet_experiments in pending/review, and skynet_qa pending items. Do not duplicate existing CRs, backlog, experiments, or QA failures.
3. Choose one bounded code slice per run, not the whole repo. Rank candidate slices by recent git update time, feature importance, churn, and coverage across frontend, backend, CLI, schema, tests, and workflow code over time. Keep the selected scope to roughly one module directory or 3-8 related files.
4. Read the selected files and nearby tests/contracts. Look for maintainability issues from big architecture to small conventions: duplicated flow, unclear ownership boundaries, expensive or fragile code paths, schema drift, frontend state/layout anti-patterns, backend error-handling gaps, CLI contract inconsistencies, missing tests, and patterns that future agents should avoid.
5. Submit each worthwhile suggestion through skynet_change_requests with action "submit", route "backlog", and category "refactor" or "technical_debt". Do not edit code, write direct backlog items, or create experiments from this role.
6. Each CR must include sampled scope, files inspected, anti-pattern evidence, why it matters, suggested refactor, expected validation, duplicate check, and a short convention note future agents should remember.
7. End with a concise memory-oriented summary of the sampled scope, recurring anti-pattern labels, submitted CR IDs, and areas intentionally skipped so later runs can avoid repeating the same review.
`
	case "erp-ux-walker":
		roleRules = `ERP UX walkthrough flow:
1. Before opening the website, call skynet_workflows status for the target repo/deployment settings, then inspect incoming work that may affect UI/UX: list skynet_change_requests in review, skynet_experiments in review/pending, skynet_backlog pending items with UI/UX wording, and skynet_qa pending UI-facing items. Use this to avoid duplicate CRs or conflicting product decisions.
2. Pick one ERP/package on the deployed website and play through it end-to-end with the browser. Evaluate actual interaction quality, copy, layout, responsiveness, accessibility cues, and whether system modules support the intended ERP flow.
3. When you find a gap, submit it through skynet_change_requests with action "submit"; use route "experiment" and category "ui_ux" for UI/UX questions, or route "backlog" and category "functional" for functional-only module/system gaps.
4. Do not write directly to skynet_experiments or skynet_backlog from this role. Human review of the RED change request must happen first.
5. Include ERP slug, deployment URL, observed steps, expected behavior, actual behavior, suggested route, and why this is not a duplicate of existing work.
`
	}

	return map[string]string{
		bootstrap.AgentsFile: fmt.Sprintf("# AGENTS.md - %s\n\n%s\n%s", spec.DisplayName, common, roleRules),
		bootstrap.IdentityFile: fmt.Sprintf(`# IDENTITY.md - %s

- Name: %s
- Purpose: %s
- Vibe: direct, scoped, and verification-first
`, spec.DisplayName, spec.DisplayName, spec.Frontmatter),
		bootstrap.SoulFile: `# SOUL.md - Operating Style

- Be concrete and evidence-led.
- Prefer small, reversible changes with clear verification.
- Do not invent completion; if verification is blocked, state the blocker and transition the item appropriately.
- Keep channel updates short and actionable.
`,
		bootstrap.UserPredefinedFile: common,
	}
}

type providerChoice struct {
	Name         string
	ProviderType string
}

func (f *SkynetWorkflowsFeature) pickProvider(ctx context.Context, providerKind string) providerChoice {
	if f.providerStore != nil {
		if providers, err := f.providerStore.ListProviders(ctx); err == nil {
			if choice := preferredProviderForKind(providers, providerKind); choice.Name != "" {
				return choice
			}
		}
	}
	if providerKind == storepkg.ProviderClaudeCLI {
		return providerChoice{Name: "claude-cli", ProviderType: storepkg.ProviderClaudeCLI}
	}
	if providerKind == storepkg.ProviderCodexCLI {
		if f.cfg != nil && strings.TrimSpace(f.cfg.Agents.Defaults.Provider) != "" {
			return providerChoice{Name: strings.TrimSpace(f.cfg.Agents.Defaults.Provider)}
		}
		return providerChoice{Name: "openai-codex", ProviderType: storepkg.ProviderChatGPTOAuth}
	}
	if f.cfg != nil && strings.TrimSpace(f.cfg.Agents.Defaults.Provider) != "" {
		return providerChoice{Name: strings.TrimSpace(f.cfg.Agents.Defaults.Provider)}
	}
	return providerChoice{Name: "openai"}
}

func preferredProviderForKind(providers []storepkg.LLMProviderData, providerKind string) providerChoice {
	type candidate struct {
		choice   providerChoice
		priority int
	}
	best := candidate{}
	for _, provider := range providers {
		if !provider.Enabled || strings.TrimSpace(provider.Name) == "" {
			continue
		}
		name := strings.ToLower(provider.Name)
		providerType := strings.TrimSpace(provider.ProviderType)
		priority := 100

		switch providerKind {
		case storepkg.ProviderClaudeCLI:
			if providerType == storepkg.ProviderClaudeCLI {
				if strings.EqualFold(provider.Name, providerNameSkynetClaudeCLI) {
					priority = 1
				} else if claudeProviderMCPBridgeDisabled(provider.Settings) {
					continue
				} else {
					priority = 10
					if strings.Contains(name, "skynet") {
						priority = 5
					}
				}
			}
		case storepkg.ProviderCodexCLI:
			switch providerType {
			case storepkg.ProviderChatGPTOAuth:
				priority = 10
			case storepkg.ProviderOpenAICompat:
				if strings.Contains(name, "codex") {
					priority = 12
				} else {
					priority = 20
				}
			case storepkg.ProviderCodexCLI:
				priority = 40
			}
			if strings.Contains(name, "skynet") {
				priority -= 5
			}
		}
		if priority >= 100 {
			continue
		}
		if best.choice.Name == "" || priority < best.priority {
			best = candidate{
				choice: providerChoice{
					Name:         strings.TrimSpace(provider.Name),
					ProviderType: providerType,
				},
				priority: priority,
			}
		}
	}
	return best.choice
}

type workflowCronSpec struct {
	Name     string
	AgentKey string
	EveryMS  int64
	Message  string
	Tool     string
	Args     map[string]any
}

func (f *SkynetWorkflowsFeature) workflowCronSpecs() []workflowCronSpec {
	return []workflowCronSpec{
		{
			Name:     "skynet backlog iterator",
			AgentKey: agentKeyBacklogIterator,
			EveryMS:  2 * 60 * 1000,
			Message:  `Run one Skynet backlog iteration. Call mcp__goclaw-bridge__skynet_backlog with action "next"; backlog claims are priority-aware and old low-priority items receive aging boosts. If no pending item exists, respond with "No pending backlog item." If an item is returned, inspect priority metadata and any "related_items"; if a nearby pending item should move up or down for the active feature/module, call mcp__goclaw-bridge__skynet_backlog with action "prioritize" and item_id/item_ids, priority, feature, and priority_reason. If a few nearby bullets are tightly related and safe to finish in one coherent change, call mcp__goclaw-bridge__skynet_backlog with action "claim_related" and item_ids before editing. Otherwise handle only the primary item. Validate readiness first. If the item is a rollup, milestone, stale, ambiguous, missing acceptance criteria, or too broad for one iteration, call mcp__goclaw-bridge__skynet_backlog with action "refine" and include concrete child bullets in "text"; prefix pure experiment-validation children with "Experiment leaf:" so they route to skynet_experiments; include priority/feature when child backlog items should inherit or override the parent priority; do not mark refinement as failure. If actionable, implement, run focused verification, write or update the repo-root QA report, and mark complete with item_id or item_ids. Completing backlog work queues QA automatically.`,
		},
		{
			Name:     "skynet experiment iterator",
			AgentKey: agentKeyExperimentIterator,
			EveryMS:  60 * 60 * 1000,
			Message:  `Run one Skynet experiment iteration. Call mcp__goclaw-bridge__skynet_experiments with action "next". This syncs repo experiments from apps/web/experiments when the queue is empty. If an item is returned, read its README/Mock/registry context, build or validate the experiment, then call mcp__goclaw-bridge__skynet_experiments with action "request_review". If no pending item exists, respond with "No pending experiment item."`,
		},
		{
			Name:    "skynet experiment review reminder",
			EveryMS: 5 * 60 * 1000,
			Tool:    "skynet_experiments",
			Args: map[string]any{
				"action": "review_reminders",
				"limit":  10,
			},
		},
		{
			Name:    "skynet change request review reminder",
			EveryMS: 5 * 60 * 1000,
			Tool:    "skynet_change_requests",
			Args: map[string]any{
				"action": "review_reminders",
				"limit":  10,
			},
		},
		{
			Name:     "skynet qa iterator",
			AgentKey: agentKeyQAIterator,
			EveryMS:  5 * 60 * 1000,
			Message:  `Run one Skynet QA iteration. Call mcp__goclaw-bridge__skynet_qa with action "next". If an item is returned, verify it. If it passes, call mcp__goclaw-bridge__skynet_qa with action "pass" so PR composition is queued; if it fails, call mcp__goclaw-bridge__skynet_qa with action "fail_to_backlog". If no pending item exists, respond with "No pending QA item."`,
		},
		{
			Name:     "skynet pr composer",
			AgentKey: agentKeyPRComposer,
			EveryMS:  10 * 60 * 1000,
			Message:  `Run one Skynet PR composition iteration. Call mcp__goclaw-bridge__skynet_pr with action "next". If an item is returned, inspect the target repository, including worktrees, branches, referenced commits, git stash list, tracked stash diffs, and untracked stash parents such as refs/stash^3. Do not fail as missing work until those locations have been checked; recover scoped artifacts from stash into a clean PR branch when found. Cherry-pick or stage only coherent post-QA changes into a PR branch, verify it, then create or prepare the PR. Never backslash-escape Markdown backtick characters in the PR title or body; prefer gh pr create --body-file from a temporary Markdown file. For important architecture PRs, include Before and After Mermaid diagrams and validate the diagram grammar before publishing. For frontend feature PRs, render the affected route/component and include snapshot/screenshot or equivalent visual evidence with viewport details. Then call mcp__goclaw-bridge__skynet_pr with action "complete" including branch, commits, files, verification, PR URL or PR-ready body, architecture diagrams when required, and frontend visual evidence when applicable. If no pending PR item exists, respond with "No pending PR item."`,
		},
		{
			Name:     "skynet main deployment sync",
			AgentKey: agentKeyMainSyncer,
			EveryMS:  15 * 60 * 1000,
			Message:  `Run one Skynet main deployment reconciliation tick. First call mcp__goclaw-bridge__skynet_workflows action "status" to confirm the configured target_repo, deploy_repo, and web_port. Then call mcp__goclaw-bridge__skynet_main_sync with action "trigger", branch "main", start_web true, and dirty_policy "stash". This is the fallback for missed or failed GitHub push-to-main webhooks. If the tool returns already_current, report that the deployed commit already matched and note the health value. If it returns already_current_restarted, report that the code was current but the web process needed recovery. If it returns dirty_recovery, include the stash reference. Do not manually edit, checkout, reset, delete, or start files/processes outside the tool.`,
		},
		{
			Name:     "skynet worktree cleanup",
			AgentKey: agentKeyWorktreeJanitor,
			EveryMS:  60 * 60 * 1000,
			Message:  `Run one Skynet worktree cleanup. Call mcp__goclaw-bridge__skynet_worktree_cleanup with action "run", base_branch "main", and remove_branches false. Do not manually delete files or directories. Report removed worktrees and skipped worktrees with reasons.`,
		},
		{
			Name:     "skynet refactor scout",
			AgentKey: agentKeyRefactorScout,
			EveryMS:  30 * 60 * 1000,
			Message:  `Run one scoped Skynet refactor scout pass. First call mcp__goclaw-bridge__skynet_workflows action "status", search agent memory for prior refactor or anti-pattern notes, and inspect existing review/pending work with skynet_change_requests, skynet_backlog, skynet_experiments, and skynet_qa so you do not duplicate suggestions. Pick one bounded code slice using recent git update time, feature importance, churn, and module coverage across frontend, backend, CLI, schema, tests, and workflow code; do not review the whole repo. Read the selected files and nearby tests/contracts, identify concrete maintainability refactors, and submit worthwhile suggestions only through mcp__goclaw-bridge__skynet_change_requests action "submit" with route "backlog" and category "refactor" or "technical_debt". Do not edit code or write direct backlog/experiment items. Include sampled scope, files inspected, anti-pattern evidence, suggested refactor, validation plan, duplicate check, and future convention note in each CR, then finish with a memory-oriented summary of scope and anti-pattern labels.`,
		},
		{
			Name:     "skynet erp ux walkthrough",
			AgentKey: agentKeyERPUXWalker,
			EveryMS:  24 * 60 * 60 * 1000,
			Message:  `Run one daily Skynet ERP UX walkthrough with Claude Opus 4.7 xhigh. First call mcp__goclaw-bridge__skynet_workflows action "status" for target repo and web_port, then inspect incoming UI/UX-affecting work so you do not duplicate decisions: call mcp__goclaw-bridge__skynet_change_requests list status "review", mcp__goclaw-bridge__skynet_experiments list, mcp__goclaw-bridge__skynet_backlog list status "pending" limit 50, and mcp__goclaw-bridge__skynet_qa list status "pending" limit 20. Then use the browser against the configured local deployment and play through one ERP end-to-end, evaluating UI, UX, copy, layout, responsiveness, accessibility cues, and module/system support gaps. For every gap, call mcp__goclaw-bridge__skynet_change_requests action "submit"; route UI/UX questions to "experiment" with category "ui_ux", and functional-only gaps to "backlog" with category "functional". Do not write directly to experiments or backlog; RED change-request review must precede both.`,
		},
	}
}

func (spec workflowCronSpec) payload() storepkg.CronPayload {
	if spec.Tool != "" {
		return storepkg.CronPayload{
			Kind: "tool_call",
			Tool: spec.Tool,
			Args: spec.Args,
		}
	}
	return storepkg.CronPayload{
		Kind:    "agent_turn",
		Message: spec.Message,
	}
}

func (f *SkynetWorkflowsFeature) ensureCronJobs(ctx context.Context) error {
	if f.cronStore == nil {
		return nil
	}
	agents, err := f.ensureAgents(ctx)
	if err != nil {
		return err
	}
	agentIDs := map[string]string{}
	for _, ag := range agents {
		agentIDs[ag.AgentKey] = ag.ID.String()
	}
	target := f.configuredOrigin(ctx)
	specs := f.workflowCronSpecs()

	jobs := f.cronStore.ListJobs(ctx, true, "", "")
	for _, spec := range specs {
		agentID := ""
		if spec.AgentKey != "" {
			agentID = agentIDs[spec.AgentKey]
		}
		if spec.AgentKey != "" && agentID == "" {
			continue
		}
		schedule := everySchedule(spec.EveryMS)
		// Keep scheduler ticks silent. Workers use the message/review tools for
		// meaningful channel updates; cron delivery would spam no-op responses.
		deliver := false
		deliverTo := ""
		if spec.Tool != "" {
			if target.LocalKey != "" {
				deliverTo = target.LocalKey
			} else {
				deliverTo = target.ChatID
			}
		}
		payload := spec.payload()

		var existing *storepkg.CronJob
		for i := range jobs {
			if strings.EqualFold(jobs[i].Name, spec.Name) {
				existing = &jobs[i]
				break
			}
		}
		if existing == nil {
			_, err := f.cronStore.AddJob(ctx, spec.Name, schedule, spec.Message, deliver, target.Channel, deliverTo, agentID, "")
			if err != nil {
				return err
			}
			jobs = f.cronStore.ListJobs(ctx, true, "", "")
			for i := range jobs {
				if strings.EqualFold(jobs[i].Name, spec.Name) {
					existing = &jobs[i]
					break
				}
			}
			if existing == nil {
				continue
			}
		}
		agentPatch := agentID
		if spec.Tool != "" {
			agentPatch = ""
		}
		_, err := f.cronStore.UpdateJob(ctx, existing.ID, storepkg.CronJobPatch{
			AgentID:        stringPtr(agentPatch),
			Schedule:       &schedule,
			Payload:        &payload,
			Message:        spec.Message,
			Enabled:        boolPtr(true),
			UserID:         stringPtr(""),
			Stateless:      boolPtr(true),
			Deliver:        boolPtr(deliver),
			DeliverChannel: stringPtr(target.Channel),
			DeliverTo:      stringPtr(deliverTo),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *SkynetWorkflowsFeature) ensureSkynetBotAccess(ctx context.Context) error {
	if f.agentStore == nil {
		return nil
	}
	existing, err := f.agentStore.GetByKey(ctx, agentKeySkynet)
	if err != nil && !isNotFoundError(err) {
		return err
	}
	if existing == nil {
		provider := f.pickProvider(ctx, storepkg.ProviderCodexCLI)
		agentData := &storepkg.AgentData{
			TenantID:            storepkg.MasterTenantID,
			AgentKey:            agentKeySkynet,
			DisplayName:         "Skynet",
			Frontmatter:         "Skynet orchestration bot for CI failures, PR conflicts, backlog, experiments, change requests, QA, PR composition, and feedback planning.",
			OwnerID:             "system",
			Provider:            provider.Name,
			Model:               modelCodex54,
			ContextWindow:       config.DefaultContextWindow,
			MaxToolIterations:   config.DefaultMaxIterations,
			Workspace:           f.resolveTargetRepo(ctx),
			RestrictToWorkspace: true,
			AgentType:           storepkg.AgentTypePredefined,
			Status:              storepkg.AgentStatusActive,
			ToolsConfig: mustJSON(map[string]any{
				"profile":   "messaging",
				"alsoAllow": uniqueSorted(append(skynetWorkflowTools, "message")),
			}),
			OtherConfig: workflowOtherConfig(workflowAgentSpec{
				Role:            "orchestrator",
				ReasoningEffort: reasoningHigh,
			}),
		}
		if err := f.agentStore.Create(ctx, agentData); err != nil {
			return err
		}
		if _, err := bootstrap.SeedToStore(ctx, f.agentStore, agentData.ID, storepkg.AgentTypePredefined); err != nil {
			return err
		}
		existing = agentData
	}

	for _, agentKey := range []string{agentKeySkynet, agentKeyBuilderBot} {
		agentData, err := f.agentStore.GetByKey(ctx, agentKey)
		if err != nil {
			if isNotFoundError(err) {
				continue
			}
			return err
		}
		if err := f.grantSkynetWorkflowAccess(ctx, agentData); err != nil {
			return err
		}
	}
	return nil
}

func (f *SkynetWorkflowsFeature) grantSkynetWorkflowAccess(ctx context.Context, agentData *storepkg.AgentData) error {
	if agentData == nil {
		return nil
	}
	merged, changed, err := mergeToolsConfig(agentData.ToolsConfig, append(skynetWorkflowTools, "message"))
	if err != nil {
		return err
	}
	updates := map[string]any{}
	if changed {
		updates["tools_config"] = merged
	}
	if strings.TrimSpace(agentData.Status) != storepkg.AgentStatusActive {
		updates["status"] = storepkg.AgentStatusActive
	}
	if len(updates) > 0 {
		if err := f.agentStore.Update(ctx, agentData.ID, updates); err != nil {
			return err
		}
	}
	content := fmt.Sprintf(`# AGENTS.md - Skynet Workflow Access

You can manage these Skynet workflow tools:
- skynet_workflows: configure/status for the target Telegram channel and repository.
- skynet_ci_failure: dispatch CI/CD failure repair work to %s.
- skynet_pr_conflict: dispatch PR merge-conflict resolution work to %s.
- skynet_main_sync: reconcile the local main deployment worktree to origin/main, preserve dirty drift with dirty_policy "stash", and restart the web app when requested.
- skynet_backlog: add/list/claim/prioritize/refine/complete implementation backlog items; higher priority is claimed first and old low-priority items receive aging boosts; complete queues QA.
- skynet_experiments: add/list/claim/request review/transition accepted experiments.
- skynet_change_requests: submit/list/review RED change requests; accepted CRs route to experiments or backlog only after human review.
- skynet_qa: add/list/claim/pass/fail QA items; pass queues PR composition.
- skynet_pr: add/list/claim/complete/fail post-QA PR composition items.
- skynet_feedback_plan: turn user feedback into backlog items through %s.
- skynet_worktree_cleanup: clean finished git worktrees through the safe cleanup tool.

When users post CI failures, PR conflicts, main deployment refresh requests, feedback, backlog bullets, experiment bullets, change requests, worktree cleanup requests, or QA bullets, call the matching tool instead of only replying in prose.
`, agentKeyCIFixer, agentKeyPRConflictResolver, agentKeyFeedbackPlanner)
	return f.agentStore.SetAgentContextFile(ctx, agentData.ID, "SKYNET_WORKFLOWS.md", content)
}

func mergeToolsConfig(raw json.RawMessage, toolNames []string) (json.RawMessage, bool, error) {
	var spec config.ToolPolicySpec
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &spec); err != nil {
			return nil, false, err
		}
	}
	changed := false
	for _, toolName := range uniqueSorted(toolNames) {
		if !containsString(spec.AlsoAllow, toolName) {
			spec.AlsoAllow = append(spec.AlsoAllow, toolName)
			changed = true
		}
	}
	if spec.Profile == "" {
		spec.Profile = "coding"
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	spec.AlsoAllow = uniqueSorted(spec.AlsoAllow)
	encoded, err := json.Marshal(spec)
	return encoded, true, err
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
