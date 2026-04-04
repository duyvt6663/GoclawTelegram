package cmd

import (
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/sodaubai"
)

const (
	toolChoiceRequired                 = "required"
	toolChoiceFunctionPrefix           = "function:"
	createSoDauBaiPollToolName         = "create_so_dau_bai_poll"
	createSoDauBaiPardonPollToolName   = "create_so_dau_bai_pardon_poll"
	explicitSoDauBaiPollActionMetadata = "explicit_so_dau_bai_poll_action"
	implicitReactionMediaMetadata      = "implicit_reaction_media"
)

func selectToolChoice(metadata map[string]string) string {
	if toolName := soDauBaiToolChoiceName(metadata); toolName != "" {
		return toolChoiceFunctionPrefix + toolName
	}
	if metadata[implicitReactionMediaMetadata] == "true" {
		return toolChoiceRequired
	}
	return ""
}

func soDauBaiToolChoiceName(metadata map[string]string) string {
	action := strings.TrimSpace(metadata[explicitSoDauBaiPollActionMetadata])
	if action == "" {
		return ""
	}
	switch sodaubai.NormalizePollAction(action) {
	case sodaubai.PollActionAdd:
		return createSoDauBaiPollToolName
	case sodaubai.PollActionRemove:
		return createSoDauBaiPardonPollToolName
	default:
		return ""
	}
}
