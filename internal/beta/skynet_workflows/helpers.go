package skynetworkflows

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
)

var bulletPrefixRE = regexp.MustCompile(`^\s*(?:[-*+]\s+|\d+[.)]\s+)(.*)$`)

func parseBulletItems(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	var items []string
	var current strings.Builder

	flush := func() {
		value := strings.TrimSpace(current.String())
		current.Reset()
		if value != "" {
			items = append(items, value)
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if match := bulletPrefixRE.FindStringSubmatch(line); len(match) == 2 {
			flush()
			value := strings.TrimSpace(match[1])
			value = strings.TrimSpace(strings.TrimPrefix(value, "[ ]"))
			value = strings.TrimSpace(strings.TrimPrefix(value, "[x]"))
			value = strings.TrimSpace(strings.TrimPrefix(value, "[X]"))
			if value != "" {
				current.WriteString(value)
			}
			continue
		}
		if current.Len() > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			current.WriteByte('\n')
			current.WriteString(trimmed)
			continue
		}
	}
	flush()

	if len(items) == 0 {
		if value := strings.TrimSpace(text); value != "" {
			items = append(items, value)
		}
	}
	return uniqueNonEmpty(items)
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func uniqueSorted(values []string) []string {
	values = uniqueNonEmpty(values)
	slices.Sort(values)
	return values
}

func tenantKeyFromCtx(ctxTenant uuid.UUID) string {
	if ctxTenant == uuid.Nil {
		return storepkg.MasterTenantID.String()
	}
	return ctxTenant.String()
}

func tenantIDFromString(value string) uuid.UUID {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || id == uuid.Nil {
		return storepkg.MasterTenantID
	}
	return id
}

func stringArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func intArg(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		n, _ := value.Int64()
		return int(n)
	default:
		return 0
	}
}

func boolPtr(value bool) *bool {
	v := value
	return &v
}

func stringPtr(value string) *string {
	v := value
	return &v
}

func everySchedule(ms int64) storepkg.CronSchedule {
	return storepkg.CronSchedule{
		Kind:    "every",
		EveryMS: &ms,
	}
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}

func jsonBytesEqual(a, b []byte) bool {
	return compactJSON(a) == compactJSON(b)
}

func compactJSON(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, value); err == nil {
		return buf.String()
	}
	return strings.TrimSpace(string(value))
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	value := strings.ToLower(err.Error())
	return strings.Contains(value, "not found") || errors.Is(err, os.ErrNotExist)
}

func isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists")
}

func writableDir(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probePath := filepath.Join(dir, ".write-probe-"+uuid.NewString())
	if err := os.WriteFile(probePath, []byte("ok"), 0o600); err != nil {
		return false
	}
	_ = os.Remove(probePath)
	return true
}

func safeWorkspace(baseWorkspace, agentKey string) string {
	baseWorkspace = strings.TrimSpace(baseWorkspace)
	candidates := make([]string, 0, 3)
	if baseWorkspace != "" {
		candidates = append(candidates, filepath.Join(baseWorkspace, ".goclaw", "agents", agentKey))
	}
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		candidates = append(candidates, filepath.Join(wd, "beta_cache", "agents", agentKey))
	}
	candidates = append(candidates, filepath.Join(os.TempDir(), "goclaw", "agents", agentKey))

	for _, candidate := range candidates {
		if writableDir(candidate) {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

type flexibleTime struct {
	time.Time
}

func (ft *flexibleTime) Scan(src any) error {
	if src == nil {
		ft.Time = time.Time{}
		return nil
	}
	switch value := src.(type) {
	case time.Time:
		ft.Time = value
		return nil
	case string:
		return ft.parse(value)
	case []byte:
		return ft.parse(string(value))
	default:
		return fmt.Errorf("unsupported timestamp type %T", src)
	}
}

func (ft *flexibleTime) parse(raw string) error {
	value := strings.TrimSpace(raw)
	if idx := strings.Index(value, " m="); idx > 0 {
		value = value[:idx]
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			ft.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("parse timestamp %q", raw)
}

func threadIDFromLocalKey(localKey string) string {
	localKey = strings.TrimSpace(localKey)
	for _, marker := range []string{":topic:", ":thread:"} {
		if idx := strings.Index(localKey, marker); idx > 0 {
			return strings.TrimSpace(localKey[idx+len(marker):])
		}
	}
	return ""
}
