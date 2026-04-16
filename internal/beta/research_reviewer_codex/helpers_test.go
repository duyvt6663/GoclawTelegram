package researchreviewercodex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestReviewerWorkspaceFallsBackWhenBaseWorkspaceIsNotWritable(t *testing.T) {
	blockedRoot := filepath.Join(t.TempDir(), "blocked-root")
	if err := os.WriteFile(blockedRoot, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatalf("create blocked root: %v", err)
	}

	got := reviewerWorkspace(blockedRoot)
	if strings.HasPrefix(got, blockedRoot) {
		t.Fatalf("reviewerWorkspace(%q) = %q, want fallback away from blocked root", blockedRoot, got)
	}
	if !writableDir(got) {
		t.Fatalf("reviewerWorkspace(%q) = %q, want writable fallback", blockedRoot, got)
	}
}

func TestPreferredProviderChoicePrefersChatGPTOAuth(t *testing.T) {
	choice := preferredProviderChoice([]storepkg.LLMProviderData{
		{Name: "openai-compat", ProviderType: storepkg.ProviderOpenAICompat, Enabled: true},
		{Name: "openai-codex", ProviderType: storepkg.ProviderChatGPTOAuth, Enabled: true},
	})

	if choice.Name != "openai-codex" {
		t.Fatalf("choice.Name = %q, want openai-codex", choice.Name)
	}
	if choice.ProviderType != storepkg.ProviderChatGPTOAuth {
		t.Fatalf("choice.ProviderType = %q, want %q", choice.ProviderType, storepkg.ProviderChatGPTOAuth)
	}
}

func TestReviewerOtherConfigJSONUsesProviderDefaultReasoningForOpenAICompat(t *testing.T) {
	data := reviewerOtherConfigJSON(storepkg.ProviderOpenAICompat)

	var raw struct {
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if raw.Reasoning.Effort != reviewerReasoningAuto {
		t.Fatalf("reasoning.effort = %q, want %q", raw.Reasoning.Effort, reviewerReasoningAuto)
	}
}

func TestReviewerOtherConfigJSONKeepsCustomReasoningForChatGPTOAuth(t *testing.T) {
	data := reviewerOtherConfigJSON(storepkg.ProviderChatGPTOAuth)

	var raw struct {
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if raw.Reasoning.Effort != reviewerReasoningEffort {
		t.Fatalf("reasoning.effort = %q, want %q", raw.Reasoning.Effort, reviewerReasoningEffort)
	}
}
