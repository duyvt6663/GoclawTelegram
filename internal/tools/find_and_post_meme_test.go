package tools

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type stubSearchProvider struct {
	name    string
	results []searchResult
	err     error
}

func (s stubSearchProvider) Search(ctx context.Context, params searchParams) ([]searchResult, error) {
	return s.results, s.err
}

func (s stubSearchProvider) Name() string { return s.name }

func TestFindAndPostMemeToolExecute_FromResultPageOGImage(t *testing.T) {
	const png1x1 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+nXQ0AAAAASUVORK5CYII="
	imageBytes, err := base64.StdEncoding.DecodeString(png1x1)
	if err != nil {
		t.Fatal(err)
	}

	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageBytes)
	}))
	defer imageServer.Close()

	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><meta property="og:image" content="` + imageServer.URL + `/meme.png"></head><body>meme</body></html>`))
	}))
	defer pageServer.Close()

	tool := NewFindAndPostMemeTool(&WebSearchTool{
		providers: []SearchProvider{
			stubSearchProvider{
				name: "stub",
				results: []searchResult{{
					Title: "mock meme page",
					URL:   pageServer.URL + "/page",
				}},
			},
		},
	}, nil)
	tool.validateURL = func(string) error { return nil }

	workspace := t.TempDir()
	ctx := WithToolWorkspace(context.Background(), workspace)
	result := tool.Execute(ctx, map[string]any{"query": "surprised cat"})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if len(result.Media) != 1 {
		t.Fatalf("expected 1 media attachment, got %d", len(result.Media))
	}
	if result.Media[0].MimeType != "image/png" {
		t.Fatalf("expected image/png, got %q", result.Media[0].MimeType)
	}
	if _, err := os.Stat(result.Media[0].Path); err != nil {
		t.Fatalf("downloaded file missing: %v", err)
	}
	if !strings.Contains(result.ForLLM, pageServer.URL+"/page") {
		t.Fatalf("expected source page in ForLLM, got %q", result.ForLLM)
	}
}

func TestFindAndPostMemeToolExecute_FromDirectImageResult(t *testing.T) {
	const png1x1 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+nXQ0AAAAASUVORK5CYII="
	imageBytes, err := base64.StdEncoding.DecodeString(png1x1)
	if err != nil {
		t.Fatal(err)
	}

	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageBytes)
	}))
	defer imageServer.Close()

	tool := NewFindAndPostMemeTool(&WebSearchTool{
		providers: []SearchProvider{
			stubSearchProvider{
				name: "stub",
				results: []searchResult{{
					Title: "direct meme",
					URL:   imageServer.URL + "/reaction.png",
				}},
			},
		},
	}, nil)
	tool.validateURL = func(string) error { return nil }

	ctx := WithToolWorkspace(context.Background(), t.TempDir())
	result := tool.Execute(ctx, map[string]any{"query": "facepalm"})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if len(result.Media) != 1 {
		t.Fatalf("expected 1 media attachment, got %d", len(result.Media))
	}
	if result.Media[0].MimeType != "image/png" {
		t.Fatalf("expected image/png, got %q", result.Media[0].MimeType)
	}
	if !strings.Contains(result.ForLLM, imageServer.URL+"/reaction.png") {
		t.Fatalf("expected image URL in ForLLM, got %q", result.ForLLM)
	}
}

func TestFindAndPostMemeToolExecute_FallsBackToTempDirWhenWorkspaceUnavailable(t *testing.T) {
	const png1x1 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+nXQ0AAAAASUVORK5CYII="
	imageBytes, err := base64.StdEncoding.DecodeString(png1x1)
	if err != nil {
		t.Fatal(err)
	}

	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageBytes)
	}))
	defer imageServer.Close()

	tool := NewFindAndPostMemeTool(&WebSearchTool{
		providers: []SearchProvider{
			stubSearchProvider{
				name: "stub",
				results: []searchResult{{
					Title: "direct meme",
					URL:   imageServer.URL + "/reaction.png",
				}},
			},
		},
	}, nil)
	tool.validateURL = func(string) error { return nil }

	baseDir := t.TempDir()
	lockedWorkspace := filepath.Join(baseDir, "locked")
	if err := os.MkdirAll(lockedWorkspace, 0o555); err != nil {
		t.Fatalf("create locked workspace: %v", err)
	}
	defer func() { _ = os.Chmod(lockedWorkspace, 0o755) }()

	ctx := WithToolWorkspace(context.Background(), lockedWorkspace)
	result := tool.Execute(ctx, map[string]any{"query": "cat meme"})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if len(result.Media) != 1 {
		t.Fatalf("expected 1 media attachment, got %d", len(result.Media))
	}
	if strings.HasPrefix(result.Media[0].Path, lockedWorkspace) {
		t.Fatalf("expected fallback path outside locked workspace, got %q", result.Media[0].Path)
	}
	if !strings.HasPrefix(result.Media[0].Path, os.TempDir()) {
		t.Fatalf("expected fallback path in temp dir, got %q", result.Media[0].Path)
	}
}

func TestPrioritizeMemeSearchResults(t *testing.T) {
	results := []searchResult{
		{Title: "Tenor", URL: "https://tenor.com/view/nope-gif-123"},
		{Title: "Imgflip", URL: "https://imgflip.com/i/abc123"},
		{Title: "Example", URL: "https://example.com/meme"},
		{Title: "KYM", URL: "https://knowyourmeme.com/memes/this-is-fine"},
	}

	prioritizeMemeSearchResults(results)

	got := []string{
		hostnameForURL(results[0].URL),
		hostnameForURL(results[1].URL),
		hostnameForURL(results[2].URL),
		hostnameForURL(results[3].URL),
	}
	want := []string{"imgflip.com", "knowyourmeme.com", "example.com", "tenor.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected ordering: got %v want %v", got, want)
	}
}

func TestExtractImgflipSearchResults(t *testing.T) {
	html := []byte(`
		<html><body>
			<a title="DEPLOYMENT FUN Meme" href="/meme/194940405/DEPLOYMENT-FUN">DEPLOYMENT FUN</a>
			<a title="DEPLOYMENT FUN Meme" href="/meme/194940405/DEPLOYMENT-FUN"><img src="//i.imgflip.com/3828z9.jpg"></a>
			<a title="Lazy deployment Meme" href="/meme/93640051/Lazy-deployment">Lazy deployment</a>
			<a title="Ignore generator" href="/memegenerator/93640051/Lazy-deployment">Add Caption</a>
		</body></html>
	`)

	got := extractImgflipSearchResults(html)
	want := []searchResult{
		{Title: "DEPLOYMENT FUN Meme", URL: "https://imgflip.com/meme/194940405/DEPLOYMENT-FUN"},
		{Title: "Lazy deployment Meme", URL: "https://imgflip.com/meme/93640051/Lazy-deployment"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected imgflip results: got %#v want %#v", got, want)
	}
}

func TestBuildImgflipSearchQueriesSimplifiesPrompt(t *testing.T) {
	got := buildImgflipSearchQueries("no deploy friday meme funny devops friday deploy")
	wantPrefix := []string{
		"no deploy friday meme funny devops friday deploy",
		"deploy friday",
		"deploy friday devops",
		"deploy",
		"friday",
		"devops",
	}
	if len(got) < len(wantPrefix) {
		t.Fatalf("expected at least %d queries, got %v", len(wantPrefix), got)
	}
	if !reflect.DeepEqual(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("unexpected query simplification: got %v want prefix %v", got, wantPrefix)
	}
}
