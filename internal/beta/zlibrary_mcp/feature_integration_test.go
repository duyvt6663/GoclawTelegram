//go:build integration

package zlibrarymcp

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestZLibraryMCPLiveDownloadLimits(t *testing.T) {
	if os.Getenv("ZLIBRARY_MCP_INTEGRATION") != "1" {
		t.Skip("set ZLIBRARY_MCP_INTEGRATION=1 to run live ZLibrary MCP compatibility test")
	}
	if os.Getenv("ZLIBRARY_MCP_PATH") == "" {
		t.Skip("set ZLIBRARY_MCP_PATH to an already built zlibrary-mcp checkout for the live test")
	}
	if os.Getenv("ZLIBRARY_EMAIL") == "" || os.Getenv("ZLIBRARY_PASSWORD") == "" {
		t.Skip("set ZLIBRARY_EMAIL and ZLIBRARY_PASSWORD for the live test")
	}

	t.Setenv("ZLIBRARY_MCP_SKIP_SETUP", "1")
	runtime := newZLibraryRuntime(t.TempDir(), &serverInstaller{
		root:         t.TempDir(),
		repoURL:      upstreamRepoURL,
		setupTimeout: 2 * time.Minute,
		runner:       osCommandRunner{},
	}, defaultMCPConnector)
	defer runtime.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	payload, err := runtime.CallTool(ctx, "get_download_limits", map[string]any{})
	if err != nil {
		t.Fatalf("get_download_limits via MCP failed: %v", err)
	}
	if payload == nil || payload.Text == "" {
		t.Fatal("expected non-empty get_download_limits payload")
	}
}
