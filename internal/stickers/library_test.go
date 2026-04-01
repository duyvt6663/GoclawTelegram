package stickers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestCaptureService_CaptureTelegramStickerStoresMetadataAndEmbedding(t *testing.T) {
	libDir := t.TempDir()
	metadataPath := filepath.Join(libDir, "metadata_with_embeddings.json")

	settingsRaw, err := json.Marshal(Settings{
		Libraries: []Library{{
			Name:         "telegram-learned",
			Path:         libDir,
			Enabled:      boolPtr(true),
			MetadataPath: metadataPath,
			MetadataRoot: libDir,
		}},
		AutoCapture: AutoCaptureSettings{
			Enabled: boolPtr(true),
			Library: "telegram-learned",
		},
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}

	assetPath := filepath.Join(t.TempDir(), "sticker.webm")
	if err := os.WriteFile(assetPath, []byte("video-sticker-bytes"), 0o644); err != nil {
		t.Fatalf("write asset fixture: %v", err)
	}
	previewPath := filepath.Join(t.TempDir(), "preview.webp")
	if err := os.WriteFile(previewPath, []byte("preview-bytes"), 0o644); err != nil {
		t.Fatalf("write preview fixture: %v", err)
	}

	svc := NewCaptureService(fakeBuiltinToolStore{
		settings: map[string]json.RawMessage{
			LocalStickerToolName: settingsRaw,
		},
	}, nil)
	svc.SetEmbeddingProvider(fakeEmbeddingProvider{})

	input := CaptureInput{
		TenantID:           uuid.New(),
		ChannelName:        "telegram-fox",
		ChannelType:        "telegram",
		ChatID:             "-100123",
		MessageID:          "42",
		StickerType:        "video",
		Emoji:              "🐟",
		SetName:            "fish_pack",
		Note:               "fish reaction sticker",
		AssetPath:          assetPath,
		AssetContentType:   "video/webm",
		AssetFileID:        "telegram-file-id",
		PreviewPath:        previewPath,
		PreviewContentType: "image/webp",
		PreviewFileID:      "telegram-preview-file-id",
	}

	if err := svc.CaptureTelegramSticker(context.Background(), input); err != nil {
		t.Fatalf("CaptureTelegramSticker() error = %v", err)
	}
	if err := svc.CaptureTelegramSticker(context.Background(), input); err != nil {
		t.Fatalf("CaptureTelegramSticker() second call error = %v", err)
	}

	entries, err := LoadMetadata(metadataPath)
	if err != nil {
		t.Fatalf("LoadMetadata() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.CaptureCount != 2 {
		t.Fatalf("CaptureCount = %d, want 2", entry.CaptureCount)
	}
	if entry.Description == "" || !strings.Contains(entry.Description, "video sticker") {
		t.Fatalf("unexpected description: %q", entry.Description)
	}
	if len(entry.Embedding) == 0 {
		t.Fatalf("expected embedding to be stored")
	}
	if entry.SearchText == "" {
		t.Fatalf("expected search text to be stored")
	}
	if entry.PreviewPath == "" {
		t.Fatalf("expected preview path to be stored")
	}
	if _, err := os.Stat(ResolveEntryPath(entry.Path, libDir)); err != nil {
		t.Fatalf("captured asset missing: %v", err)
	}
	if _, err := os.Stat(ResolveEntryPath(entry.PreviewPath, libDir)); err != nil {
		t.Fatalf("captured preview missing: %v", err)
	}
	if entry.SourceChannel != "telegram-fox" || entry.SourceMessageID != "42" {
		t.Fatalf("unexpected source metadata: %+v", entry)
	}
	if entry.TelegramFileID != "telegram-file-id" || entry.TelegramPreviewID != "telegram-preview-file-id" {
		t.Fatalf("expected telegram file ids to persist, got %+v", entry)
	}
}

type fakeBuiltinToolStore struct {
	settings map[string]json.RawMessage
}

func (f fakeBuiltinToolStore) List(context.Context) ([]store.BuiltinToolDef, error) {
	return nil, nil
}

func (f fakeBuiltinToolStore) Get(_ context.Context, name string) (*store.BuiltinToolDef, error) {
	raw := f.settings[name]
	return &store.BuiltinToolDef{
		Name:     name,
		Enabled:  true,
		Settings: raw,
	}, nil
}

func (f fakeBuiltinToolStore) Update(context.Context, string, map[string]any) error {
	return nil
}

func (f fakeBuiltinToolStore) Seed(context.Context, []store.BuiltinToolDef) error {
	return nil
}

func (f fakeBuiltinToolStore) ListEnabled(context.Context) ([]store.BuiltinToolDef, error) {
	return nil, nil
}

func (f fakeBuiltinToolStore) GetSettings(_ context.Context, name string) (json.RawMessage, error) {
	return f.settings[name], nil
}

type fakeEmbeddingProvider struct{}

func (fakeEmbeddingProvider) Name() string { return "mock" }

func (fakeEmbeddingProvider) Model() string { return "mock-embedding" }

func (fakeEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = []float32{float32(len(text))}
	}
	return out, nil
}
