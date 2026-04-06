package channels

import (
	"strings"
	"testing"
	"time"
)

func TestPendingHistoryPruneBeforeDropsStaleEntriesFromContext(t *testing.T) {
	now := time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-1 * time.Minute)

	ph := NewPendingHistory()
	ph.Record("group:1", HistoryEntry{
		Sender:    "@old",
		Body:      "this should not survive restart",
		Timestamp: now.Add(-65 * time.Minute),
		MessageID: "1",
	}, 10)
	ph.Record("group:1", HistoryEntry{
		Sender:    "@fresh",
		Body:      "this is still recent",
		Timestamp: now.Add(-30 * time.Second),
		MessageID: "2",
	}, 10)

	result, err := ph.PruneBefore(cutoff)
	if err != nil {
		t.Fatalf("PruneBefore() error = %v", err)
	}
	if result.RAMEntries != 1 {
		t.Fatalf("PruneBefore() removed %d RAM entries, want 1", result.RAMEntries)
	}

	got := ph.BuildContext("group:1", "current message", 10)
	if strings.Contains(got, "this should not survive restart") {
		t.Fatalf("BuildContext() kept stale message: %q", got)
	}
	if !strings.Contains(got, "this is still recent") {
		t.Fatalf("BuildContext() missing recent message: %q", got)
	}

	entries := ph.GetEntries("group:1")
	if len(entries) != 1 || entries[0].Body != "this is still recent" {
		t.Fatalf("GetEntries() = %#v, want only the recent message", entries)
	}
}

func TestPendingHistoryPruneBeforeDropsStaleDeferredMediaRefs(t *testing.T) {
	now := time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-1 * time.Minute)

	ph := NewPendingHistory()
	ph.Record("group:2", HistoryEntry{
		Sender:    "@old",
		Body:      "[sent media (video)]",
		MediaRefs: []MediaRef{{Type: "video", FileID: "old-file"}},
		Timestamp: now.Add(-10 * time.Minute),
		MessageID: "1",
	}, 10)
	ph.Record("group:2", HistoryEntry{
		Sender:    "@fresh",
		Body:      "[sent media (video)]",
		MediaRefs: []MediaRef{{Type: "video", FileID: "fresh-file"}},
		Timestamp: now.Add(-20 * time.Second),
		MessageID: "2",
	}, 10)

	result, err := ph.PruneBefore(cutoff)
	if err != nil {
		t.Fatalf("PruneBefore() error = %v", err)
	}
	if result.RAMEntries != 1 {
		t.Fatalf("PruneBefore() removed %d RAM entries, want 1", result.RAMEntries)
	}

	refs := ph.CollectMediaRefs("group:2")
	if len(refs) != 1 {
		t.Fatalf("CollectMediaRefs() len = %d, want 1", len(refs))
	}
	if refs[0].FileID != "fresh-file" {
		t.Fatalf("CollectMediaRefs()[0].FileID = %q, want %q", refs[0].FileID, "fresh-file")
	}
}
