package zlibrarymcp

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestEnsureRSAKeyPairGeneratedOnceAndPublicOnly(t *testing.T) {
	root := t.TempDir()

	first, err := ensureRSAKeyPair(root)
	if err != nil {
		t.Fatalf("ensureRSAKeyPair() error = %v", err)
	}
	if first.PublicPEM == "" {
		t.Fatal("expected public key PEM")
	}
	if strings.Contains(first.PublicPEM, "PRIVATE KEY") {
		t.Fatal("public key payload must not contain private key material")
	}
	if _, err := parseRSAPublicKey([]byte(first.PublicPEM)); err != nil {
		t.Fatalf("public key did not parse: %v", err)
	}
	info, err := os.Stat(first.PrivatePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("private key mode = %v, want 0600", got)
	}

	privateBefore, err := os.ReadFile(first.PrivatePath)
	if err != nil {
		t.Fatal(err)
	}
	publicBefore := first.PublicPEM

	second, err := ensureRSAKeyPair(root)
	if err != nil {
		t.Fatalf("second ensureRSAKeyPair() error = %v", err)
	}
	privateAfter, err := os.ReadFile(second.PrivatePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(privateBefore) != string(privateAfter) {
		t.Fatal("private key changed on second setup")
	}
	if second.PublicPEM != publicBefore {
		t.Fatal("public key changed on second setup")
	}
}

func TestResolveStorageRootFallsBackFromBlockedDataDir(t *testing.T) {
	blockedDataDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blockedDataDir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()

	root, err := resolveStorageRoot(blockedDataDir, workspace)
	if err != nil {
		t.Fatalf("resolveStorageRoot() error = %v", err)
	}
	want := filepath.Join(workspace, "beta_cache", featureName)
	if root != want {
		t.Fatalf("storage root = %q, want %q", root, want)
	}
	if err := probeWritableDir(root); err != nil {
		t.Fatalf("fallback root is not writable: %v", err)
	}
}

func TestNormalizeSearchBooksRequestMapsMCPArguments(t *testing.T) {
	args, err := normalizeSearchBooksRequest(SearchBooksRequest{
		Query:        "  Dune   Frank Herbert ",
		Exact:        true,
		FromYear:     1960,
		ToYear:       1970,
		Languages:    []string{" english ", ""},
		Extensions:   []string{" pdf "},
		ContentTypes: []string{" book "},
		Count:        3,
	})
	if err != nil {
		t.Fatalf("normalizeSearchBooksRequest() error = %v", err)
	}
	if args["query"] != "Dune Frank Herbert" {
		t.Fatalf("query arg = %q", args["query"])
	}
	if args["fromYear"] != 1960 || args["toYear"] != 1970 {
		t.Fatalf("year args = %#v", args)
	}
	if got := args["languages"].([]string); len(got) != 1 || got[0] != "english" {
		t.Fatalf("languages = %#v", args["languages"])
	}
	if got := args["extensions"].([]string); len(got) != 1 || got[0] != "pdf" {
		t.Fatalf("extensions = %#v", args["extensions"])
	}
	if got := args["content_types"].([]string); len(got) != 1 || got[0] != "book" {
		t.Fatalf("content_types = %#v", args["content_types"])
	}
}

func TestNormalizeDownloadBookRejectsUnsafeOutputSubdir(t *testing.T) {
	_, err := normalizeDownloadBookRequest(DownloadBookRequest{
		BookDetails:  map[string]any{"title": "Dune"},
		OutputSubdir: "../outside",
	}, t.TempDir())
	if err == nil {
		t.Fatal("expected unsafe output_subdir error")
	}
	if !isInputError(err) {
		t.Fatalf("expected input error, got %T", err)
	}
}

func TestInstallerRunsDependencySetupLifecycle(t *testing.T) {
	t.Setenv(envServerPath, "")
	t.Setenv(envSkipSetup, "")
	root := t.TempDir()
	var commands []string
	runner := fakeCommandRunner{run: func(_ context.Context, dir, name string, args []string, _ map[string]string) error {
		commands = append(commands, name+" "+strings.Join(args, " "))
		switch name {
		case "git":
			serverDir := args[len(args)-1]
			if err := os.MkdirAll(serverDir, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(serverDir, "package.json"), []byte(`{"scripts":{"build":"tsc"}}`), 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(serverDir, "setup-uv.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
				return err
			}
		case "bash":
			if err := os.MkdirAll(filepath.Join(dir, ".venv"), 0o700); err != nil {
				return err
			}
		case "npm":
			if len(args) == 1 && args[0] == "install" {
				if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o700); err != nil {
					return err
				}
			}
			if len(args) == 2 && args[0] == "run" && args[1] == "build" {
				dist := filepath.Join(dir, "dist")
				if err := os.MkdirAll(dist, 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dist, "index.js"), []byte("console.log('ok')\n"), 0o600); err != nil {
					return err
				}
			}
		}
		return nil
	}}
	installer := &serverInstaller{
		root:         root,
		repoURL:      "https://example.invalid/zlibrary-mcp.git",
		setupTimeout: time.Minute,
		runner:       runner,
	}

	spec, err := installer.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if spec.Command != "node" {
		t.Fatalf("command = %q, want node", spec.Command)
	}
	if spec.ServerDir != filepath.Join(root, "zlibrary-mcp") {
		t.Fatalf("server dir = %q", spec.ServerDir)
	}
	want := []string{
		"git clone --depth 1 https://example.invalid/zlibrary-mcp.git " + filepath.Join(root, "zlibrary-mcp"),
		"bash setup-uv.sh",
		"npm install",
		"npm run build",
	}
	if strings.Join(commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestSearchAndDownloadDispatchMCPTools(t *testing.T) {
	downloadRoot := t.TempDir()
	fake := &fakeRuntime{
		downloadRoot: downloadRoot,
		payload: &MCPToolPayload{
			Text:     `{"ok":true}`,
			CalledAt: time.Now().UTC(),
		},
	}
	feature := &ZLibraryMCPFeature{runtime: fake}

	if _, err := feature.searchBooks(context.Background(), "tenant", SearchBooksRequest{
		Query:      " Dune ",
		Count:      2,
		Extensions: []string{" epub "},
	}); err != nil {
		t.Fatalf("searchBooks() error = %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(fake.calls))
	}
	if fake.calls[0].tool != requiredSearchMCPTool {
		t.Fatalf("search tool = %q", fake.calls[0].tool)
	}
	if fake.calls[0].args["query"] != "Dune" {
		t.Fatalf("search query arg = %#v", fake.calls[0].args["query"])
	}

	if _, err := feature.downloadBook(context.Background(), "tenant", DownloadBookRequest{
		BookDetails:   map[string]any{"title": "Dune", "url": "https://example.invalid/book"},
		OutputSubdir:  "sci-fi",
		ProcessForRAG: true,
	}); err != nil {
		t.Fatalf("downloadBook() error = %v", err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(fake.calls))
	}
	downloadCall := fake.calls[1]
	if downloadCall.tool != requiredDownloadTool {
		t.Fatalf("download tool = %q", downloadCall.tool)
	}
	if _, ok := downloadCall.args["bookDetails"].(map[string]any); !ok {
		t.Fatalf("download args missing camelCase bookDetails: %#v", downloadCall.args)
	}
	if downloadCall.args["outputDir"] != filepath.Join(downloadRoot, "sci-fi") {
		t.Fatalf("outputDir = %#v", downloadCall.args["outputDir"])
	}
	if downloadCall.args["process_for_rag"] != true {
		t.Fatalf("process_for_rag = %#v", downloadCall.args["process_for_rag"])
	}
}

func TestFeatureStoreMigratesAndLogsCall(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "feature.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := &featureStore{db: db}
	if err := store.migrate(); err != nil {
		t.Fatalf("migrate() error = %v", err)
	}
	store.setStateBestEffort(context.Background(), "storage_root", "/tmp/zlibrary")
	if err := store.insertCall(&callRecord{
		TenantID: "tenant",
		ToolName: requiredSearchMCPTool,
		Status:   statusOK,
		Response: []byte(`{"ok":true}`),
	}); err != nil {
		t.Fatalf("insertCall() error = %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM beta_zlibrary_mcp_calls WHERE tool_name = ?`, requiredSearchMCPTool).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("logged calls = %d, want 1", count)
	}
}

type fakeRuntime struct {
	calls        []fakeMCPCall
	payload      *MCPToolPayload
	err          error
	downloadRoot string
	closed       bool
}

type fakeCommandRunner struct {
	run func(ctx context.Context, dir, name string, args []string, env map[string]string) error
}

func (r fakeCommandRunner) Run(ctx context.Context, dir, name string, args []string, env map[string]string) error {
	return r.run(ctx, dir, name, args, env)
}

type fakeMCPCall struct {
	tool string
	args map[string]any
}

func (f *fakeRuntime) CallTool(_ context.Context, toolName string, args map[string]any) (*MCPToolPayload, error) {
	f.calls = append(f.calls, fakeMCPCall{tool: toolName, args: args})
	if f.err != nil {
		return nil, f.err
	}
	return f.payload, nil
}

func (f *fakeRuntime) Status() RuntimeStatus {
	return RuntimeStatus{Connected: !f.closed, DownloadRoot: f.downloadRoot}
}

func (f *fakeRuntime) DownloadRoot() string {
	return f.downloadRoot
}

func (f *fakeRuntime) Close(_ context.Context) error {
	f.closed = true
	return nil
}
