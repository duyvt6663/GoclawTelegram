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

	agentKeySkynet             = "skynet"
	agentKeyBuilderBot         = "builder-bot"
	agentKeyCIFixer            = "skynet-ci-fixer"
	agentKeyBacklogIterator    = "skynet-backlog-iterator"
	agentKeyExperimentIterator = "skynet-experiment-iterator"
	agentKeyExperimentBacklog  = "skynet-experiment-to-backlog"
	agentKeyQAIterator         = "skynet-qa-iterator"
	agentKeyPRComposer         = "skynet-pr-composer"
	agentKeyFeedbackPlanner    = "skynet-feedback-planner"

	providerNameSkynetClaudeCLI = "skynet-claude-cli"

	modelClaudeSonnet = "sonnet"
	modelClaudeOpus47 = "claude-opus-4-7"
	modelCodex54      = "gpt-5.4"
	reasoningHigh     = "high"
)

var skynetWorkflowTools = []string{
	"skynet_workflows",
	"skynet_backlog",
	"skynet_experiments",
	"skynet_qa",
	"skynet_pr",
	"skynet_ci_failure",
	"skynet_feedback_plan",
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
		deps.ToolRegistry.Register(&ciFailureTool{feature: f})
		deps.ToolRegistry.Register(&feedbackPlanTool{feature: f})
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

func (f *SkynetWorkflowsFeature) workflowAgentSpecs(ctx context.Context) []workflowAgentSpec {
	targetRepo := f.resolveTargetRepo(ctx)
	codingTools := append([]string{}, skynetWorkflowTools...)
	codingTools = append(codingTools, "message")
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
- %s for skynet_feedback_plan
`, mcpToolName("skynet_backlog"), mcpToolName("skynet_experiments"), mcpToolName("skynet_qa"), mcpToolName("skynet_pr"), mcpToolName("skynet_ci_failure"), mcpToolName("skynet_feedback_plan"))
}

func (f *SkynetWorkflowsFeature) contextFilesForSpec(spec workflowAgentSpec, workspace string) map[string]string {
	targetRepo := f.resolveTargetRepo(context.Background())
	common := fmt.Sprintf(`Target repository: %s
Workspace: %s
Managed workflow feature: %s

Tools:
- Use skynet_backlog for implementation queue items.
- Use skynet_experiments for experiment queue items and channel review transitions.
- Use skynet_qa for QA queue items and QA-to-backlog transitions.
- Use skynet_pr for post-QA PR composition and completion tracking.
- Use skynet_ci_failure only for CI/CD failure intake.
- Use skynet_feedback_plan only for user feedback planning intake.

%s

Operational rules:
- Claim one queue item at a time.
- Validate unclear requests before broad edits; implement only when the item is actionable.
- Do not treat planning/decomposition as implementation failure. If a backlog item is a milestone, rollup, stale note, or lacks acceptance criteria, use the queue's refinement transition and create implementation-sized child bullets.
- Keep changes scoped to the target repository and verify with the local test/build commands you can run.
- Update the queue item status with the matching Skynet tool before ending the run.
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
	case "backlog-iterator":
		roleRules = `Backlog flow:
1. Call skynet_backlog with action "next".
2. If no item is returned, stop.
3. Validate readiness by reading linked docs, experiments/archive notes, prior QA, and current code paths.
4. If the item is a broad rollup, milestone, stale, missing acceptance criteria, or needs an experiment/spike first, call skynet_backlog with action "refine". Put concrete implementation-sized child bullets in "text" and explain the dependency/order in "result". Do not use "fail" for refinement.
5. If the item is actionable, implement, run focused verification, write or update the repo-root QA report, then call skynet_backlog with action "complete" or "fail". The complete action queues QA automatically; use "fail" only for an attempted implementation that cannot be completed safely.
`
	case "experiment-iterator":
		roleRules = `Experiment flow:
1. Call skynet_experiments with action "next".
2. Build or update the sandbox experiment, validate interaction manually or with tests, and do not promote production code unless the item explicitly asks for promotion.
3. Call skynet_experiments with action "request_review" and include the experiment path, validation result, and clear review prompt.
4. If review feedback arrives later, iterate on that same experiment instead of starting a new production feature.
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
3. Read the QA evidence and inspect the target repository git status, branches, commits, and relevant queue context.
4. Build a coherent PR branch from the completed work. Prefer cherry-picking finished commits when they exist; otherwise stage and commit only files belonging to the passed QA scope. Do not include unrelated dirty files or revert user work.
5. Run focused verification for the PR contents.
6. Create a GitHub PR if existing auth/remotes allow it. If PR creation is blocked, leave a branch/commit plus PR title/body and the exact command or blocker.
7. Call skynet_pr with action "complete" and include branch, commits, files, verification, and PR URL/body. Use "fail" only with concrete blocker evidence.
`
	case "feedback-planner":
		roleRules = `Feedback planning flow:
1. Inspect the local deployment or target repo enough to understand the feedback.
2. Turn the feedback into precise backlog bullets with validation criteria.
3. Call skynet_backlog with action "add"; do not implement in this role.
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

	specs := []struct {
		Name     string
		AgentKey string
		EveryMS  int64
		Message  string
	}{
		{
			Name:     "skynet backlog iterator",
			AgentKey: agentKeyBacklogIterator,
			EveryMS:  2 * 60 * 1000,
			Message:  `Run one Skynet backlog iteration. Call mcp__goclaw-bridge__skynet_backlog with action "next". If no pending item exists, respond with "No pending backlog item." If an item is returned, validate readiness first. If it is a rollup, milestone, stale, ambiguous, missing acceptance criteria, or too broad for one iteration, call mcp__goclaw-bridge__skynet_backlog with action "refine" and include concrete child bullets in "text"; do not mark refinement as failure. If it is actionable, implement it, run focused verification, write or update the repo-root QA report, and mark it complete or failed with mcp__goclaw-bridge__skynet_backlog. Completing a backlog item queues QA automatically.`,
		},
		{
			Name:     "skynet experiment iterator",
			AgentKey: agentKeyExperimentIterator,
			EveryMS:  60 * 60 * 1000,
			Message:  `Run one Skynet experiment iteration. Call mcp__goclaw-bridge__skynet_experiments with action "next". If an item is returned, build or validate the experiment, then call mcp__goclaw-bridge__skynet_experiments with action "request_review". If no pending item exists, respond with "No pending experiment item."`,
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
			Message:  `Run one Skynet PR composition iteration. Call mcp__goclaw-bridge__skynet_pr with action "next". If an item is returned, inspect the target repository, cherry-pick or stage only coherent post-QA changes into a PR branch, verify it, then call mcp__goclaw-bridge__skynet_pr with action "complete" including branch, commits, files, verification, and PR URL or PR-ready body. If no pending PR item exists, respond with "No pending PR item."`,
		},
	}

	jobs := f.cronStore.ListJobs(ctx, true, "", "")
	for _, spec := range specs {
		agentID := agentIDs[spec.AgentKey]
		if agentID == "" {
			continue
		}
		schedule := everySchedule(spec.EveryMS)
		// Keep scheduler ticks silent. Workers use the message/review tools for
		// meaningful channel updates; cron delivery would spam no-op responses.
		deliver := false
		deliverTo := ""

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
			continue
		}
		_, err := f.cronStore.UpdateJob(ctx, existing.ID, storepkg.CronJobPatch{
			AgentID:        stringPtr(agentID),
			Schedule:       &schedule,
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
			Frontmatter:         "Skynet orchestration bot for CI failures, backlog, experiments, QA, PR composition, and feedback planning.",
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
- skynet_backlog: add/list/claim/refine/complete implementation backlog items; complete queues QA.
- skynet_experiments: add/list/claim/request review/transition accepted experiments.
- skynet_qa: add/list/claim/pass/fail QA items; pass queues PR composition.
- skynet_pr: add/list/claim/complete/fail post-QA PR composition items.
- skynet_feedback_plan: turn user feedback into backlog items through %s.

When users post CI failures, feedback, backlog bullets, experiment bullets, or QA bullets, call the matching tool instead of only replying in prose.
`, agentKeyCIFixer, agentKeyFeedbackPlanner)
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
