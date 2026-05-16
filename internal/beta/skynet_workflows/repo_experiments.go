package skynetworkflows

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

var (
	experimentStatusRE = regexp.MustCompile(`(?im)^>\s*\*\*Status:\*\*\s*([a-z_ -]+)\s*$`)
	experimentTitleRE  = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)
)

type repoExperimentTask struct {
	Body     string
	Source   string
	Metadata map[string]string
}

type repoExperimentSyncResult struct {
	Repo     string `json:"repo"`
	Scanned  int    `json:"scanned"`
	Imported int    `json:"imported"`
	Existing int    `json:"existing"`
}

func (f *SkynetWorkflowsFeature) syncRepoExperiments(ctx context.Context, tenantID string) (*repoExperimentSyncResult, error) {
	result := &repoExperimentSyncResult{Repo: f.resolveTargetRepo(ctx)}
	if f == nil || f.store == nil {
		return result, nil
	}
	tasks, err := scanRepoExperiments(result.Repo)
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
	known, err := f.store.knownSources(tenantID, kindExperiment, sources)
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
			metadata["created_by_agent"] = "repo-experiment-sync"
		}
		if _, err := f.store.addItems(tenantID, kindExperiment, []string{task.Body}, origin, task.Source, metadata); err != nil {
			return result, err
		}
		known[task.Source] = true
		result.Imported++
	}
	if result.Imported > 0 {
		f.refreshRepoWorkflowReference(ctx, tenantID)
	}
	return result, nil
}

func scanRepoExperiments(repo string) ([]repoExperimentTask, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return nil, fmt.Errorf("target repository is not configured")
	}
	experimentsDir := filepath.Join(repo, "apps", "web", "experiments")
	entries, err := os.ReadDir(experimentsDir)
	if err != nil {
		return nil, fmt.Errorf("read repo experiments: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	tasks := make([]repoExperimentTask, 0)
	for _, entry := range entries {
		if !entry.IsDir() || strings.EqualFold(entry.Name(), "TEMPLATE") {
			continue
		}
		task, ok, err := scanRepoExperimentDir(repo, filepath.Join(experimentsDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if ok {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}

func scanRepoExperimentDir(repo, dir string) (repoExperimentTask, bool, error) {
	readmePath := filepath.Join(dir, "README.md")
	raw, err := os.ReadFile(readmePath)
	if err != nil {
		if os.IsNotExist(err) {
			return repoExperimentTask{}, false, nil
		}
		return repoExperimentTask{}, false, fmt.Errorf("read experiment README %s: %w", readmePath, err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	status := strings.ToLower(strings.TrimSpace(firstRegexpGroup(experimentStatusRE, text)))
	if status == "" {
		status = "draft"
	}
	switch status {
	case "draft", "validated":
	default:
		return repoExperimentTask{}, false, nil
	}

	title := strings.TrimSpace(firstRegexpGroup(experimentTitleRE, text))
	if title == "" {
		title = filepath.Base(dir)
	}
	relReadme, err := filepath.Rel(repo, readmePath)
	if err != nil {
		relReadme = readmePath
	}
	relDir, err := filepath.Rel(repo, dir)
	if err != nil {
		relDir = dir
	}
	relReadme = filepath.ToSlash(relReadme)
	relDir = filepath.ToSlash(relDir)
	slug := filepath.Base(dir)

	body := fmt.Sprintf("[%s] %s\nStatus: %s\nPath: %s\nTask: Validate the sandbox experiment, append findings to the writeup, update the registry status when appropriate, then request channel review. If it is already validated and accepted by user feedback, transition it to backlog.", relReadme, title, status, relDir)
	return repoExperimentTask{
		Body:   body,
		Source: "repo-experiment:" + slug,
		Metadata: map[string]string{
			"source_kind":       "repo_experiment",
			"experiment_slug":   slug,
			"experiment_path":   relDir,
			"experiment_readme": relReadme,
			"experiment_status": status,
		},
	}, true, nil
}

func firstRegexpGroup(re *regexp.Regexp, text string) string {
	match := re.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}
