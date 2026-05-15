package skynetworkflows

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseBulletItems(t *testing.T) {
	input := `
- add Telegram CI failure hook
- [ ] add backlog parser
  with continuation
1. add QA cron
not a bullet
`
	got := parseBulletItems(input)
	want := []string{
		"add Telegram CI failure hook",
		"add backlog parser\nwith continuation",
		"add QA cron",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseBulletItems() = %#v, want %#v", got, want)
	}
}

func TestParseBulletItemsFallback(t *testing.T) {
	got := parseBulletItems("single backlog item")
	want := []string{"single backlog item"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseBulletItems() = %#v, want %#v", got, want)
	}
}

func TestScanRepoBacklogFindsUncheckedTasks(t *testing.T) {
	repo := t.TempDir()
	backlogDir := filepath.Join(repo, "backlog")
	if err := os.MkdirAll(backlogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `# Roadmap

## Alpha

- [x] Already done.
- [ ] First open item
  with continuation.
- [ ] Second open item.
`
	if err := os.WriteFile(filepath.Join(backlogDir, "00-roadmap.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backlogDir, "PROGRESS.md"), []byte("- [ ] mirror only\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := scanRepoBacklog(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("scanRepoBacklog() found %d tasks, want 2: %#v", len(got), got)
	}
	if got[0].Metadata["source_path"] != "backlog/00-roadmap.md" {
		t.Fatalf("source_path = %q", got[0].Metadata["source_path"])
	}
	if got[0].Metadata["section"] != "Roadmap > Alpha" {
		t.Fatalf("section = %q", got[0].Metadata["section"])
	}
	if got[0].Body != "[backlog/00-roadmap.md:6] First open item with continuation.\nSection: Roadmap > Alpha" {
		t.Fatalf("body = %q", got[0].Body)
	}
	if got[0].Source == got[1].Source {
		t.Fatalf("sources should be unique: %q", got[0].Source)
	}
}
