package skynetworkflows

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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

func partitionRefinementItems(items []string) ([]string, []string) {
	var backlogItems []string
	var experimentItems []string
	for _, item := range uniqueNonEmpty(items) {
		if isExperimentRefinementItem(item) {
			experimentItems = append(experimentItems, item)
			continue
		}
		backlogItems = append(backlogItems, item)
	}
	return backlogItems, experimentItems
}

func isExperimentRefinementItem(item string) bool {
	lower := strings.ToLower(strings.TrimSpace(item))
	return strings.HasPrefix(lower, "experiment leaf:") || strings.HasPrefix(lower, "experiment:")
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

func boolArg(args map[string]any, key string) bool {
	switch value := args[key].(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "y", "on":
			return true
		}
	}
	return false
}

func stringSliceArg(args map[string]any, keys ...string) []string {
	for _, key := range keys {
		switch value := args[key].(type) {
		case []string:
			return uniqueNonEmpty(value)
		case []any:
			out := make([]string, 0, len(value))
			for _, item := range value {
				if text, ok := item.(string); ok {
					out = append(out, text)
				}
			}
			return uniqueNonEmpty(out)
		case string:
			parts := strings.FieldsFunc(value, func(r rune) bool {
				return r == ',' || r == '\n' || r == ' '
			})
			return uniqueNonEmpty(parts)
		}
	}
	return nil
}

func itemIDsArg(args map[string]any) []string {
	ids := stringSliceArg(args, "item_ids", "ids")
	if len(ids) == 0 {
		if id := stringArg(args, "item_id"); id != "" {
			ids = []string{id}
		}
	}
	return ids
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

func cleanRepoRelPath(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimPrefix(value, "/")
	if value == "" || value == "." || strings.HasPrefix(value, "../") || strings.Contains(value, "/../") {
		return ""
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return ""
	}
	return clean
}

func repoGitHubFileURL(repo, relPath string) string {
	relPath = cleanRepoRelPath(relPath)
	if repo == "" || relPath == "" {
		return ""
	}
	gitDir := repoGitDir(repo)
	if gitDir == "" {
		return ""
	}
	commonDir := repoCommonGitDir(gitDir)
	remote := gitConfigOriginURL(filepath.Join(commonDir, "config"))
	if remote == "" {
		return ""
	}
	branch := gitHeadBranch(filepath.Join(gitDir, "HEAD"))
	return githubFileURLFromRemote(remote, branch, relPath)
}

func repoGitDir(repo string) string {
	dotGit := filepath.Join(repo, ".git")
	if info, err := os.Stat(dotGit); err == nil && info.IsDir() {
		return dotGit
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repo, gitDir)
	}
	return filepath.Clean(gitDir)
}

func repoCommonGitDir(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	commonDir := strings.TrimSpace(string(data))
	if commonDir == "" {
		return gitDir
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	return filepath.Clean(commonDir)
}

func gitConfigOriginURL(configPath string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	inOrigin := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			inOrigin = section == `remote "origin"`
			continue
		}
		if !inOrigin {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "url" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func gitHeadBranch(headPath string) string {
	data, err := os.ReadFile(headPath)
	if err != nil {
		return "main"
	}
	head := strings.TrimSpace(string(data))
	const prefix = "ref: refs/heads/"
	if strings.HasPrefix(head, prefix) {
		if branch := strings.TrimSpace(strings.TrimPrefix(head, prefix)); branch != "" {
			return branch
		}
	}
	return "main"
}

func githubFileURLFromRemote(remote, branch, relPath string) string {
	slug := githubSlugFromRemote(remote)
	relPath = cleanRepoRelPath(relPath)
	if slug == "" || relPath == "" {
		return ""
	}
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == "HEAD" {
		branch = "main"
	}
	return "https://github.com/" + slug + "/blob/" + escapeURLPath(branch) + "/" + escapeURLPath(relPath)
}

func githubSlugFromRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	remote = strings.TrimSuffix(remote, ".git")
	if strings.HasPrefix(remote, "git@") {
		withoutUser := strings.TrimPrefix(remote, "git@")
		if idx := strings.Index(withoutUser, ":"); idx > 0 {
			host := withoutUser[:idx]
			if strings.HasPrefix(host, "github.com") {
				remote = withoutUser[idx+1:]
			}
		}
	}
	for _, prefix := range []string{
		"https://github.com/",
		"http://github.com/",
		"ssh://git@github.com/",
		"git@github.com:",
	} {
		if strings.HasPrefix(remote, prefix) {
			remote = strings.TrimPrefix(remote, prefix)
			break
		}
	}
	remote = strings.Trim(remote, "/")
	parts := strings.Split(remote, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return escapeURLPath(parts[0] + "/" + parts[1])
}

func escapeURLPath(value string) string {
	parts := strings.Split(filepath.ToSlash(value), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
