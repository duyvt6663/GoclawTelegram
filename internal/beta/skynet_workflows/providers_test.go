package skynetworkflows

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestPreferredProviderForKindSkipsClaudeCLIWithoutMCPBridge(t *testing.T) {
	choice := preferredProviderForKind([]storepkg.LLMProviderData{
		{
			Name:         "vaultdiff-claude-cli",
			ProviderType: storepkg.ProviderClaudeCLI,
			Enabled:      true,
			Settings:     json.RawMessage(`{"use_mcp_bridge":false}`),
		},
		{
			Name:         providerNameSkynetClaudeCLI,
			ProviderType: storepkg.ProviderClaudeCLI,
			Enabled:      true,
			Settings:     json.RawMessage(`{"use_mcp_bridge":true}`),
		},
	}, storepkg.ProviderClaudeCLI)

	if choice.Name != providerNameSkynetClaudeCLI {
		t.Fatalf("choice.Name = %q, want %q", choice.Name, providerNameSkynetClaudeCLI)
	}
}

func TestPreferredProviderForKindCodexUsesToolCapableProviderBeforeCLI(t *testing.T) {
	choice := preferredProviderForKind([]storepkg.LLMProviderData{
		{
			Name:         "vaultdiff-codex-cli",
			ProviderType: storepkg.ProviderCodexCLI,
			Enabled:      true,
		},
		{
			Name:         "openai-compat",
			ProviderType: storepkg.ProviderOpenAICompat,
			Enabled:      true,
		},
	}, storepkg.ProviderCodexCLI)

	if choice.Name != "openai-compat" {
		t.Fatalf("choice.Name = %q, want openai-compat", choice.Name)
	}
}

func TestWorkflowAgentSpecsIncludesRefactorScout(t *testing.T) {
	feature := &SkynetWorkflowsFeature{targetRepo: "/repo"}
	specs := feature.workflowAgentSpecs(context.Background())

	var scout *workflowAgentSpec
	for i := range specs {
		if specs[i].Key == agentKeyRefactorScout {
			scout = &specs[i]
			break
		}
	}
	if scout == nil {
		t.Fatal("refactor scout spec not registered")
	}
	if scout.DisplayName != "Skynet Refactor Scout" {
		t.Fatalf("DisplayName = %q", scout.DisplayName)
	}
	if scout.ProviderKind != storepkg.ProviderClaudeCLI || scout.Model != modelClaudeOpus47 || scout.ReasoningEffort != reasoningHigh {
		t.Fatalf("provider/model/effort = %q/%q/%q", scout.ProviderKind, scout.Model, scout.ReasoningEffort)
	}
	for _, want := range []string{"skynet_workflows", "skynet_change_requests", "skynet_backlog", "skynet_experiments", "skynet_qa", "read_file", "list_files", "exec", "memory_search", "memory_get", "message"} {
		if !stringSliceContains(scout.Tools, want) {
			t.Fatalf("refactor scout tools missing %q: %#v", want, scout.Tools)
		}
	}
}

func TestWorkflowAgentSpecsIncludesMainSyncer(t *testing.T) {
	feature := &SkynetWorkflowsFeature{targetRepo: "/repo"}
	specs := feature.workflowAgentSpecs(context.Background())

	var syncer *workflowAgentSpec
	for i := range specs {
		if specs[i].Key == agentKeyMainSyncer {
			syncer = &specs[i]
			break
		}
	}
	if syncer == nil {
		t.Fatal("main syncer spec not registered")
	}
	if syncer.DisplayName != "Skynet Main Syncer" {
		t.Fatalf("DisplayName = %q", syncer.DisplayName)
	}
	if syncer.ProviderKind != storepkg.ProviderClaudeCLI || syncer.Model != modelClaudeSonnet || syncer.ReasoningEffort != reasoningHigh {
		t.Fatalf("provider/model/effort = %q/%q/%q", syncer.ProviderKind, syncer.Model, syncer.ReasoningEffort)
	}
	for _, want := range []string{"skynet_workflows", "skynet_main_sync", "message"} {
		if !stringSliceContains(syncer.Tools, want) {
			t.Fatalf("main syncer tools missing %q: %#v", want, syncer.Tools)
		}
	}
}

func TestWorkflowCronSpecsIncludesThirtyMinuteRefactorScout(t *testing.T) {
	feature := &SkynetWorkflowsFeature{}
	specs := feature.workflowCronSpecs()

	var scout *workflowCronSpec
	for i := range specs {
		if specs[i].Name == "skynet refactor scout" {
			scout = &specs[i]
			break
		}
	}
	if scout == nil {
		t.Fatal("refactor scout cron spec not registered")
	}
	if scout.AgentKey != agentKeyRefactorScout {
		t.Fatalf("AgentKey = %q, want %q", scout.AgentKey, agentKeyRefactorScout)
	}
	if scout.EveryMS != 30*60*1000 {
		t.Fatalf("EveryMS = %d, want 30 minutes", scout.EveryMS)
	}
	for _, want := range []string{
		"memory",
		"one bounded code slice",
		"recent git update time",
		"category \"refactor\"",
		"technical_debt",
		"Do not edit code",
	} {
		if !strings.Contains(scout.Message, want) {
			t.Fatalf("refactor scout cron message missing %q:\n%s", want, scout.Message)
		}
	}
}

func TestWorkflowCronSpecsUseDirectToolCallsForReviewReminders(t *testing.T) {
	feature := &SkynetWorkflowsFeature{}
	specs := feature.workflowCronSpecs()

	expected := map[string]string{
		"skynet experiment review reminder":     "skynet_experiments",
		"skynet change request review reminder": "skynet_change_requests",
	}
	for name, toolName := range expected {
		var spec *workflowCronSpec
		for i := range specs {
			if specs[i].Name == name {
				spec = &specs[i]
				break
			}
		}
		if spec == nil {
			t.Fatalf("%s cron spec not registered", name)
		}
		if spec.AgentKey != "" {
			t.Fatalf("%s AgentKey = %q, want direct tool call with no agent", name, spec.AgentKey)
		}
		if spec.Tool != toolName {
			t.Fatalf("%s Tool = %q, want %q", name, spec.Tool, toolName)
		}
		if spec.EveryMS != 5*60*1000 {
			t.Fatalf("%s EveryMS = %d, want 5 minutes", name, spec.EveryMS)
		}
		payload := spec.payload()
		if payload.Kind != "tool_call" || payload.Tool != toolName {
			t.Fatalf("%s payload = %#v, want direct %s tool_call", name, payload, toolName)
		}
		if payload.Args["action"] != "review_reminders" || payload.Args["limit"] != 10 {
			t.Fatalf("%s payload args = %#v, want review_reminders limit 10", name, payload.Args)
		}
	}
}

func TestWorkflowCronSpecsIncludesMainDeploymentSync(t *testing.T) {
	feature := &SkynetWorkflowsFeature{}
	specs := feature.workflowCronSpecs()

	var syncer *workflowCronSpec
	for i := range specs {
		if specs[i].Name == "skynet main deployment sync" {
			syncer = &specs[i]
			break
		}
	}
	if syncer == nil {
		t.Fatal("main deployment sync cron spec not registered")
	}
	if syncer.AgentKey != agentKeyMainSyncer {
		t.Fatalf("AgentKey = %q, want %q", syncer.AgentKey, agentKeyMainSyncer)
	}
	if syncer.EveryMS != 15*60*1000 {
		t.Fatalf("EveryMS = %d, want 15 minutes", syncer.EveryMS)
	}
	for _, want := range []string{
		"GitHub push-to-main webhooks",
		"dirty_policy \"stash\"",
		"already_current",
		"dirty_recovery",
		"Do not manually edit",
	} {
		if !strings.Contains(syncer.Message, want) {
			t.Fatalf("main deployment sync cron message missing %q:\n%s", want, syncer.Message)
		}
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
