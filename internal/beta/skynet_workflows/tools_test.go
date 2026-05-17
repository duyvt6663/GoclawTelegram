package skynetworkflows

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bootstrap"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
	toolspkg "github.com/nextlevelbuilder/goclaw/internal/tools"
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

func TestBacklogRefineRoutesExperimentLeafToExperimentQueue(t *testing.T) {
	store := newTestFeatureStore(t)
	tenantID := storepkg.MasterTenantID.String()
	parentItems, err := store.addItems(tenantID, kindBacklog, []string{"Plan writing question stack rollout"}, workflowOrigin{}, "repo-backlog:rollup", map[string]string{
		"source_path": "backlog/01-mvp-platform.md",
		"source_line": "86",
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
		"result":  "Split implementation and experiment validation paths.",
		"text":    "- Experiment leaf: validate apps/web/experiments/w2-question-stack\n- Add writing stage schema support",
	})
	if result.IsError {
		t.Fatalf("refine returned error: %s", result.ForLLM)
	}

	backlogChildren, err := store.listItems(tenantID, kindBacklog, statusPending, 10)
	if err != nil {
		t.Fatalf("list backlog children: %v", err)
	}
	if len(backlogChildren) != 1 {
		t.Fatalf("backlog children = %d, want 1: %#v", len(backlogChildren), backlogChildren)
	}
	experimentChildren, err := store.listItems(tenantID, kindExperiment, statusPending, 10)
	if err != nil {
		t.Fatalf("list experiment children: %v", err)
	}
	if len(experimentChildren) != 1 {
		t.Fatalf("experiment children = %d, want 1: %#v", len(experimentChildren), experimentChildren)
	}
	if !strings.HasPrefix(experimentChildren[0].Body, "Experiment leaf:") {
		t.Fatalf("experiment body = %q, want experiment leaf", experimentChildren[0].Body)
	}
	if experimentChildren[0].Metadata["transition"] != "backlog_refinement_to_experiment" {
		t.Fatalf("experiment transition metadata = %q", experimentChildren[0].Metadata["transition"])
	}
	if experimentChildren[0].Metadata["parent_item"] != parent.ID {
		t.Fatalf("experiment child missing parent metadata: %#v", experimentChildren[0].Metadata)
	}

	updated, err := store.getItem(tenantID, parent.ID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if updated.Metadata["refined_child_count"] != "2" {
		t.Fatalf("refined_child_count = %q, want 2", updated.Metadata["refined_child_count"])
	}
	if updated.Metadata["refined_backlog_child_count"] != "1" {
		t.Fatalf("refined_backlog_child_count = %q, want 1", updated.Metadata["refined_backlog_child_count"])
	}
	if updated.Metadata["refined_experiment_child_count"] != "1" {
		t.Fatalf("refined_experiment_child_count = %q, want 1", updated.Metadata["refined_experiment_child_count"])
	}
}

func TestPrioritizeBacklogUpdatesMetadataAndClaimOrder(t *testing.T) {
	store := newTestFeatureStore(t)
	tenantID := storepkg.MasterTenantID.String()
	items, err := store.addItems(tenantID, kindBacklog, []string{
		"Routine cleanup",
		"Checkout enrollment blocker",
	}, workflowOrigin{}, "test", nil)
	if err != nil {
		t.Fatalf("addItems: %v", err)
	}

	tool := &boardTool{
		feature: &SkynetWorkflowsFeature{store: store},
		name:    "skynet_backlog",
		kind:    kindBacklog,
	}
	result := tool.Execute(context.Background(), map[string]any{
		"action":          "prioritize",
		"item_id":         items[1].ID,
		"priority":        "p0",
		"feature":         "checkout",
		"priority_reason": "Blocks package enrollment.",
	})
	if result.IsError {
		t.Fatalf("prioritize returned error: %s", result.ForLLM)
	}
	updated, err := store.getItem(tenantID, items[1].ID)
	if err != nil {
		t.Fatalf("get prioritized item: %v", err)
	}
	if updated.Metadata["priority"] != "100" || updated.Metadata["priority_feature"] != "checkout" {
		t.Fatalf("priority metadata = %#v", updated.Metadata)
	}

	claimed, err := store.claimNext(tenantID, kindBacklog, "worker", "skynet-backlog-iterator")
	if err != nil {
		t.Fatalf("claimNext: %v", err)
	}
	if claimed.ID != items[1].ID {
		t.Fatalf("claimed %s, want prioritized item %s", claimed.ID, items[1].ID)
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
	for _, want := range []string{
		"Never backslash-escape Markdown backtick characters",
		"gh pr create --body-file",
		"Before and After Mermaid diagrams",
		"flowchart LR",
		"snapshot/screenshot",
		"untracked stash parents",
		"refs/stash^3",
		"git ls-tree -r refs/stash^3",
	} {
		if !strings.Contains(prItem.Body, want) {
			t.Fatalf("PR item body missing %q:\n%s", want, prItem.Body)
		}
	}
	if updated.Metadata["pr_item"] != prItem.ID {
		t.Fatalf("QA pr_item metadata = %q, want %q", updated.Metadata["pr_item"], prItem.ID)
	}
}

func TestPRComposerContextIncludesPRBodyRules(t *testing.T) {
	feature := &SkynetWorkflowsFeature{targetRepo: "/repo"}
	files := feature.contextFilesForSpec(workflowAgentSpec{
		DisplayName: "Skynet PR Composer",
		Frontmatter: "Post-QA PR composer",
		Role:        "pr-composer",
	}, "/repo")
	content := files[bootstrap.AgentsFile]

	for _, want := range []string{
		"Never backslash-escape Markdown backtick characters",
		"gh pr create --body-file",
		"Before and After Mermaid diagrams",
		"flowchart LR",
		"sequenceDiagram",
		"snapshot/screenshot",
		"Do not fail as missing work",
		"untracked stash parents",
		"refs/stash^3",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("PR composer context missing %q:\n%s", want, content)
		}
	}
}

func TestRefactorScoutContextIncludesScopeMemoryAndCRRules(t *testing.T) {
	feature := &SkynetWorkflowsFeature{targetRepo: "/repo"}
	files := feature.contextFilesForSpec(workflowAgentSpec{
		DisplayName: "Skynet Refactor Scout",
		Frontmatter: "Scoped maintainability reviewer",
		Role:        "refactor-scout",
	}, "/repo")
	content := files[bootstrap.AgentsFile]

	for _, want := range []string{
		"memory_search",
		"list skynet_change_requests in review",
		"one bounded code slice per run",
		"3-8 related files",
		"category \"refactor\" or \"technical_debt\"",
		"Do not edit code",
		"future agents should remember",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("refactor scout context missing %q:\n%s", want, content)
		}
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

func TestBacklogBatchCompleteQueuesSingleQAItem(t *testing.T) {
	store := newTestFeatureStore(t)
	tenantID := storepkg.MasterTenantID.String()
	backlogItems, err := store.addItems(tenantID, kindBacklog, []string{
		"Add question stack UI primitive",
		"Wire question stack into writing stage",
	}, workflowOrigin{}, "repo-backlog:test", map[string]string{
		"parent_item": "parent-1",
	})
	if err != nil {
		t.Fatalf("addItems: %v", err)
	}

	tool := &boardTool{
		feature: &SkynetWorkflowsFeature{store: store},
		name:    "skynet_backlog",
		kind:    kindBacklog,
	}
	result := tool.Execute(context.Background(), map[string]any{
		"action":   "complete",
		"item_ids": []any{backlogItems[0].ID, backlogItems[1].ID},
		"result":   "Implemented one coherent question-stack slice.",
	})
	if result.IsError {
		t.Fatalf("complete returned error: %s", result.ForLLM)
	}

	qaItems, err := store.listItems(tenantID, kindQA, statusPending, 10)
	if err != nil {
		t.Fatalf("list QA items: %v", err)
	}
	if len(qaItems) != 1 {
		t.Fatalf("pending QA items = %d, want 1", len(qaItems))
	}
	if !strings.Contains(qaItems[0].Body, backlogItems[0].ID) || !strings.Contains(qaItems[0].Body, backlogItems[1].ID) {
		t.Fatalf("QA body missing batch item IDs: %q", qaItems[0].Body)
	}
	for _, backlogItem := range backlogItems {
		updated, err := store.getItem(tenantID, backlogItem.ID)
		if err != nil {
			t.Fatalf("get backlog item: %v", err)
		}
		if updated.Status != statusDone {
			t.Fatalf("backlog status = %q, want done", updated.Status)
		}
		if updated.Metadata["qa_item"] != qaItems[0].ID {
			t.Fatalf("qa_item metadata = %q, want %q", updated.Metadata["qa_item"], qaItems[0].ID)
		}
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

func TestCIFailureToolPublishesDispatchLog(t *testing.T) {
	msgBus := bus.New()
	tool := &ciFailureTool{
		feature: &SkynetWorkflowsFeature{
			msgBus:     msgBus,
			targetRepo: "/repo",
		},
	}

	result := tool.Execute(context.Background(), map[string]any{
		"log":          "CI failed",
		"pull_request": "#6",
		"branch":       "skynet/pr/example",
		"run_url":      "https://github.com/example/actions/runs/1",
		"channel":      "builder-bot",
		"chat_id":      "-1003865644303",
		"local_key":    "-1003865644303:topic:37674",
		"peer_kind":    "group",
	})
	if result.IsError {
		t.Fatalf("ci failure trigger returned error: %s", result.ForLLM)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	inbound, ok := msgBus.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("expected dispatched inbound message")
	}
	if inbound.AgentID != agentKeyCIFixer {
		t.Fatalf("AgentID = %q, want %q", inbound.AgentID, agentKeyCIFixer)
	}

	outbound, ok := msgBus.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected dispatch log outbound message")
	}
	for _, want := range []string{
		"[SKYNET CI FIXER DISPATCHED]",
		"Agent: " + agentKeyCIFixer,
		"Pull request: #6",
		"Branch: skynet/pr/example",
		"Status: accepted by GoClaw; agent is starting.",
	} {
		if !strings.Contains(outbound.Content, want) {
			t.Fatalf("dispatch log missing %q:\n%s", want, outbound.Content)
		}
	}
	if outbound.Metadata[toolspkg.MetaMessageThreadID] != "37674" {
		t.Fatalf("thread metadata = %q, want 37674", outbound.Metadata[toolspkg.MetaMessageThreadID])
	}
}

func TestPRConflictToolPublishesDispatchLog(t *testing.T) {
	msgBus := bus.New()
	tool := &prConflictTool{
		feature: &SkynetWorkflowsFeature{
			msgBus:     msgBus,
			targetRepo: "/repo",
		},
	}

	result := tool.Execute(context.Background(), map[string]any{
		"pull_request": "#11",
		"title":        "feat: share cards",
		"repository":   "duyvt6663/ResearchCrafters",
		"branch":       "skynet/pr/share-card-public-urls-2026-05-15",
		"base_branch":  "main",
		"commit":       "6c8c278",
		"base_sha":     "edea00d",
		"merge_state":  "DIRTY",
		"checks":       "SUCCESS",
		"url":          "https://github.com/duyvt6663/ResearchCrafters/pull/11",
		"channel":      "builder-bot",
		"chat_id":      "-1003865644303",
		"local_key":    "-1003865644303:topic:37674",
		"peer_kind":    "group",
	})
	if result.IsError {
		t.Fatalf("pr conflict trigger returned error: %s", result.ForLLM)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	inbound, ok := msgBus.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("expected dispatched inbound message")
	}
	if inbound.AgentID != agentKeyPRConflictResolver {
		t.Fatalf("AgentID = %q, want %q", inbound.AgentID, agentKeyPRConflictResolver)
	}
	for _, want := range []string{
		"[Skynet PR Merge Conflict]",
		"Pull request: #11",
		"Merge state: DIRTY",
		"Resolve it even if CI/Lighthouse checks are already green",
	} {
		if !strings.Contains(inbound.Content, want) {
			t.Fatalf("inbound conflict prompt missing %q:\n%s", want, inbound.Content)
		}
	}

	outbound, ok := msgBus.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected dispatch log outbound message")
	}
	for _, want := range []string{
		"[SKYNET PR CONFLICT RESOLVER DISPATCHED]",
		"Agent: " + agentKeyPRConflictResolver,
		"Pull request: #11",
		"Checks: SUCCESS",
		"Status: accepted by GoClaw; conflict resolver is starting.",
	} {
		if !strings.Contains(outbound.Content, want) {
			t.Fatalf("dispatch log missing %q:\n%s", want, outbound.Content)
		}
	}
	if outbound.Metadata[toolspkg.MetaMessageThreadID] != "37674" {
		t.Fatalf("thread metadata = %q, want 37674", outbound.Metadata[toolspkg.MetaMessageThreadID])
	}
}

func TestPRConflictDedupeKeyNormalizesPRNumber(t *testing.T) {
	args := map[string]any{
		"repository":   "duyvt6663/ResearchCrafters",
		"pull_request": "https://github.com/duyvt6663/ResearchCrafters/pull/11",
		"base_branch":  "main",
		"base_sha":     "edea00d",
		"branch":       "skynet/pr/share-card-public-urls-2026-05-15",
		"commit":       "6c8c278",
		"merge_state":  "dirty",
	}

	if got, want := prConflictConfigKey(args), "beta.skynet_workflows.pr_conflict.duyvt6663_researchcrafters.11"; got != want {
		t.Fatalf("config key = %q, want %q", got, want)
	}
	if got := prConflictFingerprint(args); !strings.Contains(got, "|DIRTY") {
		t.Fatalf("fingerprint did not normalize merge_state: %q", got)
	}
}

func TestMainSyncDefaultsAndFingerprint(t *testing.T) {
	targetRepo := "/Users/duyvt6663/github/ResearchCrafters"
	if got, want := siblingMainDeployRepo(targetRepo), "/Users/duyvt6663/github/ResearchCrafters-main"; got != want {
		t.Fatalf("siblingMainDeployRepo() = %q, want %q", got, want)
	}

	args := map[string]any{
		"repository": "duyvt6663/ResearchCrafters",
		"commit":     "abcdef123456",
	}
	if got, want := mainSyncFingerprint(args, "main"), "duyvt6663/ResearchCrafters|main|abcdef123456"; got != want {
		t.Fatalf("mainSyncFingerprint() = %q, want %q", got, want)
	}
	if got, want := mainSyncConfigKey(args, "main"), "beta.skynet_workflows.main_sync.duyvt6663_researchcrafters.main"; got != want {
		t.Fatalf("mainSyncConfigKey() = %q, want %q", got, want)
	}
}

func TestMainSyncInputValidation(t *testing.T) {
	for _, branch := range []string{"main", "release/2026.05", "feature_a-b"} {
		if !safeMainSyncBranch(branch) {
			t.Fatalf("branch %q should be accepted", branch)
		}
	}
	for _, branch := range []string{"../main", "main..next", "main next", "-bad"} {
		if safeMainSyncBranch(branch) {
			t.Fatalf("branch %q should be rejected", branch)
		}
	}
	for _, port := range []string{"3000", "80", "65535"} {
		if !safePort(port) {
			t.Fatalf("port %q should be accepted", port)
		}
	}
	for _, port := range []string{"", "3000;rm", "123456"} {
		if safePort(port) {
			t.Fatalf("port %q should be rejected", port)
		}
	}
	if got := shellQuote("a'b"); got != `'a'"'"'b'` {
		t.Fatalf("shellQuote() = %q", got)
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

func TestExperimentReviewReminderPublishesAccessLinks(t *testing.T) {
	t.Setenv("GOCLAW_SKYNET_TARGET_REPO", "")
	store := newTestFeatureStore(t)
	msgBus := bus.New()
	tenantID := storepkg.MasterTenantID.String()
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[remote \"origin\"]\n\turl = git@github.com-spartan-duykhanh:duyvt6663/ResearchCrafters.git\n"
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	items, err := store.addItems(tenantID, kindExperiment, []string{
		"[apps/web/experiments/w2-question-stack/README.md] W2 - Question Stack\nStatus: draft\nPath: apps/web/experiments/w2-question-stack",
	}, workflowOrigin{}, "repo-experiment:w2-question-stack", map[string]string{
		"experiment_slug":   "w2-question-stack",
		"experiment_path":   "apps/web/experiments/w2-question-stack",
		"experiment_readme": "apps/web/experiments/w2-question-stack/README.md",
	})
	if err != nil {
		t.Fatalf("addItems: %v", err)
	}
	reviewItem, err := store.updateStatus(tenantID, items[0].ID, statusReview, "Ready for channel review.", map[string]string{
		"transition": "experiment_review_requested",
	})
	if err != nil {
		t.Fatalf("updateStatus: %v", err)
	}

	tool := &boardTool{
		feature: &SkynetWorkflowsFeature{
			store:      store,
			msgBus:     msgBus,
			targetRepo: repo,
		},
		name: "skynet_experiments",
		kind: kindExperiment,
	}
	result := tool.Execute(context.Background(), map[string]any{
		"action":    "review_reminders",
		"channel":   "telegram",
		"chat_id":   "chat-review",
		"local_key": "chat-review:topic:42",
	})
	if result.IsError {
		t.Fatalf("review_reminders returned error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, `"status": "review_reminder_published"`) {
		t.Fatalf("result missing published status: %s", result.ForLLM)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	outbound, ok := msgBus.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected outbound review reminder")
	}
	if outbound.Channel != "telegram" || outbound.ChatID != "chat-review:topic:42" {
		t.Fatalf("outbound target = %s/%s, want telegram/chat-review:topic:42", outbound.Channel, outbound.ChatID)
	}
	if outbound.Metadata[toolspkg.MetaMessageThreadID] != "42" {
		t.Fatalf("thread metadata = %q, want 42", outbound.Metadata[toolspkg.MetaMessageThreadID])
	}
	for _, want := range []string{
		"SKYNET EXPERIMENT REVIEW REMINDER",
		"W2 - Question Stack",
		reviewItem.ID,
		"https://github.com/duyvt6663/ResearchCrafters/blob/main/apps/web/experiments/w2-question-stack/README.md",
		"https://github.com/duyvt6663/ResearchCrafters/blob/main/apps/web/experiments/w2-question-stack/Mock.tsx",
		"Route: `/experiments/w2-question-stack`",
		"Decision needed: approve to backlog, request revision, or drop.",
	} {
		if !strings.Contains(outbound.Content, want) {
			t.Fatalf("review reminder missing %q:\n%s", want, outbound.Content)
		}
	}
}

func TestChangeRequestSubmitRequiresReviewAndPublishesRedReminder(t *testing.T) {
	t.Setenv("GOCLAW_SKYNET_TARGET_REPO", "")
	store := newTestFeatureStore(t)
	msgBus := bus.New()
	tenantID := storepkg.MasterTenantID.String()
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[remote \"origin\"]\n\turl = git@github.com-spartan-duykhanh:duyvt6663/ResearchCrafters.git\n"
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := &boardTool{
		feature: &SkynetWorkflowsFeature{
			store:      store,
			msgBus:     msgBus,
			targetRepo: repo,
		},
		name: "skynet_change_requests",
		kind: kindChangeReq,
	}
	result := tool.Execute(context.Background(), map[string]any{
		"action":    "submit",
		"text":      "W2 question stack needs clearer progress feedback between writing questions.\n\n- Observed: users see no progress state.\n- Expected: the stack shows what changed after each generated question.",
		"route":     "experiment",
		"category":  "ui_ux",
		"erp":       "w2-question-stack",
		"channel":   "telegram",
		"chat_id":   "chat-review",
		"local_key": "chat-review:topic:42",
	})
	if result.IsError {
		t.Fatalf("submit returned error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, `"status": "review_requested"`) {
		t.Fatalf("submit result missing review status: %s", result.ForLLM)
	}

	items, err := store.listItems(tenantID, kindChangeReq, statusReview, 10)
	if err != nil {
		t.Fatalf("list change requests: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("review CR count = %d, want 1: %#v", len(items), items)
	}
	if items[0].Metadata["human_review"] != "required" || items[0].Metadata["suggested_route"] != kindExperiment {
		t.Fatalf("review metadata = %#v", items[0].Metadata)
	}
	for _, want := range []string{"Observed: users see no progress state.", "Expected: the stack shows what changed"} {
		if !strings.Contains(items[0].Body, want) {
			t.Fatalf("CR body lost multiline text %q: %s", want, items[0].Body)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	outbound, ok := msgBus.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected outbound CR review request")
	}
	for _, want := range []string{
		"🟥 **SKYNET CHANGE REQUEST REVIEW REQUIRED**",
		"Suggested route: `experiment`",
		"Workflow queue reference: [backlog/99-skynet-workflow-queue.md](https://github.com/duyvt6663/ResearchCrafters/blob/main/backlog/99-skynet-workflow-queue.md)",
		"approve to experiment",
		"approve to backlog",
	} {
		if !strings.Contains(outbound.Content, want) {
			t.Fatalf("CR review request missing %q:\n%s", want, outbound.Content)
		}
	}

	result = tool.Execute(context.Background(), map[string]any{
		"action":    "review_reminders",
		"channel":   "telegram",
		"chat_id":   "chat-review",
		"local_key": "chat-review:topic:42",
	})
	if result.IsError {
		t.Fatalf("review_reminders returned error: %s", result.ForLLM)
	}
	outbound, ok = msgBus.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected outbound CR reminder")
	}
	for _, want := range []string{
		"🟥 **SKYNET CHANGE REQUEST REVIEW REMINDER**",
		"RED review items block experiments/backlog",
		items[0].ID,
		"ERP: `w2-question-stack`",
		"Decision needed: approve to experiment, approve to backlog, request revision, or drop.",
	} {
		if !strings.Contains(outbound.Content, want) {
			t.Fatalf("CR reminder missing %q:\n%s", want, outbound.Content)
		}
	}
}

func TestChangeRequestCategorySupportsRefactorMetadata(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{input: "refactoring", want: "refactor"},
		{input: "code-health", want: "refactor"},
		{input: "technical debt", want: "technical_debt"},
		{input: "tech_debt", want: "technical_debt"},
	}
	for _, tc := range cases {
		if got := normalizeChangeRequestCategory(tc.input); got != tc.want {
			t.Fatalf("normalizeChangeRequestCategory(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	for _, input := range []string{"refactor", "technical-debt", "code health"} {
		if got := normalizeChangeRequestRoute(input); got != kindBacklog {
			t.Fatalf("normalizeChangeRequestRoute(%q) = %q, want %q", input, got, kindBacklog)
		}
	}
}

func TestChangeRequestApprovalQueuesExperimentOrBacklog(t *testing.T) {
	store := newTestFeatureStore(t)
	tenantID := storepkg.MasterTenantID.String()
	items, err := store.addItems(tenantID, kindChangeReq, []string{
		"Navigation between ERP stages feels visually ambiguous.",
	}, workflowOrigin{}, "erp-ux-walker", map[string]string{
		"suggested_route": kindExperiment,
		"category":        "ui_ux",
		"human_review":    "required",
	})
	if err != nil {
		t.Fatalf("add change request: %v", err)
	}
	reviewItem, err := store.updateStatus(tenantID, items[0].ID, statusReview, "awaiting human review", map[string]string{
		"transition": "change_request_review_requested",
	})
	if err != nil {
		t.Fatalf("mark review: %v", err)
	}

	tool := &boardTool{
		feature: &SkynetWorkflowsFeature{store: store},
		name:    "skynet_change_requests",
		kind:    kindChangeReq,
	}
	result := tool.Execute(context.Background(), map[string]any{
		"action":   "approve_to_experiment",
		"item_id":  reviewItem.ID,
		"feedback": "Approved for a sandbox pass before implementation.",
	})
	if result.IsError {
		t.Fatalf("approve returned error: %s", result.ForLLM)
	}

	updated, err := store.getItem(tenantID, reviewItem.ID)
	if err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if updated.Status != statusDone {
		t.Fatalf("CR status = %q, want done", updated.Status)
	}
	if updated.Metadata["queued_kind"] != kindExperiment || updated.Metadata["human_reviewed"] != "true" {
		t.Fatalf("CR approval metadata = %#v", updated.Metadata)
	}
	experimentItems, err := store.listItems(tenantID, kindExperiment, statusPending, 10)
	if err != nil {
		t.Fatalf("list experiments: %v", err)
	}
	if len(experimentItems) != 1 {
		t.Fatalf("experiment items = %d, want 1: %#v", len(experimentItems), experimentItems)
	}
	if experimentItems[0].Source != "change-request:"+reviewItem.ID {
		t.Fatalf("experiment source = %q", experimentItems[0].Source)
	}
	if !strings.Contains(experimentItems[0].Body, "Approved change request for UI/UX experiment") {
		t.Fatalf("experiment body = %q", experimentItems[0].Body)
	}
}
