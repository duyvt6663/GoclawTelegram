package agent

// resolveToolChoiceForIteration allows per-request tool choice forcing on the
// first model pass only. Subsequent iterations must be free to finalize after a
// successful tool result instead of being forced into another tool call.
func resolveToolChoiceForIteration(toolChoice string, iteration int) string {
	if toolChoice == "" {
		return ""
	}
	if iteration > 0 {
		return ""
	}
	return toolChoice
}
