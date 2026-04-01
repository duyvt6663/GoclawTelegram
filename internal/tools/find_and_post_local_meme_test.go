package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindAndPostLocalMemeToolExecute_UsesEmbeddingMetadataSearch(t *testing.T) {
	dir := t.TempDir()
	writeLocalMemeFixture(t, filepath.Join(dir, "clip-a.mp4"), 128)
	writeLocalMemeFixture(t, filepath.Join(dir, "clip-b.mp4"), 128)

	metadataPath := filepath.Join(dir, "metadata_with_embeddings.json")
	writeLocalMemeMetadataFixture(t, metadataPath, []localMemeMetadataEntry{
		{
			ID:          "cat/laughing",
			Filename:    "clip-a.mp4",
			Path:        "storage/greenscreen_memes/cat/clip-a.mp4",
			Description: "Cat laughing",
			Scenarios:   []string{"funny joke"},
			Embedding:   []float32{1, 0},
		},
		{
			ID:          "cat/angry",
			Filename:    "clip-b.mp4",
			Path:        "storage/greenscreen_memes/cat/clip-b.mp4",
			Description: "Angry cat",
			Scenarios:   []string{"mad at someone"},
			Embedding:   []float32{0, 1},
		},
	})

	settings := localMemeSettings{
		Libraries: []localMemeLibrary{{
			Name:         "cat",
			Path:         dir,
			Enabled:      boolPtr(true),
			MetadataPath: metadataPath,
		}},
		MaxBytes: 1024,
	}

	ctx := withLocalMemeSettings(t, settings)
	tool := NewFindAndPostLocalMemeTool()
	tool.SetEmbeddingProvider(mockEmbeddingProvider{
		vectors: map[string][]float32{
			"that absolutely kills me": {1, 0},
		},
	})
	result := tool.Execute(ctx, map[string]any{"query": "that absolutely kills me"})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if len(result.Media) != 1 {
		t.Fatalf("expected 1 media attachment, got %d", len(result.Media))
	}
	if got := filepath.Base(result.Media[0].Path); got != "clip-a.mp4" {
		t.Fatalf("unexpected media path: got %q", got)
	}
	if !strings.Contains(result.ForLLM, "Description: Cat laughing") {
		t.Fatalf("expected metadata description in ForLLM, got %q", result.ForLLM)
	}
}

func TestFindAndPostLocalMemeToolExecute_FallsBackToMetadataTextSearch(t *testing.T) {
	dir := t.TempDir()
	writeLocalMemeFixture(t, filepath.Join(dir, "clip-a.mp4"), 128)
	writeLocalMemeFixture(t, filepath.Join(dir, "clip-b.mp4"), 128)

	metadataPath := filepath.Join(dir, "metadata_with_embeddings.json")
	writeLocalMemeMetadataFixture(t, metadataPath, []localMemeMetadataEntry{
		{
			ID:          "cat/curious",
			Filename:    "clip-a.mp4",
			Path:        "storage/greenscreen_memes/cat/clip-a.mp4",
			Description: "A curious cat peeking around the corner",
			Scenarios:   []string{"reacting to something new"},
		},
		{
			ID:          "cat/sleepy",
			Filename:    "clip-b.mp4",
			Path:        "storage/greenscreen_memes/cat/clip-b.mp4",
			Description: "A sleepy cat nodding off",
			Scenarios:   []string{"too tired to care"},
		},
	})

	settings := localMemeSettings{
		Libraries: []localMemeLibrary{{
			Name:         "cat",
			Path:         dir,
			Enabled:      boolPtr(true),
			MetadataPath: metadataPath,
		}},
		MaxBytes: 1024,
	}

	ctx := withLocalMemeSettings(t, settings)
	tool := NewFindAndPostLocalMemeTool()
	result := tool.Execute(ctx, map[string]any{"query": "curious about this"})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if got := filepath.Base(result.Media[0].Path); got != "clip-a.mp4" {
		t.Fatalf("unexpected media path: got %q", got)
	}
}

func TestFindAndPostLocalMemeToolExecute_RejectsMissingSettings(t *testing.T) {
	tool := NewFindAndPostLocalMemeTool()
	result := tool.Execute(context.Background(), map[string]any{"query": "cat"})

	if !result.IsError {
		t.Fatalf("expected error result, got %+v", result)
	}
	if !strings.Contains(result.ForLLM, "no local meme libraries are configured") {
		t.Fatalf("unexpected error message: %q", result.ForLLM)
	}
}

func TestScoreLocalMemeCandidate(t *testing.T) {
	if got := scoreLocalMemeCandidate("typing keyboard", "keyboard-cat-typing.mp4"); got <= 0 {
		t.Fatalf("expected positive score, got %d", got)
	}
	if got := scoreLocalMemeCandidate("typing keyboard", "cat-dancing.mp4"); got != 0 {
		t.Fatalf("expected zero score for non-match, got %d", got)
	}
}

func withLocalMemeSettings(t *testing.T, settings localMemeSettings) context.Context {
	t.Helper()
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	return WithBuiltinToolSettings(context.Background(), BuiltinToolSettings{
		localMemeToolName: raw,
	})
}

func writeLocalMemeFixture(t *testing.T, path string, size int) {
	t.Helper()
	data := make([]byte, size)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

func writeLocalMemeMetadataFixture(t *testing.T, path string, entries []localMemeMetadataEntry) {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal metadata fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write metadata fixture %s: %v", path, err)
	}
}

type mockEmbeddingProvider struct {
	vectors map[string][]float32
}

func (m mockEmbeddingProvider) Name() string { return "mock" }

func (m mockEmbeddingProvider) Model() string { return "mock-embedding" }

func (m mockEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vector, ok := m.vectors[text]
		if !ok {
			return nil, fmt.Errorf("unexpected query %q", text)
		}
		out[i] = append([]float32(nil), vector...)
	}
	return out, nil
}
