package skynetworkflows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

var (
	repoOpenTaskRE = regexp.MustCompile(`^\s*[-*+]\s+\[\s\]\s+(.+?)\s*$`)
	repoAnyTaskRE  = regexp.MustCompile(`^\s*[-*+]\s+\[[ xX]\]\s+`)
	repoHeadingRE  = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
)

type repoBacklogTask struct {
	Body     string
	Source   string
	Metadata map[string]string
}

type repoBacklogSyncResult struct {
	Repo     string `json:"repo"`
	Scanned  int    `json:"scanned"`
	Imported int    `json:"imported"`
	Existing int    `json:"existing"`
}

func (f *SkynetWorkflowsFeature) syncRepoBacklog(ctx context.Context, tenantID string) (*repoBacklogSyncResult, error) {
	result := &repoBacklogSyncResult{Repo: f.resolveTargetRepo(ctx)}
	if f == nil || f.store == nil {
		return result, nil
	}
	tasks, err := scanRepoBacklog(result.Repo)
	if err != nil {
		return result, err
	}
	result.Scanned = len(tasks)
	if len(tasks) == 0 {
		return result, nil
	}

	sources := make([]string, 0, len(tasks))
	for _, task := range tasks {
		sources = append(sources, task.Source)
	}
	known, err := f.store.knownSources(tenantID, kindBacklog, sources)
	if err != nil {
		return result, err
	}

	origin := f.configuredOrigin(ctx)
	for _, task := range tasks {
		if known[task.Source] {
			result.Existing++
			continue
		}
		metadata := cloneStringMap(task.Metadata)
		metadata["created_by_agent"] = tools.ToolAgentKeyFromCtx(ctx)
		if metadata["created_by_agent"] == "" {
			metadata["created_by_agent"] = "repo-backlog-sync"
		}
		if _, err := f.store.addItems(tenantID, kindBacklog, []string{task.Body}, origin, task.Source, metadata); err != nil {
			return result, err
		}
		known[task.Source] = true
		result.Imported++
	}
	return result, nil
}

func scanRepoBacklog(repo string) ([]repoBacklogTask, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return nil, fmt.Errorf("target repository is not configured")
	}
	backlogDir := filepath.Join(repo, "backlog")
	entries, err := os.ReadDir(backlogDir)
	if err != nil {
		return nil, fmt.Errorf("read repo backlog: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var tasks []repoBacklogTask
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		switch strings.ToLower(entry.Name()) {
		case "readme.md", "progress.md":
			continue
		}
		path := filepath.Join(backlogDir, entry.Name())
		fileTasks, err := scanRepoBacklogFile(repo, path)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, fileTasks...)
	}
	return tasks, nil
}

func scanRepoBacklogFile(repo, path string) ([]repoBacklogTask, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read backlog file %s: %w", path, err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	rel, err := filepath.Rel(repo, path)
	if err != nil {
		rel = path
	}
	rel = filepath.ToSlash(rel)

	headings := make([]string, 0, 6)
	tasks := make([]repoBacklogTask, 0)
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if match := repoHeadingRE.FindStringSubmatch(line); len(match) == 3 {
			level := len(match[1])
			title := strings.TrimSpace(match[2])
			if len(headings) >= level {
				headings = headings[:level-1]
			}
			for len(headings) < level-1 {
				headings = append(headings, "")
			}
			headings = append(headings, title)
			continue
		}

		match := repoOpenTaskRE.FindStringSubmatch(line)
		if len(match) != 2 {
			continue
		}
		startLine := i + 1
		parts := []string{strings.TrimSpace(match[1])}
		j := i + 1
		for ; j < len(lines); j++ {
			next := lines[j]
			trimmed := strings.TrimSpace(next)
			if trimmed == "" || repoHeadingRE.MatchString(next) || repoAnyTaskRE.MatchString(next) {
				break
			}
			if strings.HasPrefix(next, " ") || strings.HasPrefix(next, "\t") {
				parts = append(parts, trimmed)
				continue
			}
			break
		}
		i = j - 1

		taskText := strings.Join(parts, " ")
		section := compactHeadingPath(headings)
		body := fmt.Sprintf("[%s:%d] %s", rel, startLine, taskText)
		if section != "" {
			body = body + "\nSection: " + section
		}
		source := "repo-backlog:" + rel + ":" + shortSourceHash(section+"|"+taskText)
		tasks = append(tasks, repoBacklogTask{
			Body:   body,
			Source: source,
			Metadata: map[string]string{
				"source_kind": "repo_backlog",
				"source_path": rel,
				"source_line": strconv.Itoa(startLine),
				"section":     section,
			},
		})
	}
	return tasks, nil
}

func compactHeadingPath(headings []string) string {
	parts := make([]string, 0, len(headings))
	for _, heading := range headings {
		heading = strings.TrimSpace(heading)
		if heading != "" {
			parts = append(parts, heading)
		}
	}
	return strings.Join(parts, " > ")
}

func shortSourceHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
