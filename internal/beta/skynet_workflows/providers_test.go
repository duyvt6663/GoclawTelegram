package skynetworkflows

import (
	"encoding/json"
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
