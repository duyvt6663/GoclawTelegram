package sodaubai

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPollServiceThresholdAndDayReset(t *testing.T) {
	svc := NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	svc.paths = []string{filepath.Join(t.TempDir(), "isolated-so-dau-bai-polls.json")}
	loc := time.FixedZone("UTC+7", 7*60*60)
	svc.loc = loc
	svc.now = func() time.Time {
		return time.Date(2026, time.April, 4, 9, 0, 0, 0, loc)
	}

	entry, err := svc.CreatePoll(PollCreate{
		PollID:        "poll-1",
		Action:        PollActionAdd,
		Scope:         ScopeKey("telegram-main", "-100123", "-100123"),
		Channel:       "telegram-main",
		ChatID:        "-100123",
		LocalKey:      "-100123",
		MessageID:     88,
		Target:        "@kryptonite2304",
		TargetDisplay: "@kryptonite2304",
		Reason:        "gây chuyện",
		Question:      "Cho @kryptonite2304 vào sổ đầu bài hôm nay không?",
		Threshold:     5,
	})
	if err != nil {
		t.Fatalf("CreatePoll() error = %v", err)
	}
	if entry.PollID != "poll-1" || entry.Threshold != 5 || entry.Action != PollActionAdd {
		t.Fatalf("CreatePoll() = %+v", entry)
	}

	for i := 1; i <= 4; i++ {
		result, err := svc.RecordVote("poll-1", voterID(i), []int{0})
		if err != nil {
			t.Fatalf("RecordVote(%d) error = %v", i, err)
		}
		if result.ThresholdReached {
			t.Fatalf("RecordVote(%d) threshold reached too early", i)
		}
	}

	result, err := svc.RecordVote("poll-1", voterID(5), []int{0})
	if err != nil {
		t.Fatalf("RecordVote(5) error = %v", err)
	}
	if !result.ThresholdReached || result.YesVotes != 5 || result.Poll == nil || !result.Poll.Resolved {
		t.Fatalf("RecordVote(5) = %+v, want resolved threshold at 5", result)
	}

	svc.now = func() time.Time {
		return time.Date(2026, time.April, 5, 8, 0, 0, 0, loc)
	}

	active, err := svc.FindActiveByTarget(ScopeKey("telegram-main", "-100123", "-100123"), "@kryptonite2304")
	if err != nil {
		t.Fatalf("FindActiveByTarget() after rollover error = %v", err)
	}
	if active != nil {
		t.Fatalf("FindActiveByTarget() after rollover = %+v, want nil", active)
	}
}

func TestPollServiceVoteChangeAndClose(t *testing.T) {
	svc := NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	svc.paths = []string{filepath.Join(t.TempDir(), "isolated-so-dau-bai-polls.json")}
	loc := time.FixedZone("UTC+7", 7*60*60)
	svc.loc = loc
	svc.now = func() time.Time {
		return time.Date(2026, time.April, 4, 13, 0, 0, 0, loc)
	}

	scope := ScopeKey("telegram-main", "-100123:topic:9", "-100123")
	if _, err := svc.CreatePoll(PollCreate{
		PollID:    "poll-2",
		Action:    PollActionRemove,
		Scope:     scope,
		Channel:   "telegram-main",
		ChatID:    "-100123",
		LocalKey:  "-100123:topic:9",
		ThreadID:  9,
		MessageID: 99,
		Target:    "@alice",
		Question:  "Cho @alice ra khỏi sổ đầu bài hôm nay không?",
		Threshold: 5,
	}); err != nil {
		t.Fatalf("CreatePoll() error = %v", err)
	}

	active, err := svc.FindActiveByTarget(scope, "@alice")
	if err != nil {
		t.Fatalf("FindActiveByTarget() error = %v", err)
	}
	if active == nil || active.Action != PollActionRemove {
		t.Fatalf("FindActiveByTarget() = %+v, want remove-action poll", active)
	}

	if _, err := svc.RecordVote("poll-2", "11", []int{0}); err != nil {
		t.Fatalf("RecordVote yes error = %v", err)
	}
	result, err := svc.RecordVote("poll-2", "11", []int{1})
	if err != nil {
		t.Fatalf("RecordVote change error = %v", err)
	}
	if result.YesVotes != 0 {
		t.Fatalf("RecordVote changed vote = %+v, want 0 yes votes", result)
	}

	closed, err := svc.MarkClosed("poll-2")
	if err != nil {
		t.Fatalf("MarkClosed() error = %v", err)
	}
	if closed == nil || !closed.Closed {
		t.Fatalf("MarkClosed() = %+v, want closed poll", closed)
	}
}

func TestPollServiceAllowsOppositeActionPollsForSameTarget(t *testing.T) {
	svc := NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	svc.paths = []string{filepath.Join(t.TempDir(), "isolated-so-dau-bai-polls.json")}
	loc := time.FixedZone("UTC+7", 7*60*60)
	svc.loc = loc
	svc.now = func() time.Time {
		return time.Date(2026, time.April, 4, 17, 0, 0, 0, loc)
	}

	scope := ScopeKey("telegram-main", "-100123:topic:1", "-100123")
	if _, err := svc.CreatePoll(PollCreate{
		PollID:        "poll-add",
		Action:        PollActionAdd,
		Scope:         scope,
		Channel:       "telegram-main",
		ChatID:        "-100123",
		LocalKey:      "-100123:topic:1",
		ThreadID:      1,
		MessageID:     100,
		Target:        "@alice",
		TargetDisplay: "@alice",
		Question:      "Cho @alice vào sổ đầu bài không?",
		Threshold:     5,
	}); err != nil {
		t.Fatalf("CreatePoll(add) error = %v", err)
	}

	if _, err := svc.CreatePoll(PollCreate{
		PollID:        "poll-remove",
		Action:        PollActionRemove,
		Scope:         scope,
		Channel:       "telegram-main",
		ChatID:        "-100123",
		LocalKey:      "-100123:topic:1",
		ThreadID:      1,
		MessageID:     101,
		Target:        "@alice",
		TargetDisplay: "@alice",
		Question:      "Tha @alice khỏi sổ đầu bài không?",
		Threshold:     5,
	}); err != nil {
		t.Fatalf("CreatePoll(remove) error = %v", err)
	}

	activeAdd, err := svc.FindActiveByTargetAction(scope, "@alice", PollActionAdd)
	if err != nil {
		t.Fatalf("FindActiveByTargetAction(add) error = %v", err)
	}
	if activeAdd == nil || activeAdd.PollID != "poll-add" {
		t.Fatalf("FindActiveByTargetAction(add) = %+v, want poll-add", activeAdd)
	}

	activeRemove, err := svc.FindActiveByTargetAction(scope, "@alice", PollActionRemove)
	if err != nil {
		t.Fatalf("FindActiveByTargetAction(remove) error = %v", err)
	}
	if activeRemove == nil || activeRemove.PollID != "poll-remove" {
		t.Fatalf("FindActiveByTargetAction(remove) = %+v, want poll-remove", activeRemove)
	}
}

func TestPollServiceReusesSameActionPollAcrossTopicsInSameChat(t *testing.T) {
	svc := NewPollService(filepath.Join(t.TempDir(), "so-dau-bai-polls.json"))
	svc.paths = []string{filepath.Join(t.TempDir(), "isolated-so-dau-bai-polls.json")}
	loc := time.FixedZone("UTC+7", 7*60*60)
	svc.loc = loc
	svc.now = func() time.Time {
		return time.Date(2026, time.April, 4, 18, 0, 0, 0, loc)
	}

	if _, err := svc.CreatePoll(PollCreate{
		PollID:        "poll-topic-9",
		Action:        PollActionRemove,
		Scope:         ScopeKey("telegram-main", "-100123:topic:9", "-100123"),
		Channel:       "telegram-main",
		ChatID:        "-100123",
		LocalKey:      "-100123:topic:9",
		ThreadID:      9,
		MessageID:     200,
		Target:        "@alice",
		TargetDisplay: "@alice",
		Question:      "Tha @alice khỏi sổ đầu bài không?",
		Threshold:     5,
	}); err != nil {
		t.Fatalf("CreatePoll(topic 9) error = %v", err)
	}

	active, err := svc.FindActiveByChatTargetAction("telegram-main", "-100123", "@alice", PollActionRemove)
	if err != nil {
		t.Fatalf("FindActiveByChatTargetAction() error = %v", err)
	}
	if active == nil || active.PollID != "poll-topic-9" || active.ThreadID != 9 {
		t.Fatalf("FindActiveByChatTargetAction() = %+v, want poll-topic-9 in topic 9", active)
	}

	if _, err := svc.CreatePoll(PollCreate{
		PollID:        "poll-topic-12",
		Action:        PollActionRemove,
		Scope:         ScopeKey("telegram-main", "-100123:topic:12", "-100123"),
		Channel:       "telegram-main",
		ChatID:        "-100123",
		LocalKey:      "-100123:topic:12",
		ThreadID:      12,
		MessageID:     201,
		Target:        "@alice",
		TargetDisplay: "@alice",
		Question:      "Tha @alice khỏi sổ đầu bài ở topic khác không?",
		Threshold:     5,
	}); err == nil {
		t.Fatal("CreatePoll(topic 12) error = nil, want duplicate-active-poll error across chat topics")
	}
}

func voterID(i int) string {
	return time.Date(2000, time.January, i, 0, 0, 0, 0, time.UTC).Format("20060102")
}
