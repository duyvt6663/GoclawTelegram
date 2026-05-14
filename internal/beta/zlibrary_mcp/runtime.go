package zlibrarymcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

type mcpRuntime interface {
	CallTool(ctx context.Context, toolName string, args map[string]any) (*MCPToolPayload, error)
	Status() RuntimeStatus
	DownloadRoot() string
	Close(ctx context.Context) error
}

type mcpSession interface {
	CallTool(ctx context.Context, toolName string, args map[string]any) (*MCPToolPayload, error)
	Tools() []string
	Close() error
}

type mcpConnector func(ctx context.Context, spec *runtimeSpec) (mcpSession, error)

type zlibraryRuntime struct {
	mu        sync.Mutex
	root      string
	installer *serverInstaller
	connect   mcpConnector
	session   mcpSession
	status    RuntimeStatus
}

func newZLibraryRuntime(root string, installer *serverInstaller, connector mcpConnector) *zlibraryRuntime {
	downloadRoot := filepath.Join(root, "downloads")
	return &zlibraryRuntime{
		root:      root,
		installer: installer,
		connect:   connector,
		status: RuntimeStatus{
			DownloadRoot: downloadRoot,
		},
	}
}

func (r *zlibraryRuntime) CallTool(ctx context.Context, toolName string, args map[string]any) (*MCPToolPayload, error) {
	session, err := r.ensureSession(ctx)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	payload, err := session.CallTool(callCtx, toolName, args)
	if err != nil {
		r.mu.Lock()
		_ = session.Close()
		if r.session == session {
			r.session = nil
		}
		r.status.Connected = false
		r.status.LastError = err.Error()
		r.mu.Unlock()
		return nil, err
	}
	return payload, nil
}

func (r *zlibraryRuntime) Status() RuntimeStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.status
	out.Tools = append([]string(nil), out.Tools...)
	return out
}

func (r *zlibraryRuntime) DownloadRoot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.DownloadRoot != "" {
		return r.status.DownloadRoot
	}
	return filepath.Join(r.root, "downloads")
}

func (r *zlibraryRuntime) Close(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil {
		return nil
	}
	err := r.session.Close()
	r.session = nil
	r.status.Connected = false
	return err
}

func (r *zlibraryRuntime) ensureSession(ctx context.Context) (mcpSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != nil {
		return r.session, nil
	}
	if r.connect == nil {
		r.connect = defaultMCPConnector
	}

	spec, err := r.installer.Ensure(ctx)
	if err != nil {
		r.status.LastError = err.Error()
		return nil, err
	}
	r.status.Installed = true
	r.status.ServerDir = spec.ServerDir
	r.status.DownloadRoot = spec.DownloadRoot

	session, err := r.connect(ctx, spec)
	if err != nil {
		r.status.Connected = false
		r.status.LastError = err.Error()
		return nil, err
	}
	tools := session.Tools()
	if !slices.Contains(tools, requiredSearchMCPTool) || !slices.Contains(tools, requiredDownloadTool) {
		_ = session.Close()
		err := fmt.Errorf("zlibrary MCP missing required tools: %s, %s", requiredSearchMCPTool, requiredDownloadTool)
		r.status.Connected = false
		r.status.LastError = err.Error()
		r.status.Tools = tools
		return nil, err
	}

	r.session = session
	r.status.Connected = true
	r.status.LastError = ""
	r.status.Tools = tools
	return session, nil
}

type stdioMCPSession struct {
	client    *mcpclient.Client
	toolNames []string
	connected atomic.Bool
}

func defaultMCPConnector(ctx context.Context, spec *runtimeSpec) (mcpSession, error) {
	env := mapToEnvSlice(spec.Env)
	client, err := mcpclient.NewStdioMCPClientWithOptions(spec.Command, env, spec.Args, transport.WithCommandFunc(func(cmdCtx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(cmdCtx, command, args...)
		cmd.Dir = spec.WorkDir
		cmd.Env = append(os.Environ(), env...)
		return cmd, nil
	}))
	if err != nil {
		return nil, err
	}

	initReq := mcpgo.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpgo.Implementation{Name: "goclaw-zlibrary-mcp", Version: "1.0.0"}
	if _, err := client.Initialize(ctx, initReq); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("initialize zlibrary MCP: %w", err)
	}
	toolsResult, err := client.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("list zlibrary MCP tools: %w", err)
	}
	toolNames := make([]string, 0, len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		toolNames = append(toolNames, tool.Name)
	}
	session := &stdioMCPSession{client: client, toolNames: toolNames}
	session.connected.Store(true)
	return session, nil
}

func (s *stdioMCPSession) CallTool(ctx context.Context, toolName string, args map[string]any) (*MCPToolPayload, error) {
	if s == nil || s.client == nil || !s.connected.Load() {
		return nil, fmt.Errorf("zlibrary MCP session is disconnected")
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args
	result, err := s.client.CallTool(ctx, req)
	if err != nil {
		s.connected.Store(false)
		return nil, err
	}
	text := extractMCPTextContent(result)
	return &MCPToolPayload{
		Tool:              toolName,
		Text:              text,
		StructuredContent: result.StructuredContent,
		IsError:           result.IsError,
		CalledAt:          time.Now().UTC(),
	}, nil
}

func (s *stdioMCPSession) Tools() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.toolNames...)
}

func (s *stdioMCPSession) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	s.connected.Store(false)
	return s.client.Close()
}

func extractMCPTextContent(result *mcpgo.CallToolResult) string {
	if result == nil || len(result.Content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		switch value := content.(type) {
		case mcpgo.TextContent:
			parts = append(parts, value.Text)
		case *mcpgo.TextContent:
			parts = append(parts, value.Text)
		default:
			if data, err := json.Marshal(value); err == nil {
				parts = append(parts, string(data))
			} else {
				parts = append(parts, fmt.Sprintf("[non-text content: %T]", content))
			}
		}
	}
	return strings.Join(parts, "\n")
}
