package providers

import (
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestClaudeCLIResolveWorkDirPrefersWorkspaceOption(t *testing.T) {
	p := NewClaudeCLIProvider("claude", WithClaudeCLIWorkDir(t.TempDir()))
	workspace := t.TempDir()

	got := p.resolveWorkDir(map[string]any{
		OptWorkspace: workspace,
	}, "session-1")

	if got != workspace {
		t.Fatalf("resolveWorkDir() = %q, want %q", got, workspace)
	}
}

func TestClaudeCLIResolveWorkDirFallsBackToSessionDir(t *testing.T) {
	base := t.TempDir()
	p := NewClaudeCLIProvider("claude", WithClaudeCLIWorkDir(base))

	got := p.resolveWorkDir(nil, "session-1")
	want := filepath.Join(base, "session-1")

	if got != want {
		t.Fatalf("resolveWorkDir() = %q, want %q", got, want)
	}
}

func TestClaudeCLIBuildArgsAppendsSystemPrompt(t *testing.T) {
	p := NewClaudeCLIProvider("claude")
	workDir := t.TempDir()
	sessionID := uuid.New()

	args := p.buildArgs(
		"claude-opus-4-7",
		"max",
		workDir,
		"",
		"system guidance",
		sessionID,
		"json",
		false,
		false,
	)

	if !containsArgPair(args, "--append-system-prompt", "system guidance") {
		t.Fatalf("buildArgs() missing --append-system-prompt: %#v", args)
	}
	if !containsArgPair(args, "--effort", "max") {
		t.Fatalf("buildArgs() missing --effort: %#v", args)
	}
}

func TestClaudeCLIBuildArgsAddsExtraDirs(t *testing.T) {
	extraDir := t.TempDir()
	p := NewClaudeCLIProvider("claude", WithClaudeCLIAddDirs(extraDir))

	args := p.buildArgs(
		"claude-opus-4-7",
		"",
		t.TempDir(),
		"",
		"",
		uuid.New(),
		"json",
		false,
		false,
	)

	if !containsArgPair(args, "--add-dir", extraDir) {
		t.Fatalf("buildArgs() missing --add-dir: %#v", args)
	}
}

func containsArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
