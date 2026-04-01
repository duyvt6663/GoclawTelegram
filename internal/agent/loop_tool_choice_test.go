package agent

import "testing"

func TestResolveToolChoiceForIteration(t *testing.T) {
	if got := resolveToolChoiceForIteration("", 0); got != "" {
		t.Fatalf("empty tool choice = %q, want empty", got)
	}
	if got := resolveToolChoiceForIteration("required", 0); got != "required" {
		t.Fatalf("iteration 0 tool choice = %q, want required", got)
	}
	if got := resolveToolChoiceForIteration("required", 1); got != "" {
		t.Fatalf("iteration 1 tool choice = %q, want empty", got)
	}
	if got := resolveToolChoiceForIteration("required", 3); got != "" {
		t.Fatalf("iteration 3 tool choice = %q, want empty", got)
	}
}
