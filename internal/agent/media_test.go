package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/media"
)

func TestPersistMedia_FallsBackToMediaStoreWhenWorkspaceUploadsUnavailable(t *testing.T) {
	storeDir := t.TempDir()
	mediaStore, err := media.NewStore(storeDir)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "sticker.webm")
	if err := os.WriteFile(srcPath, []byte("webm-bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	workspace := t.TempDir()
	uploadsPath := filepath.Join(workspace, ".uploads")
	if err := os.WriteFile(uploadsPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile(.uploads) error = %v", err)
	}

	loop := &Loop{id: "test-agent", mediaStore: mediaStore}
	refs := loop.persistMedia("agent:test:telegram:group:1", []bus.MediaFile{{
		Path:     srcPath,
		MimeType: "video/webm",
	}}, workspace)

	if len(refs) != 1 {
		t.Fatalf("persistMedia() refs len = %d, want 1", len(refs))
	}
	if refs[0].Kind != "video" {
		t.Fatalf("persistMedia() ref kind = %q, want %q", refs[0].Kind, "video")
	}
	if refs[0].Path == "" {
		t.Fatalf("persistMedia() ref path is empty")
	}
	if strings.HasPrefix(refs[0].Path, workspace) {
		t.Fatalf("persistMedia() path = %q, expected media-store fallback outside workspace", refs[0].Path)
	}
	if _, err := os.Stat(refs[0].Path); err != nil {
		t.Fatalf("persisted fallback file missing: %v", err)
	}
}

func TestPersistMedia_FallsBackToTempStorageWhenWorkspaceAndMediaStoreUnavailable(t *testing.T) {
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "sticker.webm")
	if err := os.WriteFile(srcPath, []byte("webm-bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	workspace := t.TempDir()
	uploadsPath := filepath.Join(workspace, ".uploads")
	if err := os.WriteFile(uploadsPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile(.uploads) error = %v", err)
	}

	loop := &Loop{id: "test-agent"}
	refs := loop.persistMedia("agent:test:telegram:group:2", []bus.MediaFile{{
		Path:     srcPath,
		MimeType: "video/webm",
	}}, workspace)

	if len(refs) != 1 {
		t.Fatalf("persistMedia() refs len = %d, want 1", len(refs))
	}
	if refs[0].Kind != "video" {
		t.Fatalf("persistMedia() ref kind = %q, want %q", refs[0].Kind, "video")
	}
	if refs[0].Path == "" {
		t.Fatalf("persistMedia() ref path is empty")
	}
	if !strings.HasPrefix(refs[0].Path, os.TempDir()) {
		t.Fatalf("persistMedia() path = %q, want temp dir fallback", refs[0].Path)
	}
	if _, err := os.Stat(refs[0].Path); err != nil {
		t.Fatalf("persisted temp fallback file missing: %v", err)
	}
}
