// Package example is a reference beta feature implementation.
// Enable with GOCLAW_BETA_EXAMPLE=1.
//
// It registers:
//   - One agent tool ("beta_example_echo") that echoes input
//   - One RPC method ("beta.example.ping") that returns pong
//
// Copy this folder as a starting point for new beta features.
package example

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// ExampleFeature demonstrates the beta feature pattern.
type ExampleFeature struct{}

func (f *ExampleFeature) Name() string { return "example" }

func (f *ExampleFeature) Init(deps beta.Deps) error {
	// Register an agent tool
	deps.ToolRegistry.Register(&echoTool{})

	// Register an RPC method
	deps.MethodRouter.Register("beta.example.ping", func(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
		client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"message": "pong"}))
	})

	return nil
}

// echoTool is a minimal tool that echoes the input text.
type echoTool struct{}

func (t *echoTool) Name() string        { return "beta_example_echo" }
func (t *echoTool) Description() string { return "Echo the input text back (beta example)" }
func (t *echoTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string", "description": "Text to echo"},
		},
		"required": []string{"text"},
	}
}

func (t *echoTool) Execute(_ context.Context, args map[string]any) *tools.Result {
	text, _ := args["text"].(string)
	return tools.NewResult("Echo: " + text)
}
