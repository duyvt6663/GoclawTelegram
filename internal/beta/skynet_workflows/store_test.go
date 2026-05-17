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
	var backlogCounts queueCounts
	for _, entry := range counts {
		if entry.Kind == kindBacklog {
			backlogCounts = entry
			break
		}
	}
	if backlogCounts.Refinement != 1 {
		t.Fatalf("refinement count = %d, want 1: %#v", backlogCounts.Refinement, counts)
	}
}

func TestRelatedPendingAndClaimPendingByIDs(t *testing.T) {
	store := newTestFeatureStore(t)
	tenantID := "tenant-1"
	items, err := store.addItems(tenantID, kindBacklog, []string{
		"Add question stack UI primitive",
		"Wire question stack into writing stage",
		"Unrelated billing task",
	}, workflowOrigin{}, "test", map[string]string{
		"parent_item": "parent-1",
	})
	if err != nil {
		t.Fatalf("addItems: %v", err)
	}
	if _, err := store.updateStatus(tenantID, items[2].ID, statusPending, "", map[string]string{"parent_item": "parent-2"}); err != nil {
		t.Fatalf("update unrelated metadata: %v", err)
	}

	primary, err := store.claimNext(tenantID, kindBacklog, "worker", "skynet-backlog-iterator")
	if err != nil {
		t.Fatalf("claimNext: %v", err)
	}
	related, err := store.relatedPending(tenantID, primary, 5)
	if err != nil {
		t.Fatalf("relatedPending: %v", err)
	}
	if len(related) != 1 || related[0].ID != items[1].ID {
		t.Fatalf("related = %#v, want second item only", related)
	}

	claimed, err := store.claimPendingByIDs(tenantID, kindBacklog, "worker", "skynet-backlog-iterator", []string{items[1].ID, items[2].ID})
	if err != nil {
		t.Fatalf("claimPendingByIDs: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed = %d, want 2", len(claimed))
	}
	for _, item := range claimed {
		if item.Status != statusInProgress {
			t.Fatalf("claimed item status = %q", item.Status)
		}
	}
}
