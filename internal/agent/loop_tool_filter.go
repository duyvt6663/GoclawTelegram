package agent

import (
	"slices"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// buildFilteredTools resolves the per-iteration tool definitions based on policy,
// disabled tools, bootstrap mode, skill visibility, channel type, and iteration budget.
// Per-user MCP tools must be registered in the Registry before calling this function
// (via getUserMCPTools) so they are included in policy filtering and execution.
// Returns tool definitions for the provider, an allowed-tools map for execution validation,
// and the (potentially modified) messages slice when final-iteration stripping appends a hint.
func (l *Loop) buildFilteredTools(req *RunRequest, topicHiddenTools map[string]bool, hadBootstrap bool, iteration, maxIter int, messages []providers.Message) ([]providers.ToolDefinition, map[string]bool, []providers.Message) {
	// Build provider request with policy-filtered tools.
	var toolDefs []providers.ToolDefinition
	if l.toolPolicy != nil {
		toolDefs = l.toolPolicy.FilterTools(l.tools, l.id, l.provider.Name(), l.agentToolPolicy, req.ToolAllow, false, false)
	} else {
		toolDefs = l.tools.ProviderDefs()
	}

	// Per-tenant tool exclusions: remove tools disabled for this agent's tenant.
	if len(l.disabledTools) > 0 {
		filtered := toolDefs[:0:0]
		for _, td := range toolDefs {
			if !l.disabledTools[td.Function.Name] {
				filtered = append(filtered, td)
			}
		}
		toolDefs = filtered
	}

	// Topic-scoped beta routing: hide feature-owned tools that are disabled for
	// the current chat/topic while leaving unrelated global tools available.
	if len(topicHiddenTools) > 0 {
		aliases := l.tools.Aliases()
		filtered := toolDefs[:0:0]
		for _, td := range toolDefs {
			name := td.Function.Name
			canonical := name
			if resolved, ok := aliases[name]; ok && resolved != "" {
				canonical = resolved
			}
			if topicHiddenTools[name] || topicHiddenTools[canonical] {
				continue
			}
			filtered = append(filtered, td)
		}
		toolDefs = filtered
	}

	// Bootstrap mode: restrict API tool definitions to write_file only (open agents).
	// Predefined agents keep all tools — BOOTSTRAP.md guides behavior.
	if hadBootstrap && l.agentType != store.AgentTypePredefined {
		var bootstrapDefs []providers.ToolDefinition
		for _, td := range toolDefs {
			if bootstrapToolAllowlist[td.Function.Name] {
				bootstrapDefs = append(bootstrapDefs, td)
			}
		}
		toolDefs = bootstrapDefs
	}

	// Hide skill_manage from LLM when skill_evolve is off.
	// Tool stays in the registry (shared) but won't appear in API tool definitions.
	if !l.skillEvolve {
		filtered := toolDefs[:0:0]
		for _, td := range toolDefs {
			if td.Function.Name != "skill_manage" {
				filtered = append(filtered, td)
			}
		}
		toolDefs = filtered
	}

	// Hide channel-specific tools when channel type doesn't match.
	if req.ChannelType != "" {
		filtered := toolDefs[:0:0]
		for _, td := range toolDefs {
			if tool, ok := l.tools.Get(td.Function.Name); ok {
				if ca, ok := tool.(tools.ChannelAware); ok {
					if !slices.Contains(ca.RequiredChannelTypes(), req.ChannelType) {
						continue
					}
				}
			}
			filtered = append(filtered, td)
		}
		toolDefs = filtered
	}

	// Final iteration: strip all tools to force a text-only response.
	// Without this the model may keep requesting tools and exit with "...".
	if iteration == maxIter {
		toolDefs = nil
		messages = append(messages, providers.Message{
			Role:    "user",
			Content: "[System] Final iteration reached. Summarize all findings and respond to the user now. No more tool calls allowed.",
		})
	}

	allowedTools := make(map[string]bool, len(toolDefs))
	for _, td := range toolDefs {
		allowedTools[td.Function.Name] = true
	}

	return toolDefs, allowedTools, messages
}
