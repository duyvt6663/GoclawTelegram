package skynetworkflows

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestFeatureStore(t *testing.T) *featureStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	store := &featureStore{db: db}
	if err := store.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

func TestUpdateStatusMergesMetadataAndCountsRefinement(t *testing.T) {
	store := newTestFeatureStore(t)
	items, err := store.addItems("tenant-1", kindBacklog, []string{"rollup item"}, workflowOrigin{}, "repo-backlog:rollup", map[string]string{
		"source_path": "backlog/00-roadmap.md",
		"source_line": "65",
	})
	if err != nil {
		t.Fatalf("addItems: %v", err)
	}

	updated, err := store.updateStatus("tenant-1", items[0].ID, statusRefinement, "split into children", map[string]string{
		"updated_by_agent": "skynet-backlog-iterator",
	})
	if err != nil {
		t.Fatalf("updateStatus: %v", err)
	}

	if updated.Metadata["source_path"] != "backlog/00-roadmap.md" {
		t.Fatalf("source_path metadata was not preserved: %#v", updated.Metadata)
	}
	if updated.Metadata["updated_by_agent"] != "skynet-backlog-iterator" {
		t.Fatalf("updated_by_agent metadata missing: %#v", updated.Metadata)
	}

	counts, err := store.counts("tenant-1")
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts[0].Refinement != 1 {
		t.Fatalf("refinement count = %d, want 1: %#v", counts[0].Refinement, counts[0])
	}
}
