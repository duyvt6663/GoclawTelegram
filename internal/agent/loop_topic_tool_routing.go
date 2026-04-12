package agent

import (
	"context"
	"strconv"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
)

func normalizeRunThreadID(threadID int) int {
	if threadID <= 1 {
		return 0
	}
	return threadID
}

func threadIDFromLocalKey(localKey string) int {
	localKey = strings.TrimSpace(localKey)
	if localKey == "" {
		return 0
	}

	var (
		raw string
		idx int
	)
	switch {
	case strings.Contains(localKey, ":topic:"):
		idx = strings.LastIndex(localKey, ":topic:")
		raw = localKey[idx+len(":topic:"):]
	case strings.Contains(localKey, ":thread:"):
		idx = strings.LastIndex(localKey, ":thread:")
		raw = localKey[idx+len(":thread:"):]
	default:
		return 0
	}

	threadID, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return normalizeRunThreadID(threadID)
}

func effectiveRunThreadID(req *RunRequest) int {
	if req == nil {
		return 0
	}
	if threadID := normalizeRunThreadID(req.ThreadID); threadID > 0 {
		return threadID
	}
	return threadIDFromLocalKey(req.LocalKey)
}

func hiddenToolSetFromDecision(decision *topicrouting.TopicToolDecision) map[string]bool {
	if decision == nil || !decision.Matched || len(decision.HiddenTools) == 0 {
		return nil
	}
	hidden := make(map[string]bool, len(decision.HiddenTools))
	for _, toolName := range decision.HiddenTools {
		toolName = strings.TrimSpace(toolName)
		if toolName == "" {
			continue
		}
		hidden[toolName] = true
	}
	if len(hidden) == 0 {
		return nil
	}
	return hidden
}

func (l *Loop) resolveTopicToolDecision(ctx context.Context, req *RunRequest) (*topicrouting.TopicToolDecision, error) {
	if req == nil {
		return nil, nil
	}
	scope := topicrouting.TopicToolScope{
		Channel:     strings.TrimSpace(req.Channel),
		ChannelType: strings.TrimSpace(req.ChannelType),
		ChatID:      strings.TrimSpace(req.ChatID),
		ThreadID:    effectiveRunThreadID(req),
		LocalKey:    strings.TrimSpace(req.LocalKey),
	}
	if scope.Channel == "" || scope.ChatID == "" {
		return nil, nil
	}
	return topicrouting.ResolveTopicToolDecision(ctx, scope)
}
