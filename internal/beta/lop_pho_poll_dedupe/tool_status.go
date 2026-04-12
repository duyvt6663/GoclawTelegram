package lopphopolldedupe

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type statusTool struct {
	feature *LopPhoPollDedupeFeature
}

func (t *statusTool) Name() string { return "lop_pho_poll_dedupe_status" }

func (t *statusTool) Description() string {
	return "Show recent lớp phó poll dedupe claims and duplicate suppressions for the current Telegram group/topic."
}

func (t *statusTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"chat_id":   map[string]any{"type": "string", "description": "Optional Telegram chat ID override."},
			"thread_id": map[string]any{"type": "integer", "description": "Optional Telegram topic/thread ID override."},
			"limit":     map[string]any{"type": "integer", "description": "Optional number of recent claims to return."},
		},
	}
}

func (t *statusTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("lớp phó poll dedupe feature is unavailable")
	}

	chatID := stringArg(args, "chat_id")
	threadID, hasThread := intArg(args, "thread_id")
	if chatID == "" {
		chatID, threadID = chatTargetFromToolContext(ctx)
		if threadID > 0 {
			hasThread = true
		}
	}
	filter := ClaimFilter{
		ChatID: strings.TrimSpace(chatID),
		Limit:  0,
	}
	if limit, ok := intArg(args, "limit"); ok {
		filter.Limit = limit
	}
	if hasThread {
		filter.ThreadID = normalizeThreadID(threadID)
		filter.HasThread = true
	}

	status, err := t.feature.statusSnapshot(ctx, tenantKeyFromCtx(ctx), filter)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	out, _ := json.Marshal(status)
	return tools.NewResult(string(out))
}
