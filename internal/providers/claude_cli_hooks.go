package providers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// BuildCLIHooksConfig generates a Claude CLI settings file with PreToolUse hooks
// that enforce GoClaw's security policies (shell deny patterns, path restrictions).
// Read may optionally be allowed from extraReadDirs, while Edit/Write remain pinned
// to workspace when restrictToWorkspace is true.
// Returns settings file path and a cleanup function.
func BuildCLIHooksConfig(workspace string, restrictToWorkspace bool, extraReadDirs ...string) (string, func(), error) {
	tmpDir := filepath.Join(os.TempDir(), "goclaw-cli-hooks")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", nil, fmt.Errorf("create hooks dir: %w", err)
	}

	id := uuid.New().String()[:8]

	// Write the hook script
	hookScript := generateHookScript(workspace, restrictToWorkspace, extraReadDirs...)
	hookPath := filepath.Join(tmpDir, fmt.Sprintf("hook-%s.sh", id))
	if err := os.WriteFile(hookPath, []byte(hookScript), 0755); err != nil {
		return "", nil, fmt.Errorf("write hook script: %w", err)
	}

	// Write settings JSON
	settings := generateSettingsJSON(hookPath)
	settingsPath := filepath.Join(tmpDir, fmt.Sprintf("settings-%s.json", id))
	if err := os.WriteFile(settingsPath, settings, 0600); err != nil {
		os.Remove(hookPath)
		return "", nil, fmt.Errorf("write settings: %w", err)
	}

	cleanup := func() {
		os.Remove(hookPath)
		os.Remove(settingsPath)
	}

	return settingsPath, cleanup, nil
}

// generateSettingsJSON creates Claude CLI settings with PreToolUse hooks.
func generateSettingsJSON(hookPath string) []byte {
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "Bash",
					"hooks": []map[string]any{
						{"type": "command", "command": hookPath},
					},
				},
				{
					"matcher": "Write",
					"hooks": []map[string]any{
						{"type": "command", "command": hookPath},
					},
				},
				{
					"matcher": "Edit",
					"hooks": []map[string]any{
						{"type": "command", "command": hookPath},
					},
				},
				{
					"matcher": "Read",
					"hooks": []map[string]any{
						{"type": "command", "command": hookPath},
					},
				},
			},
		},
	}

	data, _ := json.MarshalIndent(settings, "", "  ")
	return data
}

// generateHookScript creates a bash script that enforces GoClaw security policies.
func generateHookScript(workspace string, restrictToWorkspace bool, extraReadDirs ...string) string {
	var sb strings.Builder
	readRestrictionEnabled := (restrictToWorkspace && workspace != "") || len(extraReadDirs) > 0

	sb.WriteString(`#!/bin/bash
set -euo pipefail

# GoClaw security hook for Claude CLI PreToolUse.
# Checks shell deny patterns and workspace path restrictions.

INPUT=$(cat)
TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // empty')
TOOL_INPUT=$(echo "$INPUT" | jq -c '.tool_input // {}')

allow() {
  echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
  exit 0
}

deny() {
  local reason="$1"
  echo "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"$reason\"}}"
  exit 0
}

path_within_root() {
  local resolved="$1"
  local root="$2"
  [[ "$resolved" == "$root" || "$resolved" == "$root"/* ]]
}

`)

	// Shell deny patterns check
	sb.WriteString(`# === Shell command deny patterns ===
check_shell_deny() {
  local cmd="$1"
  local patterns=(
`)

	for _, p := range ShellDenyPatterns {
		// Escape single quotes for bash
		escaped := strings.ReplaceAll(p, `'`, `'\''`)
		fmt.Fprintf(&sb, "    '%s'\n", escaped)
	}

	sb.WriteString(`  )

  for pattern in "${patterns[@]}"; do
    if echo "$cmd" | grep -qE "$pattern" 2>/dev/null; then
      deny "security: shell command blocked by deny pattern"
    fi
  done
}

`)

	// Path restriction check
	if restrictToWorkspace && workspace != "" {
		// Escape workspace path for safe bash embedding (single quotes + quote escaping)
		safeWorkspace := strings.ReplaceAll(workspace, `'`, `'\''`)
		fmt.Fprintf(&sb, `# === Workspace path restriction ===
WORKSPACE='%s'

check_write_path_restriction() {
  local file_path="$1"
  # Resolve all paths (including relative) to absolute for proper checking
  local resolved
  resolved=$(realpath -m "$file_path" 2>/dev/null || echo "$file_path")
  if ! path_within_root "$resolved" "$WORKSPACE"; then
    deny "security: path outside workspace boundary"
  fi
}

`, safeWorkspace)
	}

	if len(extraReadDirs) > 0 {
		sb.WriteString("# === Extra read-only directories ===\nREAD_DIRS=(\n")
		for _, dir := range extraReadDirs {
			dir = filepath.Clean(strings.TrimSpace(dir))
			if dir == "" {
				continue
			}
			safeDir := strings.ReplaceAll(dir, `'`, `'\''`)
			fmt.Fprintf(&sb, "  '%s'\n", safeDir)
		}
		sb.WriteString(")\n\n")
	} else {
		sb.WriteString("# === Extra read-only directories ===\nREAD_DIRS=()\n\n")
	}

	if readRestrictionEnabled {
		sb.WriteString(`# === Read path restriction ===
check_read_path_restriction() {
  local file_path="$1"
  local resolved
  resolved=$(realpath -m "$file_path" 2>/dev/null || echo "$file_path")
`)

		if restrictToWorkspace && workspace != "" {
			sb.WriteString(`  if path_within_root "$resolved" "$WORKSPACE"; then
    return 0
  fi
`)
		}

		sb.WriteString(`  for root in "${READ_DIRS[@]}"; do
    if path_within_root "$resolved" "$root"; then
      return 0
    fi
  done
  deny "security: path outside allowed read roots"
}

`)
	}

	// Main dispatch
	sb.WriteString(`# === Main ===
case "$TOOL_NAME" in
  Bash)
    CMD=$(echo "$TOOL_INPUT" | jq -r '.command // empty')
    if [ -n "$CMD" ]; then
      check_shell_deny "$CMD"
    fi
    ;;
  Write)
    FILE_PATH=$(echo "$TOOL_INPUT" | jq -r '.file_path // empty')
`)

	if restrictToWorkspace && workspace != "" {
		sb.WriteString(`    if [ -n "$FILE_PATH" ]; then
      check_write_path_restriction "$FILE_PATH"
    fi
`)
	}

	sb.WriteString(`    ;;
  Edit)
    FILE_PATH=$(echo "$TOOL_INPUT" | jq -r '.file_path // empty')
`)

	if restrictToWorkspace && workspace != "" {
		sb.WriteString(`    if [ -n "$FILE_PATH" ]; then
      check_write_path_restriction "$FILE_PATH"
    fi
`)
	}

	sb.WriteString(`    ;;
  Read)
    FILE_PATH=$(echo "$TOOL_INPUT" | jq -r '.file_path // empty')
`)

	if readRestrictionEnabled {
		sb.WriteString(`    if [ -n "$FILE_PATH" ]; then
      check_read_path_restriction "$FILE_PATH"
    fi
`)
	}

	sb.WriteString(`    ;;
esac

# Default: allow
allow
`)

	return sb.String()
}
