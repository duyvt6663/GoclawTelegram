package mcp

import "testing"

func TestBridgeToolNamesIncludesSkynetWorkflowTools(t *testing.T) {
	for _, name := range []string{
		"skynet_workflows",
		"skynet_backlog",
		"skynet_experiments",
		"skynet_qa",
		"skynet_pr",
		"skynet_ci_failure",
		"skynet_feedback_plan",
	} {
		if !BridgeToolNames[name] {
			t.Fatalf("BridgeToolNames is missing %q", name)
		}
	}
}
