package cmd

import "testing"

func TestSelectToolChoiceForReactionMedia(t *testing.T) {
	got := selectToolChoice(map[string]string{
		implicitReactionMediaMetadata: "true",
	})
	if got != toolChoiceRequired {
		t.Fatalf("selectToolChoice(reaction) = %q, want %q", got, toolChoiceRequired)
	}
}

func TestSelectToolChoiceForExplicitSoDauBaiPardonPoll(t *testing.T) {
	got := selectToolChoice(map[string]string{
		explicitSoDauBaiPollActionMetadata: "remove",
		implicitReactionMediaMetadata:      "true",
	})
	if got != toolChoiceFunctionPrefix+createSoDauBaiPardonPollToolName {
		t.Fatalf("selectToolChoice(remove) = %q", got)
	}
}

func TestSelectToolChoiceForExplicitSoDauBaiAddPoll(t *testing.T) {
	got := selectToolChoice(map[string]string{
		explicitSoDauBaiPollActionMetadata: "add",
	})
	if got != toolChoiceFunctionPrefix+createSoDauBaiPollToolName {
		t.Fatalf("selectToolChoice(add) = %q", got)
	}
}
