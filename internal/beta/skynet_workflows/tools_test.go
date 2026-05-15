package skynetworkflows

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestBacklogRefineCreatesChildItemsAndMarksParent(t *testing.T) {
	store := newTestFeatureStore(t)
	tenantID := storepkg.MasterTenantID.String()
	parentItems, err := store.addItems(tenantID, kindBacklog, []string{"Add all math and writing modules"}, workflowOrigin{}, "repo-backlog:rollup", map[string]string{
		"source_path": "backlog/00-roadmap.md",
		"source_line": "65",
	})
	if err != nil {
		t.Fatalf("addItems: %v", err)
	}
	parent := parentItems[0]

	tool := &boardTool{
		feature: &SkynetWorkflowsFeature{store: store},
		name:    "skynet_backlog",
		kind:    kindBacklog,
	}
	result := tool.Execute(context.Background(), map[string]any{
		"action":  "refine",
		"item_id": parent.ID,
		"result":  "The parent is a rollup; split before implementation.",
		"text":    "- Add stage_subtype schema\n- Add ClaimEvidenceMatrix",
	})
	if result.IsError {
		t.Fatalf("refine returned error: %s", result.ForLLM)
	}

	updated, err := store.getItem(tenantID, parent.ID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if updated.Status != statusRefinement {
		t.Fatalf("parent status = %q, want %q", updated.Status, statusRefinement)
	}
	if updated.Metadata["source_path"] != "backlog/00-roadmap.md" {
		t.Fatalf("parent source metadata lost: %#v", updated.Metadata)
	}
	if updated.Metadata["refined_child_count"] != "2" {
		t.Fatalf("refined_child_count = %q, want 2", updated.Metadata["refined_child_count"])
	}

	children, err := store.listItems(tenantID, kindBacklog, statusPending, 10)
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("pending children = %d, want 2: %#v", len(children), children)
	}
	for _, child := range children {
		if child.Metadata["parent_item"] != parent.ID {
			t.Fatalf("child missing parent metadata: %#v", child.Metadata)
		}
	}
}

func TestQAPassQueuesPRCompositionItem(t *testing.T) {
	store := newTestFeatureStore(t)
	tenantID := storepkg.MasterTenantID.String()
	qaItems, err := store.addItems(tenantID, kindQA, []string{"Verify math stage hint acceptance flow"}, workflowOrigin{}, "qa-manual", map[string]string{
		"source_backlog_item": "backlog-123",
	})
	if err != nil {
		t.Fatalf("addItems: %v", err)
	}
	qaItem := qaItems[0]

	tool := &boardTool{
		feature: &SkynetWorkflowsFeature{store: store},
		name:    "skynet_qa",
		kind:    kindQA,
	}
	result := tool.Execute(context.Background(), map[string]any{
		"action":  "pass",
		"item_id": qaItem.ID,
		"result":  "Manual QA passed after running focused tests.",
	})
	if result.IsError {
		t.Fatalf("pass returned error: %s", result.ForLLM)
	}

	updated, err := store.getItem(tenantID, qaItem.ID)
	if err != nil {
		t.Fatalf("get QA item: %v", err)
	}
	if updated.Status != statusDone {
		t.Fatalf("QA status = %q, want %q", updated.Status, statusDone)
	}
	if updated.Metadata["transition"] != "qa_passed_to_pr" {
		t.Fatalf("QA transition metadata = %q, want qa_passed_to_pr", updated.Metadata["transition"])
	}
	if updated.Metadata["pr_item_count"] != "1" {
		t.Fatalf("pr_item_count = %q, want 1", updated.Metadata["pr_item_count"])
	}

	prItems, err := store.listItems(tenantID, kindPR, statusPending, 10)
	if err != nil {
		t.Fatalf("list PR items: %v", err)
	}
	if len(prItems) != 1 {
		t.Fatalf("pending PR items = %d, want 1: %#v", len(prItems), prItems)
	}
	prItem := prItems[0]
	if prItem.Source != "qa-pass:"+qaItem.ID {
		t.Fatalf("PR source = %q, want qa-pass:%s", prItem.Source, qaItem.ID)
	}
	if prItem.Metadata["source_qa_item"] != qaItem.ID {
		t.Fatalf("PR item missing source QA metadata: %#v", prItem.Metadata)
	}
	if !strings.Contains(prItem.Body, "Manual QA passed after running focused tests.") {
		t.Fatalf("PR item body missing QA evidence: %q", prItem.Body)
	}
	if updated.Metadata["pr_item"] != prItem.ID {
		t.Fatalf("QA pr_item metadata = %q, want %q", updated.Metadata["pr_item"], prItem.ID)
	}
}

func TestBacklogCompleteQueuesQAItem(t *testing.T) {
	store := newTestFeatureStore(t)
	tenantID := storepkg.MasterTenantID.String()
	backlogItems, err := store.addItems(tenantID, kindBacklog, []string{"Wire runner logs into the stage page"}, workflowOrigin{}, "repo-backlog:test", map[string]string{
		"source_path": "backlog/01-mvp-platform.md",
		"source_line": "21",
	})
	if err != nil {
		t.Fatalf("addItems: %v", err)
	}
	backlogItem := backlogItems[0]

	tool := &boardTool{
		feature: &SkynetWorkflowsFeature{store: store},
		name:    "skynet_backlog",
		kind:    kindBacklog,
	}
	result := tool.Execute(context.Background(), map[string]any{
		"action":  "complete",
		"item_id": backlogItem.ID,
		"result":  "Implemented stage page runId plumbing and wrote qa/runner-logs.md.",
	})
	if result.IsError {
		t.Fatalf("complete returned error: %s", result.ForLLM)
	}

	updated, err := store.getItem(tenantID, backlogItem.ID)
	if err != nil {
		t.Fatalf("get backlog item: %v", err)
	}
	if updated.Status != statusDone {
		t.Fatalf("backlog status = %q, want %q", updated.Status, statusDone)
	}
	if updated.Metadata["transition"] != "backlog_completed_to_qa" {
		t.Fatalf("transition metadata = %q, want backlog_completed_to_qa", updated.Metadata["transition"])
	}
	if updated.Metadata["qa_item_count"] != "1" {
		t.Fatalf("qa_item_count = %q, want 1", updated.Metadata["qa_item_count"])
	}

	qaItems, err := store.listItems(tenantID, kindQA, statusPending, 10)
	if err != nil {
		t.Fatalf("list QA items: %v", err)
	}
	if len(qaItems) != 1 {
		t.Fatalf("pending QA items = %d, want 1: %#v", len(qaItems), qaItems)
	}
	qaItem := qaItems[0]
	if qaItem.Source != "backlog-complete:"+backlogItem.ID {
		t.Fatalf("QA source = %q, want backlog-complete:%s", qaItem.Source, backlogItem.ID)
	}
	if qaItem.Metadata["source_backlog"] != backlogItem.ID {
		t.Fatalf("QA item missing source backlog metadata: %#v", qaItem.Metadata)
	}
	if !strings.Contains(qaItem.Body, "Implemented stage page runId plumbing") {
		t.Fatalf("QA item body missing implementation result: %q", qaItem.Body)
	}
	if updated.Metadata["qa_item"] != qaItem.ID {
		t.Fatalf("backlog qa_item metadata = %q, want %q", updated.Metadata["qa_item"], qaItem.ID)
	}
}

func TestDispatchAgentUsesRootWorkspaceScope(t *testing.T) {
	msgBus := bus.New()
	feature := &SkynetWorkflowsFeature{msgBus: msgBus}

	if err := feature.dispatchAgent(context.Background(), agentKeyCIFixer, "fix CI", workflowOrigin{
		Channel:  "builder-bot",
		ChatID:   "-1003865644303:topic:37674",
		PeerKind: "group",
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, ok := msgBus.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("expected dispatched inbound message")
	}
	if msg.UserID != "" {
		t.Fatalf("UserID = %q, want empty root workspace scope", msg.UserID)
	}
	if msg.Metadata["skynet_workflow"] != "true" {
		t.Fatalf("skynet_workflow metadata = %q, want true", msg.Metadata["skynet_workflow"])
	}
}

func TestEmptyQueueNextPublishesIdleLog(t *testing.T) {
	store := newTestFeatureStore(t)
	msgBus := bus.New()
	tool := &boardTool{
		feature: &SkynetWorkflowsFeature{
			store:  store,
			msgBus: msgBus,
		},
		name: "skynet_qa",
		kind: kindQA,
	}

	result := tool.Execute(context.Background(), map[string]any{
		"action":  "next",
		"channel": "telegram",
		"chat_id": "chat-1",
	})
	if result.IsError {
		t.Fatalf("next returned error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, `"idle_log": "published"`) {
		t.Fatalf("result missing published idle_log: %s", result.ForLLM)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	outbound, ok := msgBus.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected outbound idle log")
	}
	if outbound.Channel != "telegram" || outbound.ChatID != "chat-1" {
		t.Fatalf("outbound target = %s/%s, want telegram/chat-1", outbound.Channel, outbound.ChatID)
	}
	if !strings.Contains(outbound.Content, "[SKYNET QA IDLE]") {
		t.Fatalf("idle log content = %q", outbound.Content)
	}
}
