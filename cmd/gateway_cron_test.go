package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/agent"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/scheduler"
	"github.com/nextlevelbuilder/goclaw/internal/sessions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type trackingCronSessionStore struct {
	*followupSessionStore
	resets int
	saves  int
}

func newTrackingCronSessionStore() *trackingCronSessionStore {
	return &trackingCronSessionStore{followupSessionStore: newFollowupSessionStore()}
}

func (s *trackingCronSessionStore) Reset(ctx context.Context, key string) {
	s.resets++
	s.followupSessionStore.Reset(ctx, key)
}

func (s *trackingCronSessionStore) Save(ctx context.Context, key string) error {
	s.saves++
	return s.followupSessionStore.Save(ctx, key)
}

func TestCronHandlerResetsStatelessSessions(t *testing.T) {
	sessStore := newTrackingCronSessionStore()
	sched := scheduler.NewScheduler(
		[]scheduler.LaneConfig{{Name: scheduler.LaneCron, Concurrency: 1}},
		scheduler.QueueConfig{Mode: scheduler.QueueModeQueue, Cap: 10, DebounceMs: 0, MaxConcurrent: 1},
		func(ctx context.Context, req agent.RunRequest) (*agent.RunResult, error) {
			base := sessions.BuildCronSessionKey("skynet-backlog-iterator", "job-1")
			if req.SessionKey == base || !strings.HasPrefix(req.SessionKey, base+":run:") {
				t.Fatalf("stateless session key = %q, want unique run key with prefix %q", req.SessionKey, base+":run:")
			}
			return &agent.RunResult{Content: "ok"}, nil
		},
	)
	defer sched.Stop()

	handler := makeCronJobHandler(sched, bus.New(), &config.Config{}, nil, sessStore, nil)
	job := &store.CronJob{
		ID:        "job-1",
		TenantID:  store.MasterTenantID,
		Name:      "stateless test",
		AgentID:   "skynet-backlog-iterator",
		UserID:    "system:test",
		Payload:   store.CronPayload{Kind: "agent_turn", Message: "run"},
		Stateless: true,
	}

	if _, err := handler(job); err != nil {
		t.Fatal(err)
	}
	if sessStore.resets != 1 {
		t.Fatalf("Reset calls = %d, want 1", sessStore.resets)
	}
	if sessStore.saves != 1 {
		t.Fatalf("Save calls = %d, want 1", sessStore.saves)
	}
}

func TestCronHandlerKeepsStatefulSessions(t *testing.T) {
	sessStore := newTrackingCronSessionStore()
	sched := scheduler.NewScheduler(
		[]scheduler.LaneConfig{{Name: scheduler.LaneCron, Concurrency: 1}},
		scheduler.QueueConfig{Mode: scheduler.QueueModeQueue, Cap: 10, DebounceMs: 0, MaxConcurrent: 1},
		func(ctx context.Context, req agent.RunRequest) (*agent.RunResult, error) {
			want := sessions.BuildCronSessionKey("skynet-backlog-iterator", "job-2")
			if req.SessionKey != want {
				t.Fatalf("session key = %q, want %q", req.SessionKey, want)
			}
			return &agent.RunResult{Content: "ok"}, nil
		},
	)
	defer sched.Stop()

	handler := makeCronJobHandler(sched, bus.New(), &config.Config{}, nil, sessStore, nil)
	job := &store.CronJob{
		ID:        "job-2",
		TenantID:  store.MasterTenantID,
		Name:      "stateful test",
		AgentID:   "skynet-backlog-iterator",
		UserID:    "system:test",
		Payload:   store.CronPayload{Kind: "agent_turn", Message: "run"},
		Stateless: false,
	}

	if _, err := handler(job); err != nil {
		t.Fatal(err)
	}
	if sessStore.resets != 0 {
		t.Fatalf("Reset calls = %d, want 0", sessStore.resets)
	}
	if sessStore.saves != 0 {
		t.Fatalf("Save calls = %d, want 0", sessStore.saves)
	}
}
