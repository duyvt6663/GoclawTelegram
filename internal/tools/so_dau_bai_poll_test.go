package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/sodaubai"
)

type fakeSoDauBaiPollCreator struct {
	pollID    string
	messageID int
	chatID    int64
	threadID  int
	question  string
	yes       string
	no        string
	err       error
}

func (f *fakeSoDauBaiPollCreator) CreateSoDauBaiPoll(_ context.Context, chatID int64, threadID int, question, yesOption, noOption string, _ int) (string, int, error) {
	f.chatID = chatID
	f.threadID = threadID
	f.question = question
	f.yes = yesOption
	f.no = noOption
	if f.err != nil {
		return "", 0, f.err
	}
	return f.pollID, f.messageID, nil
}

func TestCreateSoDauBaiPollToolRequiresGroup(t *testing.T) {
	soSvc := sodaubai.NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	pollSvc := sodaubai.NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	creator := &fakeSoDauBaiPollCreator{pollID: "poll-1", messageID: 77}
	tool := NewCreateSoDauBaiPollTool(soSvc, pollSvc, func(channel string) SoDauBaiPollCreator {
		if channel == "telegram-main" {
			return creator
		}
		return nil
	})

	ctx := WithToolChannel(context.Background(), "telegram-main")
	ctx = WithToolChatID(ctx, "-100123")
	ctx = WithToolPeerKind(ctx, "direct")
	result := tool.Execute(ctx, map[string]any{"target": "@alice"})
	if !result.IsError || !strings.Contains(result.ForLLM, "group chats") {
		t.Fatalf("Execute() = %+v, want group-only error", result)
	}
}

func TestCreateSoDauBaiPollToolCreatesAndTracksPoll(t *testing.T) {
	soSvc := sodaubai.NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	pollSvc := sodaubai.NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	creator := &fakeSoDauBaiPollCreator{pollID: "poll-1", messageID: 77}
	tool := NewCreateSoDauBaiPollTool(soSvc, pollSvc, func(channel string) SoDauBaiPollCreator {
		if channel == "telegram-main" {
			return creator
		}
		return nil
	})

	ctx := WithToolChannel(context.Background(), "telegram-main")
	ctx = WithToolChatID(ctx, "-100123")
	ctx = WithToolLocalKey(ctx, "-100123:topic:9")
	ctx = WithToolPeerKind(ctx, "group")

	result := tool.Execute(ctx, map[string]any{
		"target": "@alice",
		"reason": "noi nhieu qua",
	})
	if result.IsError {
		t.Fatalf("Execute() error = %s", result.ForLLM)
	}
	if creator.chatID != -100123 || creator.threadID != 9 {
		t.Fatalf("CreateSoDauBaiPoll called with chat/thread = %d/%d", creator.chatID, creator.threadID)
	}
	if !strings.Contains(creator.question, "@alice") || !strings.Contains(strings.ToLower(creator.question), "sổ đầu bài") {
		t.Fatalf("question = %q, want target + so dau bai wording", creator.question)
	}

	active, err := pollSvc.FindActiveByTarget(sodaubai.ScopeKey("telegram-main", "-100123:topic:9", "-100123"), "@alice")
	if err != nil {
		t.Fatalf("FindActiveByTarget() error = %v", err)
	}
	if active == nil || active.PollID != "poll-1" || active.MessageID != 77 {
		t.Fatalf("FindActiveByTarget() = %+v, want tracked poll", active)
	}
}

func TestCreateSoDauBaiPollToolSkipsAlreadyBlockedTarget(t *testing.T) {
	soSvc := sodaubai.NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	pollSvc := sodaubai.NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	tool := NewCreateSoDauBaiPollTool(soSvc, pollSvc, func(channel string) SoDauBaiPollCreator {
		return &fakeSoDauBaiPollCreator{pollID: "poll-2", messageID: 78}
	})

	scope := sodaubai.ScopeKey("telegram-main", "-100123", "-100123")
	soSvc.SetAlways(scope, []string{"@alice"})

	ctx := WithToolChannel(context.Background(), "telegram-main")
	ctx = WithToolChatID(ctx, "-100123")
	ctx = WithToolLocalKey(ctx, "-100123")
	ctx = WithToolPeerKind(ctx, "group")

	result := tool.Execute(ctx, map[string]any{"target": "@alice"})
	if result.IsError {
		t.Fatalf("Execute() error = %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "already in today's sổ đầu bài") {
		t.Fatalf("Execute() = %q, want already-blocked message", result.ForLLM)
	}
}
