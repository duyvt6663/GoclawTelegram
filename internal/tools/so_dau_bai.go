package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/sodaubai"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	soDauBaiListToolName   = "so_dau_bai_today"
	soDauBaiManageToolName = "so_dau_bai_manage"
	soDauBaiLopTruong      = "@duyvt6663"
)

type SoDauBaiTodayTool struct {
	service *sodaubai.Service
}

func NewSoDauBaiTodayTool(service *sodaubai.Service) *SoDauBaiTodayTool {
	return &SoDauBaiTodayTool{service: service}
}

func (t *SoDauBaiTodayTool) Name() string { return soDauBaiListToolName }

func (t *SoDauBaiTodayTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *SoDauBaiTodayTool) Description() string {
	return "Show today's sổ đầu bài: the temporary Telegram block list for the current local day. This list resets automatically when the local date changes."
}

func (t *SoDauBaiTodayTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *SoDauBaiTodayTool) Execute(ctx context.Context, _ map[string]any) *Result {
	if t.service == nil {
		return ErrorResult("so_dau_bai service is not configured")
	}
	state, err := t.service.TodayForScope(soDauBaiScopeFromCtx(ctx))
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read today's so_dau_bai: %v", err))
	}
	return NewResult(formatSoDauBaiToday(state))
}

type SoDauBaiManageTool struct {
	service  *sodaubai.Service
	contacts store.ContactStore
}

func NewSoDauBaiManageTool(service *sodaubai.Service, contacts store.ContactStore) *SoDauBaiManageTool {
	return &SoDauBaiManageTool{service: service, contacts: contacts}
}

func (t *SoDauBaiManageTool) Name() string { return soDauBaiManageToolName }

func (t *SoDauBaiManageTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *SoDauBaiManageTool) Description() string {
	return "Add or remove someone from today's sổ đầu bài, the temporary Telegram ignore list for the current local day. Only @duyvt6663, the lớp trưởng, is allowed to use this tool."
}

func (t *SoDauBaiManageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"add", "remove"},
				"description": "Whether to add or remove someone from today's so_dau_bai.",
			},
			"target": map[string]any{
				"type":        "string",
				"description": "Telegram target to update. Use @username, numeric user ID, a raw sender form like 123456|username, or a known display name/nickname from this Telegram chat.",
			},
			"note": map[string]any{
				"type":        "string",
				"description": "Optional short reason or note for why this person is on today's so_dau_bai.",
			},
		},
		"required": []string{"action", "target"},
	}
}

func (t *SoDauBaiManageTool) Execute(ctx context.Context, args map[string]any) *Result {
	if t.service == nil {
		return ErrorResult("so_dau_bai service is not configured")
	}
	if err := ensureSoDauBaiLopTruong(ctx); err != nil {
		return ErrorResult(err.Error())
	}

	action := strings.TrimSpace(GetParamString(args, "action", ""))
	target := strings.TrimSpace(GetParamString(args, "target", ""))
	note := strings.TrimSpace(GetParamString(args, "note", ""))
	if action == "" {
		return ErrorResult("action is required")
	}
	if target == "" {
		return ErrorResult("target is required")
	}
	target = resolveTelegramTarget(ctx, t.contacts, target)

	sender := prettyTelegramSender(store.SenderIDFromContext(ctx))
	scope := soDauBaiScopeFromCtx(ctx)
	switch action {
	case "add":
		if t.service.HasAlways(scope, target) {
			state, err := t.service.TodayForScope(scope)
			if err != nil {
				return ErrorResult(fmt.Sprintf("failed to read today's so_dau_bai: %v", err))
			}
			return NewResult(fmt.Sprintf("%s is already always in today's sổ đầu bài via deny_from.\n\n%s", target, formatSoDauBaiToday(state)))
		}
		entry, added, err := t.service.AddToday(target, sender, note)
		if err != nil {
			return ErrorResult(fmt.Sprintf("failed to update so_dau_bai: %v", err))
		}
		if !added {
			return NewResult(fmt.Sprintf("%s is already in today's sổ đầu bài.\n\n%s", entry.Target, formatSoDauBaiTodayFromEntry(entry)))
		}
		return NewResult(fmt.Sprintf("Added %s to today's sổ đầu bài.\n\n%s", entry.Target, formatSoDauBaiTodayFromEntry(entry)))
	case "remove":
		if t.service.HasAlways(scope, target) {
			return ErrorResult(fmt.Sprintf("%s is in deny_from, so it always stays in today's sổ đầu bài.", target))
		}
		entry, removed, err := t.service.RemoveToday(target)
		if err != nil {
			return ErrorResult(fmt.Sprintf("failed to update so_dau_bai: %v", err))
		}
		if !removed || entry == nil {
			return NewResult(fmt.Sprintf("%s was not in today's sổ đầu bài.", target))
		}
		return NewResult(fmt.Sprintf("Removed %s from today's sổ đầu bài.", entry.Target))
	default:
		return ErrorResult(fmt.Sprintf("unknown action %q — use add or remove", action))
	}
}

func ensureSoDauBaiLopTruong(ctx context.Context) error {
	senderID := store.SenderIDFromContext(ctx)
	if senderID == "" {
		return fmt.Errorf("only %s can update today's sổ đầu bài", soDauBaiLopTruong)
	}
	if channels.SenderMatchesList(senderID, []string{soDauBaiLopTruong}) {
		return nil
	}
	return fmt.Errorf("only %s (lớp trưởng) can update today's sổ đầu bài", soDauBaiLopTruong)
}

func prettyTelegramSender(senderID string) string {
	senderID = strings.TrimSpace(senderID)
	if senderID == "" {
		return soDauBaiLopTruong
	}
	if idx := strings.Index(senderID, "|"); idx >= 0 && idx+1 < len(senderID) {
		user := strings.TrimSpace(senderID[idx+1:])
		if user != "" {
			return "@" + strings.TrimPrefix(user, "@")
		}
	}
	return senderID
}

func soDauBaiScopeFromCtx(ctx context.Context) string {
	return sodaubai.ScopeKey(ToolChannelFromCtx(ctx), ToolLocalKeyFromCtx(ctx), ToolChatIDFromCtx(ctx))
}

func formatSoDauBaiToday(state sodaubai.State) string {
	dateLabel := state.Date
	if dateLabel == "" {
		dateLabel = "today"
	}
	if len(state.Entries) == 0 {
		return fmt.Sprintf("Today's sổ đầu bài (%s) is empty.", dateLabel)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Today's sổ đầu bài (%s): %d blocked user(s).", dateLabel, len(state.Entries))
	for i, entry := range state.Entries {
		fmt.Fprintf(&b, "\n%d. %s", i+1, entry.Target)
		if entry.Note != "" {
			fmt.Fprintf(&b, " — %s", entry.Note)
		}
		if entry.AddedBy != "" {
			fmt.Fprintf(&b, " (added by %s", entry.AddedBy)
			if entry.AddedAt != "" {
				fmt.Fprintf(&b, " at %s", entry.AddedAt)
			}
			b.WriteString(")")
		} else if entry.AddedAt != "" {
			fmt.Fprintf(&b, " (added at %s)", entry.AddedAt)
		}
	}
	return b.String()
}

func formatSoDauBaiTodayFromEntry(entry sodaubai.Entry) string {
	state := sodaubai.State{
		Date:    entry.AddedDay,
		Entries: []sodaubai.Entry{entry},
	}
	return formatSoDauBaiToday(state)
}
