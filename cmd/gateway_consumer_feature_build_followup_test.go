package cmd

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/agent"
	featurerequests "github.com/nextlevelbuilder/goclaw/internal/beta/feature_requests"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/scheduler"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func TestHandleFeatureBuildFollowupRoutesToOriginalSessionAndPublishesReply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sessStore := newFollowupSessionStore()
	reqCh := make(chan agent.RunRequest, 1)
	sched := scheduler.NewScheduler(
		[]scheduler.LaneConfig{{Name: scheduler.LaneSubagent, Concurrency: 1}},
		scheduler.QueueConfig{Mode: scheduler.QueueModeQueue, Cap: 10, DebounceMs: 0, MaxConcurrent: 1},
		func(ctx context.Context, req agent.RunRequest) (*agent.RunResult, error) {
			reqCh <- req
			return &agent.RunResult{Content: "Checked feature_detail. The build failed at verification."}, nil
		},
	)
	defer sched.Stop()

	msgBus := bus.New()
	deps := &ConsumerDeps{
		Sched:     sched,
		MsgBus:    msgBus,
		SessStore: sessStore,
	}

	msg := bus.InboundMessage{
		Channel:  tools.ChannelSystem,
		SenderID: featurerequests.BuildFollowupSenderID("feature-123"),
		ChatID:   "-100321",
		Content:  "[System Message] Background build finished.",
		UserID:   "group:telegram:-100321",
		AgentID:  "builder-bot",
		Metadata: map[string]string{
			tools.MetaOriginChannel:    "telegram",
			tools.MetaOriginPeerKind:   "group",
			tools.MetaOriginLocalKey:   "-100321:topic:42",
			tools.MetaOriginSessionKey: "agent:builder-bot:telegram:group:-100321:topic:42",
		},
	}

	if !handleFeatureBuildFollowup(ctx, msg, deps) {
		t.Fatal("expected feature build follow-up handler to accept the message")
	}

	select {
	case req := <-reqCh:
		if req.SessionKey != msg.Metadata[tools.MetaOriginSessionKey] {
			t.Fatalf("session_key = %q, want %q", req.SessionKey, msg.Metadata[tools.MetaOriginSessionKey])
		}
		if req.Channel != "telegram" {
			t.Fatalf("channel = %q, want %q", req.Channel, "telegram")
		}
		if req.ChatID != msg.ChatID {
			t.Fatalf("chat_id = %q, want %q", req.ChatID, msg.ChatID)
		}
		if req.PeerKind != "group" {
			t.Fatalf("peer_kind = %q, want %q", req.PeerKind, "group")
		}
		if req.LocalKey != "-100321:topic:42" {
			t.Fatalf("local_key = %q, want %q", req.LocalKey, "-100321:topic:42")
		}
		if !req.HideInput {
			t.Fatal("expected follow-up announce run to hide input")
		}
		if req.RunKind != "announce" {
			t.Fatalf("run_kind = %q, want %q", req.RunKind, "announce")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scheduled follow-up run")
	}

	deps.BgWg.Wait()

	history := sessStore.GetHistory(ctx, msg.Metadata[tools.MetaOriginSessionKey])
	if len(history) != 1 {
		t.Fatalf("history count = %d, want 1", len(history))
	}
	if history[0].Role != "user" {
		t.Fatalf("history role = %q, want %q", history[0].Role, "user")
	}
	if history[0].Content != msg.Content {
		t.Fatalf("history content = %q, want %q", history[0].Content, msg.Content)
	}

	outCtx, outCancel := context.WithTimeout(context.Background(), time.Second)
	defer outCancel()

	outMsg, ok := msgBus.SubscribeOutbound(outCtx)
	if !ok {
		t.Fatal("expected outbound follow-up message")
	}
	if outMsg.Channel != "telegram" {
		t.Fatalf("outbound channel = %q, want %q", outMsg.Channel, "telegram")
	}
	if outMsg.ChatID != msg.ChatID {
		t.Fatalf("outbound chat_id = %q, want %q", outMsg.ChatID, msg.ChatID)
	}
	if outMsg.Metadata["local_key"] != "-100321:topic:42" {
		t.Fatalf("outbound local_key = %q, want %q", outMsg.Metadata["local_key"], "-100321:topic:42")
	}
	if !strings.Contains(outMsg.Content, "feature_detail") {
		t.Fatalf("outbound content = %q, want feature_detail summary", outMsg.Content)
	}
}

type followupSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*store.SessionData
}

func newFollowupSessionStore() *followupSessionStore {
	return &followupSessionStore{sessions: make(map[string]*store.SessionData)}
}

func (m *followupSessionStore) GetOrCreate(_ context.Context, key string) *store.SessionData {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[key]; s != nil {
		return s
	}
	s := &store.SessionData{Key: key}
	m.sessions[key] = s
	return s
}

func (m *followupSessionStore) Get(_ context.Context, key string) *store.SessionData {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[key]
}

func (m *followupSessionStore) AddMessage(_ context.Context, key string, msg providers.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[key]
	if s == nil {
		s = &store.SessionData{Key: key}
		m.sessions[key] = s
	}
	s.Messages = append(s.Messages, msg)
}

func (m *followupSessionStore) GetHistory(_ context.Context, key string) []providers.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[key]
	if s == nil {
		return nil
	}
	out := make([]providers.Message, len(s.Messages))
	copy(out, s.Messages)
	return out
}

func (m *followupSessionStore) GetSummary(context.Context, string) string                      { return "" }
func (m *followupSessionStore) SetSummary(context.Context, string, string)                     {}
func (m *followupSessionStore) GetLabel(context.Context, string) string                        { return "" }
func (m *followupSessionStore) SetLabel(context.Context, string, string)                       {}
func (m *followupSessionStore) SetAgentInfo(context.Context, string, uuid.UUID, string)        {}
func (m *followupSessionStore) TruncateHistory(context.Context, string, int)                   {}
func (m *followupSessionStore) SetHistory(context.Context, string, []providers.Message)        {}
func (m *followupSessionStore) Reset(context.Context, string)                                  {}
func (m *followupSessionStore) Delete(context.Context, string) error                           { return nil }
func (m *followupSessionStore) Save(context.Context, string) error                             { return nil }
func (m *followupSessionStore) UpdateMetadata(context.Context, string, string, string, string) {}
func (m *followupSessionStore) AccumulateTokens(context.Context, string, int64, int64)         {}
func (m *followupSessionStore) IncrementCompaction(context.Context, string)                    {}
func (m *followupSessionStore) GetCompactionCount(context.Context, string) int                 { return 0 }
func (m *followupSessionStore) GetMemoryFlushCompactionCount(context.Context, string) int      { return 0 }
func (m *followupSessionStore) SetMemoryFlushDone(context.Context, string)                     {}
func (m *followupSessionStore) GetSessionMetadata(context.Context, string) map[string]string {
	return nil
}
func (m *followupSessionStore) SetSessionMetadata(context.Context, string, map[string]string) {}
func (m *followupSessionStore) SetSpawnInfo(context.Context, string, string, int)             {}
func (m *followupSessionStore) SetContextWindow(context.Context, string, int)                 {}
func (m *followupSessionStore) GetContextWindow(context.Context, string) int                  { return 0 }
func (m *followupSessionStore) SetLastPromptTokens(context.Context, string, int, int)         {}
func (m *followupSessionStore) GetLastPromptTokens(context.Context, string) (int, int)        { return 0, 0 }
func (m *followupSessionStore) List(context.Context, string) []store.SessionInfo              { return nil }
func (m *followupSessionStore) ListPaged(context.Context, store.SessionListOpts) store.SessionListResult {
	return store.SessionListResult{}
}
func (m *followupSessionStore) ListPagedRich(context.Context, store.SessionListOpts) store.SessionListRichResult {
	return store.SessionListRichResult{}
}
func (m *followupSessionStore) LastUsedChannel(context.Context, string) (string, string) {
	return "", ""
}
