package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/agent"
	featurerequests "github.com/nextlevelbuilder/goclaw/internal/beta/feature_requests"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/scheduler"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func TestHandleFeatureBuildFollowupRoutesToOriginalSessionAndPublishesReply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
		Sched:  sched,
		MsgBus: msgBus,
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
