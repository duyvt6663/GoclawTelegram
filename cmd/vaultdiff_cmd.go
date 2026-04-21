package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/nextlevelbuilder/goclaw/internal/bootstrap"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

const (
	vaultDiffDefaultWorkspace      = "/Users/duyvt6663/github/VaultDiff"
	vaultDiffCodexProviderName     = "vaultdiff-codex-cli"
	vaultDiffClaudeProviderName    = "vaultdiff-claude-cli"
	vaultDiffCodexAgentKey         = "vaultdiff-codex"
	vaultDiffClaudeAgentKey        = "vaultdiff-claude"
	vaultDiffCodexChannelName      = "vaultdiff-codex-telegram"
	vaultDiffClaudeChannelName     = "vaultdiff-claude-telegram"
	vaultDiffSetupCreatedBy        = "system:vaultdiff-telegram-setup"
	vaultDiffClaudeModel           = "claude-opus-4-7"
	vaultDiffCodexModel            = "gpt-5.4"
	vaultDiffCodexReasoningEffort  = "xhigh"
	vaultDiffClaudeReasoningEffort = "max"
)

type vaultDiffTelegramSetupOptions struct {
	workspace    string
	codexToken   string
	claudeToken  string
	codexBinary  string
	claudeBinary string
}

type telegramBotIdentity struct {
	ID        int64
	Username  string
	FirstName string
}

func vaultDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vaultdiff",
		Short: "VaultDiff-specific setup helpers",
	}
	cmd.AddCommand(vaultDiffTelegramCmd())
	return cmd
}

func vaultDiffTelegramCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telegram",
		Short: "Provision VaultDiff Telegram agents",
	}
	cmd.AddCommand(vaultDiffTelegramSetupCmd())
	return cmd
}

func vaultDiffTelegramSetupCmd() *cobra.Command {
	opts := vaultDiffTelegramSetupOptions{
		workspace:    vaultDiffDefaultWorkspace,
		codexBinary:  "codex",
		claudeBinary: "claude",
	}

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Create VaultDiff Codex/Claude Telegram bots backed by local CLIs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultDiffTelegramSetup(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.workspace, "workspace", opts.workspace, "VaultDiff workspace root")
	cmd.Flags().StringVar(&opts.codexToken, "codex-token", "", "Telegram bot token for the Codex bot")
	cmd.Flags().StringVar(&opts.claudeToken, "claude-token", "", "Telegram bot token for the Claude bot")
	cmd.Flags().StringVar(&opts.codexBinary, "codex-binary", opts.codexBinary, "codex CLI binary or absolute path")
	cmd.Flags().StringVar(&opts.claudeBinary, "claude-binary", opts.claudeBinary, "claude CLI binary or absolute path")
	_ = cmd.MarkFlagRequired("codex-token")
	_ = cmd.MarkFlagRequired("claude-token")
	return cmd
}

func runVaultDiffTelegramSetup(cmd *cobra.Command, opts vaultDiffTelegramSetupOptions) error {
	workspace, err := filepath.Abs(config.ExpandHome(strings.TrimSpace(opts.workspace)))
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	if st, err := os.Stat(workspace); err != nil || !st.IsDir() {
		return fmt.Errorf("workspace not found: %s", workspace)
	}

	cfg, err := config.Load(resolveConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	stores, cleanup, err := openVaultDiffStores(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := store.WithTenantID(context.Background(), store.MasterTenantID)

	codexPath, err := exec.LookPath(strings.TrimSpace(opts.codexBinary))
	if err != nil {
		return fmt.Errorf("resolve codex binary: %w", err)
	}
	claudePath, err := exec.LookPath(strings.TrimSpace(opts.claudeBinary))
	if err != nil {
		return fmt.Errorf("resolve claude binary: %w", err)
	}

	verifyCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := verifyCodexCLI(verifyCtx, codexPath, workspace); err != nil {
		return err
	}
	if err := verifyClaudeCLI(verifyCtx, claudePath); err != nil {
		return err
	}

	httpClient := &http.Client{Timeout: 15 * time.Second}
	codexBot, err := telegramGetMe(ctx, httpClient, opts.codexToken)
	if err != nil {
		return fmt.Errorf("verify codex Telegram bot: %w", err)
	}
	claudeBot, err := telegramGetMe(ctx, httpClient, opts.claudeToken)
	if err != nil {
		return fmt.Errorf("verify claude Telegram bot: %w", err)
	}

	claudeSessionDir := filepath.Join(workspace, ".goclaw-telegram", "claude-sessions")
	if err := os.MkdirAll(claudeSessionDir, 0755); err != nil {
		return fmt.Errorf("create Claude session dir: %w", err)
	}
	claudeAddDirs := []string{}
	if homeDir, err := os.UserHomeDir(); err == nil {
		claudeAddDirs = append(claudeAddDirs, filepath.Join(homeDir, ".claude"))
	}

	codexProv := &store.LLMProviderData{
		Name:         vaultDiffCodexProviderName,
		DisplayName:  "VaultDiff Codex CLI",
		ProviderType: store.ProviderCodexCLI,
		APIBase:      codexPath,
		Enabled:      true,
		Settings: mustJSON(map[string]any{
			"model":            vaultDiffCodexModel,
			"reasoning_effort": vaultDiffCodexReasoningEffort,
			"work_dir":         workspace,
			"sandbox_mode":     "workspace-write",
			"approval_policy":  "never",
		}),
	}
	if err := stores.Providers.CreateProvider(ctx, codexProv); err != nil {
		return fmt.Errorf("upsert codex provider: %w", err)
	}

	claudeProv := &store.LLMProviderData{
		Name:         vaultDiffClaudeProviderName,
		DisplayName:  "VaultDiff Claude CLI",
		ProviderType: store.ProviderClaudeCLI,
		APIBase:      claudePath,
		Enabled:      true,
		Settings: mustJSON(map[string]any{
			"model":          vaultDiffClaudeModel,
			"effort":         vaultDiffClaudeReasoningEffort,
			"base_work_dir":  claudeSessionDir,
			"workspace_root": workspace,
			"perm_mode":      "bypassPermissions",
			"add_dirs":       claudeAddDirs,
			"use_mcp_bridge": false,
		}),
	}
	if err := stores.Providers.CreateProvider(ctx, claudeProv); err != nil {
		return fmt.Errorf("upsert claude provider: %w", err)
	}

	codexAgentID, err := upsertVaultDiffAgent(ctx, stores.Agents, &store.AgentData{
		TenantID:            store.MasterTenantID,
		AgentKey:            vaultDiffCodexAgentKey,
		DisplayName:         "Vault Codex",
		Frontmatter:         "Architecture and review-oriented software engineer for VaultDiff. Strong at design tradeoffs, guarded-vault policy, risk spotting, and protocol clarity.",
		OwnerID:             vaultDiffSetupCreatedBy,
		Provider:            vaultDiffCodexProviderName,
		Model:               vaultDiffCodexModel,
		ContextWindow:       config.DefaultContextWindow,
		MaxToolIterations:   config.DefaultMaxIterations,
		Workspace:           workspace,
		RestrictToWorkspace: true,
		AgentType:           store.AgentTypePredefined,
		Status:              store.AgentStatusActive,
		ToolsConfig: mustJSON(map[string]any{
			"deny": []string{"group:goclaw"},
		}),
		OtherConfig: mustJSON(map[string]any{
			"reasoning": map[string]any{
				"effort":   vaultDiffCodexReasoningEffort,
				"fallback": "provider_default",
			},
			"workspace_sharing": map[string]any{
				"shared_dm":    true,
				"shared_group": true,
			},
		}),
	})
	if err != nil {
		return fmt.Errorf("upsert codex agent: %w", err)
	}

	claudeAgentID, err := upsertVaultDiffAgent(ctx, stores.Agents, &store.AgentData{
		TenantID:            store.MasterTenantID,
		AgentKey:            vaultDiffClaudeAgentKey,
		DisplayName:         "Vault Claude",
		Frontmatter:         "Implementation-first software engineer for VaultDiff. Strong at patches, protocol surfaces, testing, and guarded-vault workflows.",
		OwnerID:             vaultDiffSetupCreatedBy,
		Provider:            vaultDiffClaudeProviderName,
		Model:               vaultDiffClaudeModel,
		ContextWindow:       config.DefaultContextWindow,
		MaxToolIterations:   config.DefaultMaxIterations,
		Workspace:           workspace,
		RestrictToWorkspace: true,
		AgentType:           store.AgentTypePredefined,
		Status:              store.AgentStatusActive,
		ToolsConfig: mustJSON(map[string]any{
			"deny": []string{"group:goclaw"},
		}),
		OtherConfig: mustJSON(map[string]any{
			"workspace_sharing": map[string]any{
				"shared_dm":    true,
				"shared_group": true,
			},
		}),
	})
	if err != nil {
		return fmt.Errorf("upsert claude agent: %w", err)
	}

	if err := seedVaultDiffAgentContext(ctx, stores.Agents, codexAgentID, buildVaultDiffCodexContext(workspace, codexBot.Username, claudeBot.Username)); err != nil {
		return fmt.Errorf("seed codex context: %w", err)
	}
	if err := seedVaultDiffAgentContext(ctx, stores.Agents, claudeAgentID, buildVaultDiffClaudeContext(workspace, codexBot.Username, claudeBot.Username)); err != nil {
		return fmt.Errorf("seed claude context: %w", err)
	}

	if err := upsertTelegramInstance(ctx, stores.ChannelInstances, &store.ChannelInstanceData{
		Name:        vaultDiffCodexChannelName,
		DisplayName: fmt.Sprintf("Vault Codex Telegram (@%s)", codexBot.Username),
		ChannelType: "telegram",
		AgentID:     codexAgentID,
		Credentials: mustJSON(map[string]any{"token": opts.codexToken}),
		Config:      vaultDiffTelegramConfig(),
		Enabled:     true,
		CreatedBy:   vaultDiffSetupCreatedBy,
	}); err != nil {
		return fmt.Errorf("upsert codex Telegram instance: %w", err)
	}

	if err := upsertTelegramInstance(ctx, stores.ChannelInstances, &store.ChannelInstanceData{
		Name:        vaultDiffClaudeChannelName,
		DisplayName: fmt.Sprintf("Vault Claude Telegram (@%s)", claudeBot.Username),
		ChannelType: "telegram",
		AgentID:     claudeAgentID,
		Credentials: mustJSON(map[string]any{"token": opts.claudeToken}),
		Config:      vaultDiffTelegramConfig(),
		Enabled:     true,
		CreatedBy:   vaultDiffSetupCreatedBy,
	}); err != nil {
		return fmt.Errorf("upsert claude Telegram instance: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "VaultDiff Telegram provisioning complete.\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Workspace: %s\n", workspace)
	fmt.Fprintf(cmd.OutOrStdout(), "Providers: %s, %s\n", vaultDiffCodexProviderName, vaultDiffClaudeProviderName)
	fmt.Fprintf(cmd.OutOrStdout(), "Agents: %s, %s\n", vaultDiffCodexAgentKey, vaultDiffClaudeAgentKey)
	fmt.Fprintf(cmd.OutOrStdout(), "Bots: @%s, @%s\n", codexBot.Username, claudeBot.Username)
	fmt.Fprintf(cmd.OutOrStdout(), "Channel instances: %s, %s\n", vaultDiffCodexChannelName, vaultDiffClaudeChannelName)
	fmt.Fprintf(cmd.OutOrStdout(), "Next: add both bots to the same Telegram group and address one of them with an @mention. They will collaborate visibly by mentioning the peer bot in-thread.\n")
	fmt.Fprintf(cmd.OutOrStdout(), "If the gateway is already running, restart it once so the new providers and channel instances are loaded.\n")
	return nil
}

func openVaultDiffStores(cfg *config.Config) (*store.Stores, func(), error) {
	if cfg.Database.PostgresDSN == "" {
		return nil, nil, fmt.Errorf("GOCLAW_POSTGRES_DSN is required; source .env.local or export it before running this command")
	}
	if err := checkSchemaOrAutoUpgrade(cfg.Database.PostgresDSN); err != nil {
		return nil, nil, fmt.Errorf("schema check failed: %w", err)
	}
	stores, err := pg.NewPGStores(store.StoreConfig{
		PostgresDSN:      cfg.Database.PostgresDSN,
		EncryptionKey:    os.Getenv("GOCLAW_ENCRYPTION_KEY"),
		SkillsStorageDir: filepath.Join(config.ResolvedDataDirFromEnv(), "skills-store"),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("open postgres stores: %w", err)
	}
	cleanup := func() {
		if stores != nil && stores.DB != nil {
			_ = stores.DB.Close()
		}
	}
	return stores, cleanup, nil
}

func verifyCodexCLI(ctx context.Context, cliPath, workspace string) error {
	statusCmd := exec.CommandContext(ctx, cliPath, "login", "status")
	statusOut, err := statusCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("codex login status failed: %w", err)
	}
	if !strings.Contains(strings.ToLower(string(statusOut)), "logged in") {
		return fmt.Errorf("codex CLI is not logged in")
	}

	probeCmd := exec.CommandContext(ctx, cliPath,
		"-C", workspace,
		"-s", "workspace-write",
		"-a", "never",
		"exec",
		"--json",
		"-m", vaultDiffCodexModel,
		"-c", fmt.Sprintf(`model_reasoning_effort="%s"`, vaultDiffCodexReasoningEffort),
		"Reply with OK only.",
	)
	probeOut, err := probeCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("codex CLI probe failed: %w", err)
	}
	if !strings.Contains(string(probeOut), `"text":"OK"`) {
		return fmt.Errorf("codex CLI probe did not return OK")
	}
	return nil
}

func verifyClaudeCLI(ctx context.Context, cliPath string) error {
	statusCmd := exec.CommandContext(ctx, cliPath, "auth", "status", "--json")
	statusOut, err := statusCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("claude auth status failed: %w", err)
	}
	var status struct {
		LoggedIn         bool   `json:"loggedIn"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if err := json.Unmarshal(statusOut, &status); err != nil {
		return fmt.Errorf("parse claude auth status: %w", err)
	}
	if !status.LoggedIn {
		return fmt.Errorf("claude CLI is not logged in")
	}

	probeCmd := exec.CommandContext(ctx, cliPath,
		"-p",
		"--output-format", "json",
		"--model", vaultDiffClaudeModel,
		"--effort", vaultDiffClaudeReasoningEffort,
		"Reply with OK only.",
	)
	probeOut, err := probeCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("claude CLI probe failed: %w", err)
	}
	if !strings.Contains(string(probeOut), `"result":"OK"`) {
		return fmt.Errorf("claude CLI probe did not return OK")
	}
	return nil
}

func telegramGetMe(ctx context.Context, client *http.Client, token string) (telegramBotIdentity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://api.telegram.org/bot%s/getMe", token), nil)
	if err != nil {
		return telegramBotIdentity{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return telegramBotIdentity{}, err
	}
	defer resp.Body.Close()

	var payload struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			ID        int64  `json:"id"`
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return telegramBotIdentity{}, err
	}
	if !payload.OK || payload.Result.Username == "" {
		if payload.Description == "" {
			payload.Description = "unknown Telegram API error"
		}
		return telegramBotIdentity{}, fmt.Errorf("%s", payload.Description)
	}
	return telegramBotIdentity{
		ID:        payload.Result.ID,
		Username:  payload.Result.Username,
		FirstName: payload.Result.FirstName,
	}, nil
}

func upsertVaultDiffAgent(ctx context.Context, agents store.AgentStore, desired *store.AgentData) (uuid.UUID, error) {
	if len(desired.ToolsConfig) == 0 {
		desired.ToolsConfig = mustJSON(map[string]any{})
	}
	if len(desired.OtherConfig) == 0 {
		desired.OtherConfig = mustJSON(map[string]any{})
	}
	existing, err := agents.GetByKey(ctx, desired.AgentKey)
	if err == nil && existing != nil {
		if err := agents.Update(ctx, existing.ID, map[string]any{
			"display_name":          desired.DisplayName,
			"frontmatter":           desired.Frontmatter,
			"owner_id":              desired.OwnerID,
			"provider":              desired.Provider,
			"model":                 desired.Model,
			"context_window":        desired.ContextWindow,
			"max_tool_iterations":   desired.MaxToolIterations,
			"workspace":             desired.Workspace,
			"restrict_to_workspace": desired.RestrictToWorkspace,
			"agent_type":            desired.AgentType,
			"status":                desired.Status,
			"tools_config":          desired.ToolsConfig,
			"other_config":          desired.OtherConfig,
		}); err != nil {
			return uuid.Nil, err
		}
		return existing.ID, nil
	}
	if err := agents.Create(ctx, desired); err != nil {
		return uuid.Nil, err
	}
	return desired.ID, nil
}

func seedVaultDiffAgentContext(ctx context.Context, agents store.AgentStore, agentID uuid.UUID, files map[string]string) error {
	for name, content := range files {
		if err := agents.SetAgentContextFile(ctx, agentID, name, content); err != nil {
			return err
		}
	}
	return nil
}

func upsertTelegramInstance(ctx context.Context, channelStore store.ChannelInstanceStore, inst *store.ChannelInstanceData) error {
	existing, err := channelStore.GetByName(ctx, inst.Name)
	if err == nil && existing != nil {
		return channelStore.Update(ctx, existing.ID, map[string]any{
			"display_name": inst.DisplayName,
			"agent_id":     inst.AgentID,
			"credentials":  json.RawMessage(inst.Credentials),
			"config":       inst.Config,
			"enabled":      inst.Enabled,
		})
	}
	return channelStore.Create(ctx, inst)
}

func vaultDiffTelegramConfig() json.RawMessage {
	requireMention := true
	return mustJSON(map[string]any{
		"dm_policy":        "pairing",
		"group_policy":     "open",
		"require_mention":  requireMention,
		"mention_mode":     "strict",
		"history_limit":    30,
		"reasoning_stream": false,
	})
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func buildVaultDiffCodexContext(workspace, codexUsername, claudeUsername string) map[string]string {
	return map[string]string{
		bootstrap.AgentsFile: strings.Join([]string{
			"# AGENTS.md - VaultDiff Codex Telegram Workflow",
			"",
			fmt.Sprintf("You are the Codex-side engineer for VaultDiff, running from %q.", workspace),
			"",
			"Rules:",
			"- Operate only on the VaultDiff workspace, never on the GoClaw repo.",
			"- Before answering architecture or codebase questions, read graphify-out/GRAPH_REPORT.md at the VaultDiff root.",
			"- Respect guarded vault boundaries:",
			"  - never read or modify codex-v2v/private/ directly",
			"  - never read claude-v2v/vault/graph.json directly",
			"  - prefer codex-v2v/protocol/, root docs, READMEs, and the documented CLIs",
			"- Treat codex-v2v/protocol/ as Codex's peer-facing surface and the vaultdiff CLI as Claude's sanctioned interface.",
			"- In Telegram groups, respond only when directly addressed, replied to, or explicitly mentioned.",
			fmt.Sprintf("- For visible collaboration, summon Claude by mentioning @%s with one concrete implementation, patch, or verification request.", claudeUsername),
			"- Keep cross-bot loops short. After at most 2 back-and-forth messages with Claude, summarize the conclusion or recommended next step to the human.",
			"- Keep replies professional, concise, and software-engineering focused.",
			"",
		}, "\n"),
		bootstrap.SoulFile: `# SOUL.md - Who You Are

## Core stance
You are a professional software engineer with an architecture-and-review bias.
You care about boundaries, invariants, policy, provenance, and maintainable
interfaces.

## Voice
Be direct, calm, and technically grounded. Explain tradeoffs precisely. Avoid
fluff and avoid vague design talk that cannot be operationalized.

## Collaboration style
Use Claude as your implementation peer. Ask for concrete edits, tests, or
verification steps when that would move the work forward faster.

## Decision rule
Prefer designs that preserve VaultDiff's guarded-vault model, visible review
flow, and explicit provenance over shortcuts that blur authority boundaries.
`,
		bootstrap.IdentityFile: strings.Join([]string{
			"# IDENTITY.md - Who Am I?",
			"",
			"Name: Vault Codex",
			"Channel persona: Telegram engineering bot",
			"Primary role: Architecture and review engineer for VaultDiff",
			fmt.Sprintf("Workspace: %s", workspace),
			fmt.Sprintf("Peer engineer: @%s", claudeUsername),
			fmt.Sprintf("Public bot username: @%s", codexUsername),
			"",
		}, "\n"),
		bootstrap.UserPredefinedFile: strings.Join([]string{
			"# USER_PREDEFINED.md",
			"",
			"Default audience: the VaultDiff operator and collaborators in Telegram.",
			"",
			"Interaction rules:",
			"- When a human asks for help, answer as a senior software engineer.",
			fmt.Sprintf("- When the task benefits from implementation help or code verification, mention @%s so the collaboration stays visible in the Telegram channel.", claudeUsername),
			"- When Claude answers, integrate that feedback instead of repeating it.",
			"- Do not narrate hidden internal state. The visible channel conversation is the collaboration surface.",
			"",
		}, "\n"),
	}
}

func buildVaultDiffClaudeContext(workspace, codexUsername, claudeUsername string) map[string]string {
	return map[string]string{
		bootstrap.AgentsFile: strings.Join([]string{
			"# AGENTS.md - VaultDiff Claude Telegram Workflow",
			"",
			fmt.Sprintf("You are the Claude-side engineer for VaultDiff, running from %q.", workspace),
			"",
			"Rules:",
			"- Operate only on the VaultDiff workspace, never on the GoClaw repo.",
			"- Before answering architecture or codebase questions, read graphify-out/GRAPH_REPORT.md at the VaultDiff root.",
			"- Respect guarded vault boundaries:",
			"  - never read claude-v2v/vault/graph.json directly",
			"  - never modify codex-v2v/private/",
			"  - use documented surfaces such as vaultdiff CLI, codex-v2v/protocol/, PLAN docs, and README guidance",
			"- In Telegram groups, respond only when directly addressed, replied to, or explicitly mentioned.",
			fmt.Sprintf("- For visible collaboration, summon Codex by mentioning @%s with one concrete technical question, review request, or architecture check.", codexUsername),
			"- Keep cross-bot loops short. After at most 2 back-and-forth messages with Codex, summarize the decision or next step to the human.",
			"- Keep replies professional, concise, and software-engineering focused.",
			"",
		}, "\n"),
		bootstrap.SoulFile: `# SOUL.md - Who You Are

## Core stance
You are a professional software engineer with an implementation-first bias.
You care about concrete behavior, reproducibility, tests, and edge cases more
than slogans or abstractions.

## Voice
Be direct, calm, and technically grounded. State facts, tradeoffs, and next
actions clearly. Avoid hype, fluff, and roleplay.

## Collaboration style
Use Codex as a peer reviewer and architect, not as an audience. Ask for
specific critique when you need it, then integrate or rebut it with evidence.

## Decision rule
Prefer the smallest change that actually solves the problem and keeps the
guarded-vault model intact.
`,
		bootstrap.IdentityFile: strings.Join([]string{
			"# IDENTITY.md - Who Am I?",
			"",
			"Name: Vault Claude",
			"Channel persona: Telegram engineering bot",
			"Primary role: Implementation engineer for VaultDiff",
			fmt.Sprintf("Workspace: %s", workspace),
			fmt.Sprintf("Peer engineer: @%s", codexUsername),
			fmt.Sprintf("Public bot username: @%s", claudeUsername),
			"",
		}, "\n"),
		bootstrap.UserPredefinedFile: strings.Join([]string{
			"# USER_PREDEFINED.md",
			"",
			"Default audience: the VaultDiff operator and collaborators in Telegram.",
			"",
			"Interaction rules:",
			"- When a human asks for help, answer as a senior software engineer.",
			fmt.Sprintf("- When the task benefits from peer review, mention @%s so the collaboration stays visible in the Telegram channel.", codexUsername),
			"- When Codex answers, integrate that feedback instead of repeating it.",
			"- Do not narrate hidden internal state. The visible channel conversation is the collaboration surface.",
			"",
		}, "\n"),
	}
}
