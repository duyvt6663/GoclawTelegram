// Package zlibrarymcp integrates the upstream Z-Library MCP server as a
// flag-gated beta feature.
package zlibrarymcp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
)

const (
	featureName           = "zlibrary_mcp"
	mcpServerName         = "zlibrary"
	upstreamRepoURL       = "https://github.com/loganrooks/zlibrary-mcp.git"
	defaultCallTimeout    = 90 * time.Second
	defaultSetupTimeout   = 15 * time.Minute
	statusOK              = "success"
	statusFail            = "failed"
	toolPublicKeyName     = "zlibrary_mcp_public_key"
	toolSearchBooksName   = "zlibrary_mcp_search_books"
	toolDownloadBookName  = "zlibrary_mcp_download_book"
	requiredSearchMCPTool = "search_books"
	requiredDownloadTool  = "download_book_to_file"
)

// ZLibraryMCPFeature installs and runs loganrooks/zlibrary-mcp behind
// GoClaw-native tools, RPC methods, and HTTP routes.
//
// Plan:
// 1. Resolve a writable feature data root and generate a reusable RSA keypair there without exposing the private key.
// 2. Lazily clone/build/connect the upstream stdio MCP server, reusing the running client across tool calls.
// 3. Wrap only the beta-facing public key, search, and download surfaces while logging calls in feature-local tables.
type ZLibraryMCPFeature struct {
	store       *featureStore
	runtime     mcpRuntime
	storageRoot string
	keyPair     *rsaKeyPair
}

func (f *ZLibraryMCPFeature) Name() string { return featureName }

func (f *ZLibraryMCPFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	root, err := resolveStorageRoot(deps.DataDir, deps.Workspace)
	if err != nil {
		return fmt.Errorf("%s storage: %w", featureName, err)
	}
	keyPair, err := ensureRSAKeyPair(root)
	if err != nil {
		return fmt.Errorf("%s rsa keypair: %w", featureName, err)
	}

	f.storageRoot = root
	f.keyPair = keyPair
	f.store = &featureStore{db: deps.Stores.DB}
	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}
	f.store.setStateBestEffort(context.Background(), "storage_root", root)
	f.store.setStateBestEffort(context.Background(), "public_key_path", keyPair.PublicPath)

	if f.runtime == nil {
		f.runtime = newZLibraryRuntime(root, &serverInstaller{
			root:         root,
			repoURL:      upstreamRepoURL,
			setupTimeout: defaultSetupTimeout,
			runner:       osCommandRunner{},
		}, defaultMCPConnector)
	}

	topicrouting.RegisterTopicFeatureTools(featureName, toolPublicKeyName, toolSearchBooksName, toolDownloadBookName)
	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&publicKeyTool{feature: f})
		deps.ToolRegistry.Register(&searchBooksTool{feature: f})
		deps.ToolRegistry.Register(&downloadBookTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	slog.Info("beta zlibrary mcp initialized", "storage_root", root)
	return nil
}

func (f *ZLibraryMCPFeature) Shutdown(ctx context.Context) error {
	topicrouting.UnregisterTopicFeatureTools(featureName)
	if f != nil && f.runtime != nil {
		return f.runtime.Close(ctx)
	}
	return nil
}

func (f *ZLibraryMCPFeature) publicKey() (*PublicKeyPayload, error) {
	if f == nil || f.keyPair == nil {
		return nil, fmt.Errorf("%s keypair is unavailable", featureName)
	}
	return &PublicKeyPayload{
		Algorithm: "RSA",
		Format:    "PEM",
		PublicKey: f.keyPair.PublicPEM,
	}, nil
}

func (f *ZLibraryMCPFeature) status() StatusPayload {
	payload := StatusPayload{
		Feature:      featureName,
		StorageRoot:  f.storageRoot,
		PublicKeySet: f.keyPair != nil && f.keyPair.PublicPEM != "",
	}
	if f != nil && f.runtime != nil {
		payload.Runtime = f.runtime.Status()
	}
	return payload
}

func (f *ZLibraryMCPFeature) searchBooks(ctx context.Context, tenantID string, req SearchBooksRequest) (*MCPToolPayload, error) {
	args, err := normalizeSearchBooksRequest(req)
	if err != nil {
		return nil, err
	}
	return f.callMCP(ctx, tenantID, requiredSearchMCPTool, args)
}

func (f *ZLibraryMCPFeature) downloadBook(ctx context.Context, tenantID string, req DownloadBookRequest) (*MCPToolPayload, error) {
	if f == nil || f.runtime == nil {
		return nil, fmt.Errorf("%s runtime is unavailable", featureName)
	}
	args, err := normalizeDownloadBookRequest(req, f.runtime.DownloadRoot())
	if err != nil {
		return nil, err
	}
	return f.callMCP(ctx, tenantID, requiredDownloadTool, args)
}

func (f *ZLibraryMCPFeature) callMCP(ctx context.Context, tenantID, mcpTool string, args map[string]any) (*MCPToolPayload, error) {
	if f == nil || f.runtime == nil {
		return nil, fmt.Errorf("%s runtime is unavailable", featureName)
	}
	payload, err := f.runtime.CallTool(ctx, mcpTool, args)
	if err != nil {
		f.persistCallBestEffort(tenantID, mcpTool, statusFail, err.Error(), nil)
		return nil, err
	}
	status := statusOK
	errMessage := ""
	if payload != nil && payload.IsError {
		status = statusFail
		errMessage = payload.Text
	}
	f.persistCallBestEffort(tenantID, mcpTool, status, errMessage, payload)
	return payload, nil
}

func (f *ZLibraryMCPFeature) persistCallBestEffort(tenantID, toolName, status, errMessage string, payload *MCPToolPayload) {
	if f == nil || f.store == nil {
		return
	}
	if err := f.store.insertCall(&callRecord{
		TenantID:     tenantID,
		ToolName:     toolName,
		Status:       status,
		ErrorMessage: errMessage,
		Response:     mustJSON(payload),
	}); err != nil {
		slog.Debug("beta zlibrary mcp call persistence failed", "tool", toolName, "error", err)
	}
}
