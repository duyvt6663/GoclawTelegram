package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/stickers"
)

func TestFindAndPostLocalStickerToolExecute_UsesEmbeddingMetadataSearch(t *testing.T) {
	dir := t.TempDir()
	writeLocalMemeFixture(t, filepath.Join(dir, "fish-sticker.webm"), 128)
	writeLocalMemeFixture(t, filepath.Join(dir, "cat-sticker.webp"), 128)

	metadataPath := filepath.Join(dir, "metadata_with_embeddings.json")
	writeLocalStickerMetadataFixture(t, metadataPath, []stickers.MetadataEntry{
		{
			ID:          "fish/side-eye",
			Filename:    "fish-sticker.webm",
			Path:        "fish-sticker.webm",
			Description: "Judgy fish side-eye sticker",
			Keywords:    []string{"fish", "judgy", "side eye"},
			Embedding:   []float32{1, 0},
			ContentType: "video/webm",
		},
		{
			ID:          "cat/wave",
			Filename:    "cat-sticker.webp",
			Path:        "cat-sticker.webp",
			Description: "Cat waving sticker",
			Keywords:    []string{"cat", "hello", "wave"},
			Embedding:   []float32{0, 1},
			ContentType: "image/webp",
		},
	})

	settings := stickers.Settings{
		Libraries: []stickers.Library{{
			Name:         "telegram-learned",
			Path:         dir,
			Enabled:      boolPtr(true),
			MetadataPath: metadataPath,
			MetadataRoot: dir,
		}},
	}

	ctx := withLocalStickerSettings(context.Background(), settings)
	tool := NewFindAndPostLocalStickerTool()
	tool.SetEmbeddingProvider(mockEmbeddingProvider{
		vectors: map[string][]float32{
			"send a judgy fish reaction": {1, 0},
		},
	})

	result := tool.Execute(ctx, map[string]any{"query": "send a judgy fish reaction"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if len(result.Media) != 1 {
		t.Fatalf("expected 1 media attachment, got %d", len(result.Media))
	}
	if got := filepath.Base(result.Media[0].Path); got != "fish-sticker.webm" {
		t.Fatalf("unexpected sticker path: got %q", got)
	}
	if !strings.Contains(result.ForLLM, "Description: Judgy fish side-eye sticker") {
		t.Fatalf("expected metadata description in ForLLM, got %q", result.ForLLM)
	}
}

func TestFindAndPostLocalStickerToolExecute_UsesTelegramFileIDOnTelegram(t *testing.T) {
	dir := t.TempDir()
	writeLocalMemeFixture(t, filepath.Join(dir, "fish-sticker.webm"), 128)

	metadataPath := filepath.Join(dir, "metadata_with_embeddings.json")
	writeLocalStickerMetadataFixture(t, metadataPath, []stickers.MetadataEntry{{
		ID:             "fish/side-eye",
		Filename:       "fish-sticker.webm",
		Path:           "fish-sticker.webm",
		Description:    "Judgy fish side-eye sticker",
		Keywords:       []string{"fish", "judgy"},
		Embedding:      []float32{1, 0},
		ContentType:    "video/webm",
		TelegramFileID: "telegram-sticker-file-id",
	}})

	settings := stickers.Settings{
		Libraries: []stickers.Library{{
			Name:         "telegram-learned",
			Path:         dir,
			Enabled:      boolPtr(true),
			MetadataPath: metadataPath,
			MetadataRoot: dir,
		}},
	}

	ctx := withLocalStickerSettings(WithToolChannelType(context.Background(), "telegram"), settings)
	tool := NewFindAndPostLocalStickerTool()
	tool.SetEmbeddingProvider(mockEmbeddingProvider{
		vectors: map[string][]float32{
			"send a judgy fish reaction": {1, 0},
		},
	})

	result := tool.Execute(ctx, map[string]any{"query": "send a judgy fish reaction"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if got := result.Media[0].Path; got != "telegram-file-id:telegram-sticker-file-id" {
		t.Fatalf("unexpected telegram sticker media path: got %q", got)
	}
}

func TestFindAndPostLocalStickerToolExecute_FallsBackToMetadataTextSearch(t *testing.T) {
	dir := t.TempDir()
	writeLocalMemeFixture(t, filepath.Join(dir, "fish-sticker.webm"), 128)
	writeLocalMemeFixture(t, filepath.Join(dir, "cat-sticker.webp"), 128)

	metadataPath := filepath.Join(dir, "metadata_with_embeddings.json")
	writeLocalStickerMetadataFixture(t, metadataPath, []stickers.MetadataEntry{
		{
			ID:          "fish/side-eye",
			Filename:    "fish-sticker.webm",
			Path:        "fish-sticker.webm",
			Description: "Fish making a suspicious side-eye",
			Keywords:    []string{"fish", "side eye", "judging"},
			ContentType: "video/webm",
		},
		{
			ID:          "cat/wave",
			Filename:    "cat-sticker.webp",
			Path:        "cat-sticker.webp",
			Description: "Cat waving hello",
			Keywords:    []string{"cat", "wave", "hello"},
			ContentType: "image/webp",
		},
	})

	settings := stickers.Settings{
		Libraries: []stickers.Library{{
			Name:         "telegram-learned",
			Path:         dir,
			Enabled:      boolPtr(true),
			MetadataPath: metadataPath,
			MetadataRoot: dir,
		}},
	}

	ctx := withLocalStickerSettings(context.Background(), settings)
	tool := NewFindAndPostLocalStickerTool()
	result := tool.Execute(ctx, map[string]any{"query": "fish reaction"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if got := filepath.Base(result.Media[0].Path); got != "fish-sticker.webm" {
		t.Fatalf("unexpected sticker path: got %q", got)
	}
}

func writeLocalStickerMetadataFixture(t *testing.T, path string, entries []stickers.MetadataEntry) {
	t.Helper()
	data, err := json.Marshal(stickers.MetadataEnvelope{Stickers: entries})
	if err != nil {
		t.Fatalf("marshal metadata fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write metadata fixture %s: %v", path, err)
	}
}
