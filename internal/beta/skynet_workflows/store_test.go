package skynetworkflows

import (
	"database/sql"
	"testing"
	"time"

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

func TestBacklogPriorityClaimOrder(t *testing.T) {
	store := newTestFeatureStore(t)
	tenantID := "tenant-priority"
	low, err := store.addItems(tenantID, kindBacklog, []string{"Low priority cleanup"}, workflowOrigin{}, "test-low", map[string]string{
		"priority": "10",
	})
	if err != nil {
		t.Fatalf("add low priority item: %v", err)
	}
	high, err := store.addItems(tenantID, kindBacklog, []string{"High priority feature"}, workflowOrigin{}, "test-high", map[string]string{
		"priority":         "80",
		"priority_feature": "checkout",
	})
	if err != nil {
		t.Fatalf("add high priority item: %v", err)
	}

	listed, err := store.listItems(tenantID, kindBacklog, statusPending, 10)
	if err != nil {
		t.Fatalf("listItems: %v", err)
	}
	if len(listed) < 2 || listed[0].ID != high[0].ID || listed[1].ID != low[0].ID {
		t.Fatalf("priority list order = %#v, want high before low", listed)
	}

	claimed, err := store.claimNext(tenantID, kindBacklog, "worker", "skynet-backlog-iterator")
	if err != nil {
		t.Fatalf("claimNext: %v", err)
	}
	if claimed.ID != high[0].ID {
		t.Fatalf("claimed %s, want high priority %s", claimed.ID, high[0].ID)
	}
}

func TestBacklogPriorityAgingBoostsBottomItems(t *testing.T) {
	store := newTestFeatureStore(t)
	tenantID := "tenant-aging"
	oldLow, err := store.addItems(tenantID, kindBacklog, []string{"Old low priority task"}, workflowOrigin{}, "test-old-low", map[string]string{
		"priority": "0",
	})
	if err != nil {
		t.Fatalf("add old low item: %v", err)
	}
	high, err := store.addItems(tenantID, kindBacklog, []string{"New urgent task"}, workflowOrigin{}, "test-high", map[string]string{
		"priority": "100",
	})
	if err != nil {
		t.Fatalf("add high item: %v", err)
	}
	oldCreatedAt := time.Now().UTC().Add(-48 * time.Hour)
	if _, err := store.db.Exec(`UPDATE beta_skynet_workflow_items SET created_at=$3, updated_at=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, oldLow[0].ID, oldCreatedAt); err != nil {
		t.Fatalf("age old item: %v", err)
	}

	claimed, err := store.claimNext(tenantID, kindBacklog, "worker", "skynet-backlog-iterator")
	if err != nil {
		t.Fatalf("claimNext: %v", err)
	}
	if claimed.ID != high[0].ID {
		t.Fatalf("claimed %s, want urgent item %s", claimed.ID, high[0].ID)
	}
	boosted, err := store.getItem(tenantID, oldLow[0].ID)
	if err != nil {
		t.Fatalf("get boosted item: %v", err)
	}
	if boosted.Metadata["priority_boost"] != "5" || boosted.Metadata["priority_last_boost_reason"] != "bottom_queue_aging" {
		t.Fatalf("old low item priority boost metadata = %#v", boosted.Metadata)
	}
}
