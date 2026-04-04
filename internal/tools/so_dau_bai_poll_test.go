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
	}, nil)

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
	}, nil)

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
	if active == nil || active.PollID != "poll-1" || active.MessageID != 77 || active.Action != sodaubai.PollActionAdd {
		t.Fatalf("FindActiveByTarget() = %+v, want tracked poll", active)
	}
}

func TestCreateSoDauBaiPollToolSkipsAlreadyBlockedTarget(t *testing.T) {
	soSvc := sodaubai.NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	pollSvc := sodaubai.NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	tool := NewCreateSoDauBaiPollTool(soSvc, pollSvc, func(channel string) SoDauBaiPollCreator {
		return &fakeSoDauBaiPollCreator{pollID: "poll-2", messageID: 78}
	}, nil)

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

func TestCreateSoDauBaiPardonPollToolCreatesAndTracksPoll(t *testing.T) {
	soSvc := sodaubai.NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	pollSvc := sodaubai.NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	creator := &fakeSoDauBaiPollCreator{pollID: "poll-3", messageID: 79}
	tool := NewCreateSoDauBaiPardonPollTool(soSvc, pollSvc, func(channel string) SoDauBaiPollCreator {
		if channel == "telegram-main" {
			return creator
		}
		return nil
	}, nil)

	if _, _, err := soSvc.AddToday("@alice", "@duyvt6663", "spam meme"); err != nil {
		t.Fatalf("AddToday() error = %v", err)
	}

	ctx := WithToolChannel(context.Background(), "telegram-main")
	ctx = WithToolChatID(ctx, "-100123")
	ctx = WithToolLocalKey(ctx, "-100123:topic:9")
	ctx = WithToolPeerKind(ctx, "group")

	result := tool.Execute(ctx, map[string]any{
		"target": "@alice",
		"reason": "biet quay dau la bo",
	})
	if result.IsError {
		t.Fatalf("Execute() error = %s", result.ForLLM)
	}
	if creator.chatID != -100123 || creator.threadID != 9 {
		t.Fatalf("CreateSoDauBaiPoll called with chat/thread = %d/%d", creator.chatID, creator.threadID)
	}
	if !strings.Contains(strings.ToLower(creator.question), "khỏi sổ đầu bài") {
		t.Fatalf("question = %q, want pardon wording", creator.question)
	}
	if creator.yes != "Tha ra ngoài" || creator.no != "Ngồi lại sổ" {
		t.Fatalf("options = %q / %q, want pardon options", creator.yes, creator.no)
	}

	active, err := pollSvc.FindActiveByTarget(sodaubai.ScopeKey("telegram-main", "-100123:topic:9", "-100123"), "@alice")
	if err != nil {
		t.Fatalf("FindActiveByTarget() error = %v", err)
	}
	if active == nil || active.PollID != "poll-3" || active.Action != sodaubai.PollActionRemove {
		t.Fatalf("FindActiveByTarget() = %+v, want tracked pardon poll", active)
	}
}

func TestCreateSoDauBaiPardonPollToolAllowsOppositeActionPollForSameTarget(t *testing.T) {
	soSvc := sodaubai.NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	pollSvc := sodaubai.NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	creator := &fakeSoDauBaiPollCreator{pollID: "poll-pardon", messageID: 82}
	tool := NewCreateSoDauBaiPardonPollTool(soSvc, pollSvc, func(channel string) SoDauBaiPollCreator {
		if channel == "telegram-main" {
			return creator
		}
		return nil
	}, nil)

	if _, _, err := soSvc.AddToday("@alice", "@duyvt6663", "manual add"); err != nil {
		t.Fatalf("AddToday() error = %v", err)
	}
	if _, err := pollSvc.CreatePoll(sodaubai.PollCreate{
		PollID:        "poll-add",
		Action:        sodaubai.PollActionAdd,
		Scope:         sodaubai.ScopeKey("telegram-main", "-100123:topic:9", "-100123"),
		Channel:       "telegram-main",
		ChatID:        "-100123",
		LocalKey:      "-100123:topic:9",
		ThreadID:      9,
		MessageID:     81,
		Target:        "@alice",
		TargetDisplay: "@alice",
		Question:      "Cho @alice vào sổ đầu bài không?",
		Threshold:     5,
	}); err != nil {
		t.Fatalf("CreatePoll(add) error = %v", err)
	}

	ctx := WithToolChannel(context.Background(), "telegram-main")
	ctx = WithToolChatID(ctx, "-100123")
	ctx = WithToolLocalKey(ctx, "-100123:topic:9")
	ctx = WithToolPeerKind(ctx, "group")

	result := tool.Execute(ctx, map[string]any{"target": "@alice"})
	if result.IsError {
		t.Fatalf("Execute() error = %s", result.ForLLM)
	}
	if creator.chatID != -100123 || creator.threadID != 9 {
		t.Fatalf("CreateSoDauBaiPoll called with chat/thread = %d/%d", creator.chatID, creator.threadID)
	}
}

func TestCreateSoDauBaiPardonPollToolReusesActivePollAcrossTopics(t *testing.T) {
	soSvc := sodaubai.NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	pollSvc := sodaubai.NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	creator := &fakeSoDauBaiPollCreator{pollID: "poll-pardon", messageID: 82}
	tool := NewCreateSoDauBaiPardonPollTool(soSvc, pollSvc, func(channel string) SoDauBaiPollCreator {
		if channel == "telegram-main" {
			return creator
		}
		return nil
	}, nil)

	if _, _, err := soSvc.AddToday("@alice", "@duyvt6663", "manual add"); err != nil {
		t.Fatalf("AddToday() error = %v", err)
	}
	if _, err := pollSvc.CreatePoll(sodaubai.PollCreate{
		PollID:        "poll-topic-9",
		Action:        sodaubai.PollActionRemove,
		Scope:         sodaubai.ScopeKey("telegram-main", "-100123:topic:9", "-100123"),
		Channel:       "telegram-main",
		ChatID:        "-100123",
		LocalKey:      "-100123:topic:9",
		ThreadID:      9,
		MessageID:     81,
		Target:        "@alice",
		TargetDisplay: "@alice",
		Question:      "Tha @alice khỏi sổ đầu bài không?",
		Threshold:     5,
	}); err != nil {
		t.Fatalf("CreatePoll(existing) error = %v", err)
	}

	ctx := WithToolChannel(context.Background(), "telegram-main")
	ctx = WithToolChatID(ctx, "-100123")
	ctx = WithToolLocalKey(ctx, "-100123:topic:12")
	ctx = WithToolPeerKind(ctx, "group")

	result := tool.Execute(ctx, map[string]any{"target": "@alice"})
	if result.IsError {
		t.Fatalf("Execute() error = %s", result.ForLLM)
	}
	if creator.chatID != 0 || creator.threadID != 0 || creator.question != "" {
		t.Fatalf("CreateSoDauBaiPoll should not be called when reusing an active poll, got chat/thread/question = %d/%d/%q", creator.chatID, creator.threadID, creator.question)
	}
	if !strings.Contains(result.ForLLM, "topic 9") || !strings.Contains(result.ForLLM, "Reusing that poll instead of opening another") {
		t.Fatalf("Execute() = %q, want cross-topic reuse message", result.ForLLM)
	}
}

func TestCreateSoDauBaiPardonPollToolRequiresExistingEntry(t *testing.T) {
	soSvc := sodaubai.NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	pollSvc := sodaubai.NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	tool := NewCreateSoDauBaiPardonPollTool(soSvc, pollSvc, func(channel string) SoDauBaiPollCreator {
		return &fakeSoDauBaiPollCreator{pollID: "poll-4", messageID: 80}
	}, nil)

	ctx := WithToolChannel(context.Background(), "telegram-main")
	ctx = WithToolChatID(ctx, "-100123")
	ctx = WithToolLocalKey(ctx, "-100123")
	ctx = WithToolPeerKind(ctx, "group")

	result := tool.Execute(ctx, map[string]any{"target": "@alice"})
	if result.IsError {
		t.Fatalf("Execute() error = %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "not currently in today's sổ đầu bài") {
		t.Fatalf("Execute() = %q, want not-in-list message", result.ForLLM)
	}
}

func TestCreateSoDauBaiPardonPollToolRejectsAlwaysDeniedTarget(t *testing.T) {
	soSvc := sodaubai.NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	pollSvc := sodaubai.NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	tool := NewCreateSoDauBaiPardonPollTool(soSvc, pollSvc, func(channel string) SoDauBaiPollCreator {
		return &fakeSoDauBaiPollCreator{pollID: "poll-5", messageID: 81}
	}, nil)

	scope := sodaubai.ScopeKey("telegram-main", "-100123", "-100123")
	soSvc.SetAlways(scope, []string{"@alice"})

	ctx := WithToolChannel(context.Background(), "telegram-main")
	ctx = WithToolChatID(ctx, "-100123")
	ctx = WithToolLocalKey(ctx, "-100123")
	ctx = WithToolPeerKind(ctx, "group")

	result := tool.Execute(ctx, map[string]any{"target": "@alice"})
	if !result.IsError || !strings.Contains(result.ForLLM, "cannot vote them out") {
		t.Fatalf("Execute() = %+v, want deny_from refusal", result)
	}
}
