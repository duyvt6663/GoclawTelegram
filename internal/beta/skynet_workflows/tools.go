package skynetworkflows

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type workflowControlTool struct {
	feature *SkynetWorkflowsFeature
}

func (t *workflowControlTool) Name() string { return "skynet_workflows" }

func (t *workflowControlTool) Description() string {
	return "Configure and inspect the Skynet workflow stack: Telegram review target, target repository, managed agents, and cron jobs."
}

func (t *workflowControlTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action to perform.",
				"enum":        []string{"configure", "status", "seed_cron"},
			},
			"channel":     map[string]any{"type": "string", "description": "Channel name. Defaults to the current channel."},
			"chat_id":     map[string]any{"type": "string", "description": "Chat ID. Defaults to the current chat."},
			"local_key":   map[string]any{"type": "string", "description": "Composite topic/thread key, e.g. -100123:topic:42. Defaults to current local key."},
			"peer_kind":   map[string]any{"type": "string", "description": "direct or group. Defaults to current peer kind."},
			"target_repo": map[string]any{"type": "string", "description": "Absolute path of the repository the Skynet workers should edit."},
		},
		"required": []string{"action"},
	}
}

func (t *workflowControlTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("skynet_workflows feature is not initialized")
	}
	action := strings.ToLower(stringArg(args, "action"))
	if action == "" {
		action = "status"
	}

	switch action {
	case "configure":
		origin := originFromToolContext(ctx, args)
		if err := t.feature.saveConfiguredOrigin(ctx, origin); err != nil {
			return tools.ErrorResult(err.Error())
		}
		if targetRepo := stringArg(args, "target_repo"); targetRepo != "" {
			if err := t.feature.setTargetRepo(ctx, targetRepo); err != nil {
				return tools.ErrorResult(err.Error())
			}
		}
		if _, err := t.feature.ensureAgents(ctx); err != nil {
			return tools.ErrorResult(err.Error())
		}
		if err := t.feature.ensureCronJobs(ctx); err != nil {
			return tools.ErrorResult(err.Error())
		}
		return jsonResult(map[string]any{
			"status":      "configured",
			"origin":      t.feature.configuredOrigin(ctx),
			"target_repo": t.feature.resolveTargetRepo(ctx),
		})
	case "seed_cron":
		if err := t.feature.ensureCronJobs(ctx); err != nil {
			return tools.ErrorResult(err.Error())
		}
		return jsonResult(map[string]any{"status": "cron seeded"})
	case "status":
		return t.status(ctx)
	default:
		return tools.ErrorResult("unsupported action: " + action)
	}
}

func (t *workflowControlTool) status(ctx context.Context) *tools.Result {
	tenantID := tenantKeyFromCtx(storepkg.TenantIDFromContext(ctx))
	counts, err := t.feature.store.counts(tenantID)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	agents, err := t.feature.ensureAgents(ctx)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	payload := map[string]any{
		"feature":     featureName,
		"target_repo": t.feature.resolveTargetRepo(ctx),
		"origin":      t.feature.configuredOrigin(ctx),
		"agents":      agents,
		"queues":      counts,
	}
	return jsonResult(payload)
}

type boardTool struct {
	feature *SkynetWorkflowsFeature
	name    string
	kind    string
}

func (t *boardTool) Name() string { return t.name }

func (t *boardTool) Description() string {
	switch t.kind {
	case kindBacklog:
		return "Parse and manage Skynet backlog bullets. Cron workers claim one pending backlog item at a time, then complete, refine, or fail it."
	case kindExperiment:
		return "Parse and manage Skynet experiment bullets. Experiment workers claim one pending experiment, request channel review, then transition accepted work to backlog."
	case kindQA:
		return "Parse and manage Skynet QA bullets. QA workers claim one pending QA item, pass it into PR composition, or move failures to backlog."
	case kindPR:
		return "Manage post-QA PR composition items. PR workers claim one pending item, prepare a coherent pull request, then complete or fail it."
	default:
		return "Manage a Skynet workflow queue."
	}
}

func (t *boardTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{
					"add", "parse", "sync_repo", "list", "next", "complete", "refine", "split", "fail",
					"pass", "fail_to_backlog", "request_review", "approve_to_backlog", "revise", "drop",
				},
			},
			"text":      map[string]any{"type": "string", "description": "Raw text or bullet list to add. For refine/split, this is the child backlog bullet list to enqueue."},
			"item_id":   map[string]any{"type": "string", "description": "Workflow item ID for status transitions."},
			"result":    map[string]any{"type": "string", "description": "Result, validation summary, review request, failure evidence, or transition notes."},
			"feedback":  map[string]any{"type": "string", "description": "User feedback for experiment revision or transition."},
			"status":    map[string]any{"type": "string", "description": "Optional status filter for list."},
			"limit":     map[string]any{"type": "integer", "description": "Optional list limit, max 100."},
			"source":    map[string]any{"type": "string", "description": "Optional source label."},
			"agent_key": map[string]any{"type": "string", "description": "Optional claiming agent key."},
			"channel":   map[string]any{"type": "string", "description": "Optional channel override."},
			"chat_id":   map[string]any{"type": "string", "description": "Optional chat override."},
			"local_key": map[string]any{"type": "string", "description": "Optional topic/thread local key override."},
		},
		"required": []string{"action"},
	}
}

func (t *boardTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil || t.feature.store == nil {
		return tools.ErrorResult("skynet workflow queue is not initialized")
	}
	action := strings.ToLower(stringArg(args, "action"))
	tenantID := tenantKeyFromCtx(storepkg.TenantIDFromContext(ctx))

	switch action {
	case "add", "parse":
		return t.add(ctx, tenantID, args)
	case "sync_repo":
		if t.kind != kindBacklog {
			return tools.ErrorResult("sync_repo is only valid for skynet_backlog")
		}
		result, err := t.feature.syncRepoBacklog(ctx, tenantID)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		return jsonResult(map[string]any{"status": "synced", "repo_sync": result})
	case "list":
		var syncResult *repoBacklogSyncResult
		statusFilter := stringArg(args, "status")
		if t.kind == kindBacklog && (statusFilter == "" || statusFilter == statusPending) {
			var err error
			syncResult, err = t.feature.syncRepoBacklog(ctx, tenantID)
			if err != nil {
				return tools.ErrorResult(err.Error())
			}
		}
		items, err := t.feature.store.listItems(tenantID, t.kind, stringArg(args, "status"), intArg(args, "limit"))
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		payload := map[string]any{"items": items}
		if syncResult != nil {
			payload["repo_sync"] = syncResult
		}
		return jsonResult(payload)
	case "next":
		return t.next(ctx, tenantID, args)
	case "complete":
		if t.kind == kindQA {
			return t.passQAToPR(ctx, tenantID, args)
		}
		if t.kind == kindBacklog {
			return t.completeBacklogToQA(ctx, tenantID, args)
		}
		return t.update(ctx, tenantID, args, statusDone, "completed")
	case "refine", "split":
		if t.kind != kindBacklog {
			return tools.ErrorResult(action + " is only valid for skynet_backlog")
		}
		return t.refineBacklog(ctx, tenantID, args)
	case "fail":
		return t.update(ctx, tenantID, args, statusFailed, "failed")
	case "drop":
		return t.update(ctx, tenantID, args, statusDropped, "dropped")
	case "pass":
		if t.kind != kindQA {
			return tools.ErrorResult("pass is only valid for skynet_qa")
		}
		return t.passQAToPR(ctx, tenantID, args)
	case "fail_to_backlog":
		if t.kind != kindQA {
			return tools.ErrorResult("fail_to_backlog is only valid for skynet_qa")
		}
		return t.failQAToBacklog(ctx, tenantID, args)
	case "request_review":
		if t.kind != kindExperiment {
			return tools.ErrorResult("request_review is only valid for skynet_experiments")
		}
		return t.requestExperimentReview(ctx, tenantID, args)
	case "approve_to_backlog":
		if t.kind != kindExperiment {
			return tools.ErrorResult("approve_to_backlog is only valid for skynet_experiments")
		}
		return t.approveExperimentToBacklog(ctx, tenantID, args)
	case "revise":
		if t.kind != kindExperiment {
			return tools.ErrorResult("revise is only valid for skynet_experiments")
		}
		return t.reviseExperiment(ctx, tenantID, args)
	default:
		return tools.ErrorResult("unsupported action: " + action)
	}
}

func (t *boardTool) add(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	text := stringArg(args, "text")
	if text == "" {
		return tools.ErrorResult("text is required")
	}
	origin := originFromToolContext(ctx, args)
	items, err := t.feature.store.addItems(tenantID, t.kind, parseBulletItems(text), origin, stringArg(args, "source"), map[string]string{
		"created_by_agent": tools.ToolAgentKeyFromCtx(ctx),
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return jsonResult(map[string]any{"status": "added", "count": len(items), "items": items})
}

func (t *boardTool) next(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	agentKey := stringArg(args, "agent_key")
	if agentKey == "" {
		agentKey = tools.ToolAgentKeyFromCtx(ctx)
	}
	if agentKey == "" {
		agentKey = defaultWorkerForKind(t.kind)
	}
	claimedBy := storepkg.SenderIDFromContext(ctx)
	if claimedBy == "" {
		claimedBy = "system:skynet"
	}
	item, err := t.feature.store.claimNext(tenantID, t.kind, claimedBy, agentKey)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if item == nil && t.kind == kindBacklog {
		if _, err := t.feature.syncRepoBacklog(ctx, tenantID); err != nil {
			return tools.ErrorResult(err.Error())
		}
		item, err = t.feature.store.claimNext(tenantID, t.kind, claimedBy, agentKey)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
	}
	if item == nil {
		idleLogStatus, err := t.feature.publishQueueIdleLog(ctx, t.kind, tenantID, originFromToolContext(ctx, args))
		if err != nil {
			slog.Warn("skynet workflow idle notification failed", "kind", t.kind, "error", err)
		}
		return jsonResult(map[string]any{
			"status":   "empty",
			"message":  "no pending " + t.kind + " item",
			"idle_log": idleLogStatus,
		})
	}
	if err := t.feature.publishWorkflowUpdate(ctx, item, "CLAIMED", ""); err != nil {
		slog.Warn("skynet workflow update notification failed", "kind", t.kind, "item_id", item.ID, "error", err)
	}
	return jsonResult(map[string]any{
		"status":       "claimed",
		"item":         item,
		"instructions": instructionsForKind(t.kind),
	})
}

func (t *boardTool) update(ctx context.Context, tenantID string, args map[string]any, status, defaultResult string) *tools.Result {
	itemID := stringArg(args, "item_id")
	if itemID == "" {
		return tools.ErrorResult("item_id is required")
	}
	result := stringArg(args, "result")
	if result == "" {
		result = defaultResult
	}
	item, err := t.feature.store.updateStatus(tenantID, itemID, status, result, map[string]string{
		"updated_by_agent": tools.ToolAgentKeyFromCtx(ctx),
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if err := t.feature.publishWorkflowUpdate(ctx, item, strings.ToUpper(status), result); err != nil {
		slog.Warn("skynet workflow terminal notification failed", "kind", t.kind, "item_id", item.ID, "status", status, "error", err)
	}
	return jsonResult(map[string]any{"status": status, "item": item})
}

func (t *boardTool) completeBacklogToQA(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	itemID := stringArg(args, "item_id")
	if itemID == "" {
		return tools.ErrorResult("item_id is required")
	}
	result := stringArg(args, "result")
	if result == "" {
		result = "coding completed"
	}
	item, err := t.feature.store.getItem(tenantID, itemID)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	var qaItems []workflowItem
	qaItemID := strings.TrimSpace(item.Metadata["qa_item"])
	if qaItemID == "" {
		qaText := fmt.Sprintf(`Verify backlog-completed work from %s.

Backlog item:
%s

Implementation result:
%s

QA requirements:
- Inspect the target repository changes and any repo-root qa/ report created by the implementation worker.
- Run focused verification for the changed behavior.
- If behavior passes, call skynet_qa with action "pass" so PR composition is queued.
- If behavior fails, call skynet_qa with action "fail_to_backlog" with exact reproduction evidence.`, item.ID, item.Body, result)
		childMetadata := map[string]string{
			"created_by_agent": tools.ToolAgentKeyFromCtx(ctx),
			"source_backlog":   item.ID,
			"transition":       "backlog_completed_to_qa",
		}
		if sourcePath := item.Metadata["source_path"]; sourcePath != "" {
			childMetadata["parent_source_path"] = sourcePath
		}
		if sourceLine := item.Metadata["source_line"]; sourceLine != "" {
			childMetadata["parent_source_line"] = sourceLine
		}
		qaItems, err = t.feature.store.addItems(tenantID, kindQA, []string{qaText}, originFromItem(item), "backlog-complete:"+item.ID, childMetadata)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		if len(qaItems) > 0 {
			qaItemID = qaItems[0].ID
		}
	}

	updateResult := result
	if qaItemID != "" && !strings.Contains(updateResult, qaItemID) {
		updateResult = fmt.Sprintf("%s\n\nQueued QA item: %s", result, qaItemID)
	}
	updated, err := t.feature.store.updateStatus(tenantID, itemID, statusDone, updateResult, map[string]string{
		"transition":       "backlog_completed_to_qa",
		"updated_by_agent": tools.ToolAgentKeyFromCtx(ctx),
		"qa_item_count":    fmt.Sprintf("%d", len(qaItems)),
		"qa_item":          qaItemID,
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if err := t.feature.publishWorkflowUpdate(ctx, updated, "DONE -> QA", updateResult); err != nil {
		slog.Warn("skynet backlog completion-to-QA notification failed", "item_id", updated.ID, "error", err)
	}
	return jsonResult(map[string]any{
		"status":   "queued_for_qa",
		"item":     updated,
		"qa_items": qaItems,
	})
}

func (t *boardTool) passQAToPR(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	itemID := stringArg(args, "item_id")
	if itemID == "" {
		return tools.ErrorResult("item_id is required")
	}
	result := stringArg(args, "result")
	if result == "" {
		result = "qa passed"
	}
	item, err := t.feature.store.getItem(tenantID, itemID)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	prText := fmt.Sprintf(`Prepare a comprehensive pull request for QA-passed work from %s.

QA item:
%s

QA pass evidence:
%s

PR composition requirements:
- Gather the changes connected to this QA item and its upstream backlog or experiment context.
- Cherry-pick finished commits when they exist; otherwise stage and commit only the coherent files for this scope.
- Exclude unrelated dirty work and never revert user changes.
- Run focused verification.
- Create a GitHub PR if auth/remotes allow it, or produce a PR-ready title/body and exact blocker if creation is blocked.`, item.ID, item.Body, result)
	prItems, err := t.feature.store.addItems(tenantID, kindPR, []string{prText}, originFromItem(item), "qa-pass:"+item.ID, map[string]string{
		"created_by_agent": tools.ToolAgentKeyFromCtx(ctx),
		"source_qa_item":   item.ID,
		"transition":       "qa_passed_to_pr",
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	updateResult := result
	prItemID := ""
	if len(prItems) > 0 {
		prItemID = prItems[0].ID
		updateResult = fmt.Sprintf("%s\n\nQueued PR composition item: %s", result, prItemID)
	}
	updated, err := t.feature.store.updateStatus(tenantID, itemID, statusDone, updateResult, map[string]string{
		"transition":       "qa_passed_to_pr",
		"updated_by_agent": tools.ToolAgentKeyFromCtx(ctx),
		"pr_item_count":    fmt.Sprintf("%d", len(prItems)),
		"pr_item":          prItemID,
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if err := t.feature.publishWorkflowUpdate(ctx, updated, "PASSED -> PR", updateResult); err != nil {
		slog.Warn("skynet QA pass-to-PR notification failed", "item_id", updated.ID, "error", err)
	}
	return jsonResult(map[string]any{
		"status":   "queued_for_pr",
		"qa_item":  updated,
		"pr_items": prItems,
	})
}

func (t *boardTool) refineBacklog(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	itemID := stringArg(args, "item_id")
	if itemID == "" {
		return tools.ErrorResult("item_id is required")
	}
	result := stringArg(args, "result")
	childText := stringArg(args, "text")
	if result == "" && childText == "" {
		return tools.ErrorResult("result or text is required for backlog refinement")
	}
	item, err := t.feature.store.getItem(tenantID, itemID)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	var childItems []workflowItem
	if childText != "" {
		childMetadata := map[string]string{
			"created_by_agent": tools.ToolAgentKeyFromCtx(ctx),
			"parent_item":      item.ID,
			"transition":       "backlog_refinement",
		}
		if sourcePath := item.Metadata["source_path"]; sourcePath != "" {
			childMetadata["parent_source_path"] = sourcePath
		}
		if sourceLine := item.Metadata["source_line"]; sourceLine != "" {
			childMetadata["parent_source_line"] = sourceLine
		}
		childItems, err = t.feature.store.addItems(
			tenantID,
			kindBacklog,
			parseBulletItems(childText),
			originFromItem(item),
			"backlog-refinement:"+item.ID,
			childMetadata,
		)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
	}
	if result == "" {
		result = fmt.Sprintf("Backlog item refined into %d child item(s).", len(childItems))
	}
	updated, err := t.feature.store.updateStatus(tenantID, itemID, statusRefinement, result, map[string]string{
		"transition":          "backlog_refinement",
		"updated_by_agent":    tools.ToolAgentKeyFromCtx(ctx),
		"refined_child_count": fmt.Sprintf("%d", len(childItems)),
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if err := t.feature.publishWorkflowUpdate(ctx, updated, "NEEDS REFINEMENT", result); err != nil {
		slog.Warn("skynet backlog refinement notification failed", "item_id", updated.ID, "error", err)
	}
	return jsonResult(map[string]any{
		"status":      statusRefinement,
		"item":        updated,
		"child_items": childItems,
	})
}

func (t *boardTool) failQAToBacklog(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	itemID := stringArg(args, "item_id")
	if itemID == "" {
		return tools.ErrorResult("item_id is required")
	}
	result := stringArg(args, "result")
	if result == "" {
		return tools.ErrorResult("result is required for fail_to_backlog")
	}
	item, err := t.feature.store.getItem(tenantID, itemID)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	updated, err := t.feature.store.updateStatus(tenantID, itemID, statusFailed, result, map[string]string{
		"transition": "qa_failed_to_backlog",
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	backlogText := fmt.Sprintf("Fix QA failure from %s\n\nQA item:\n%s\n\nFailure evidence:\n%s", item.ID, item.Body, result)
	backlogItems, err := t.feature.store.addItems(tenantID, kindBacklog, []string{backlogText}, originFromItem(item), "qa-failure", map[string]string{
		"source_qa_item": item.ID,
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if err := t.feature.publishWorkflowUpdate(ctx, updated, "FAILED -> BACKLOG", result); err != nil {
		slog.Warn("skynet QA transition notification failed", "item_id", updated.ID, "error", err)
	}
	return jsonResult(map[string]any{
		"status":        "moved_to_backlog",
		"qa_item":       updated,
		"backlog_items": backlogItems,
	})
}

func (t *boardTool) requestExperimentReview(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	itemID := stringArg(args, "item_id")
	if itemID == "" {
		return tools.ErrorResult("item_id is required")
	}
	result := stringArg(args, "result")
	if result == "" {
		return tools.ErrorResult("result is required")
	}
	item, err := t.feature.store.getItem(tenantID, itemID)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	updated, err := t.feature.store.updateStatus(tenantID, itemID, statusReview, result, map[string]string{
		"transition": "experiment_review_requested",
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if err := t.feature.publishExperimentReview(ctx, updated, result); err != nil {
		return jsonResult(map[string]any{
			"status":  "review_pending_delivery",
			"item":    updated,
			"warning": err.Error(),
			"message": experimentReviewMessage(updated, result),
		})
	}
	_ = item
	return jsonResult(map[string]any{"status": "review_requested", "item": updated})
}

func (t *boardTool) approveExperimentToBacklog(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	itemID := stringArg(args, "item_id")
	if itemID == "" {
		return tools.ErrorResult("item_id is required")
	}
	feedback := stringArg(args, "feedback", "result")
	item, err := t.feature.store.getItem(tenantID, itemID)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	message := fmt.Sprintf(`[Skynet Experiment Accepted]

Convert this accepted experiment into concrete implementation backlog bullets.

Experiment ID: %s
Experiment:
%s

Experiment result:
%s

Reviewer feedback:
%s

Call skynet_backlog with action "add" and put the backlog plan in bullet form. Do not implement the feature in this transition role.
`, item.ID, item.Body, item.Result, feedback)
	if err := t.feature.dispatchAgent(ctx, agentKeyExperimentBacklog, message, originFromItem(item)); err != nil {
		return tools.ErrorResult(err.Error())
	}
	updated, err := t.feature.store.updateStatus(tenantID, itemID, statusDone, "accepted and dispatched to backlog transition planner", map[string]string{
		"transition": "accepted_to_backlog_planner",
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return jsonResult(map[string]any{"status": "transition_dispatched", "item": updated, "agent": agentKeyExperimentBacklog})
}

func (t *boardTool) reviseExperiment(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	itemID := stringArg(args, "item_id")
	if itemID == "" {
		return tools.ErrorResult("item_id is required")
	}
	feedback := stringArg(args, "feedback", "result")
	if feedback == "" {
		return tools.ErrorResult("feedback is required")
	}
	item, err := t.feature.store.getItem(tenantID, itemID)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	body := item.Body + "\n\nReview feedback to address:\n" + feedback
	newItems, err := t.feature.store.addItems(tenantID, kindExperiment, []string{body}, originFromItem(item), "experiment-review-feedback", map[string]string{
		"source_experiment": item.ID,
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	updated, err := t.feature.store.updateStatus(tenantID, itemID, statusDropped, "superseded by revision item", map[string]string{
		"transition": "superseded_by_revision",
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return jsonResult(map[string]any{"status": "revision_queued", "old_item": updated, "new_items": newItems})
}

type ciFailureTool struct {
	feature *SkynetWorkflowsFeature
}

func (t *ciFailureTool) Name() string { return "skynet_ci_failure" }

func (t *ciFailureTool) Description() string {
	return "Trigger the Skynet CI fixer agent from CI/CD failure text, GitHub run URLs, pull request metadata, or webhook payloads."
}

func (t *ciFailureTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":       map[string]any{"type": "string", "enum": []string{"trigger"}},
			"log":          map[string]any{"type": "string", "description": "Failure log or notification text."},
			"repository":   map[string]any{"type": "string"},
			"run_url":      map[string]any{"type": "string"},
			"pull_request": map[string]any{"type": "string"},
			"branch":       map[string]any{"type": "string"},
			"commit":       map[string]any{"type": "string"},
			"author":       map[string]any{"type": "string"},
			"target_repo":  map[string]any{"type": "string"},
			"channel":      map[string]any{"type": "string"},
			"chat_id":      map[string]any{"type": "string"},
			"local_key":    map[string]any{"type": "string"},
		},
		"required": []string{"log"},
	}
}

func (t *ciFailureTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	logText := stringArg(args, "log", "text")
	if logText == "" {
		return tools.ErrorResult("log is required")
	}
	if targetRepo := stringArg(args, "target_repo"); targetRepo != "" {
		if err := t.feature.setTargetRepo(ctx, targetRepo); err != nil {
			return tools.ErrorResult(err.Error())
		}
	}
	message := buildCIFailureMessage(t.feature.resolveTargetRepo(ctx), logText, args)
	if err := t.feature.dispatchAgent(ctx, agentKeyCIFixer, message, originFromToolContext(ctx, args)); err != nil {
		return tools.ErrorResult(err.Error())
	}
	return jsonResult(map[string]any{"status": "dispatched", "agent": agentKeyCIFixer})
}

type feedbackPlanTool struct {
	feature *SkynetWorkflowsFeature
}

func (t *feedbackPlanTool) Name() string { return "skynet_feedback_plan" }

func (t *feedbackPlanTool) Description() string {
	return "Trigger the Skynet feedback planner to inspect the local deployment and generate backlog bullets from user feedback."
}

func (t *feedbackPlanTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":         map[string]any{"type": "string", "enum": []string{"trigger"}},
			"feedback":       map[string]any{"type": "string"},
			"deployment_url": map[string]any{"type": "string"},
			"context":        map[string]any{"type": "string"},
			"target_repo":    map[string]any{"type": "string"},
			"channel":        map[string]any{"type": "string"},
			"chat_id":        map[string]any{"type": "string"},
			"local_key":      map[string]any{"type": "string"},
		},
		"required": []string{"feedback"},
	}
}

func (t *feedbackPlanTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	feedback := stringArg(args, "feedback", "text")
	if feedback == "" {
		return tools.ErrorResult("feedback is required")
	}
	if targetRepo := stringArg(args, "target_repo"); targetRepo != "" {
		if err := t.feature.setTargetRepo(ctx, targetRepo); err != nil {
			return tools.ErrorResult(err.Error())
		}
	}
	message := fmt.Sprintf(`[Skynet Feedback Planning]

Target repository: %s
Deployment URL: %s

User feedback:
%s

Additional context:
%s

Inspect the local deployment or repository enough to turn this feedback into concrete backlog bullets. Then call skynet_backlog with action "add". Do not implement in this planning role.
`, t.feature.resolveTargetRepo(ctx), stringArg(args, "deployment_url"), feedback, stringArg(args, "context"))
	if err := t.feature.dispatchAgent(ctx, agentKeyFeedbackPlanner, message, originFromToolContext(ctx, args)); err != nil {
		return tools.ErrorResult(err.Error())
	}
	return jsonResult(map[string]any{"status": "dispatched", "agent": agentKeyFeedbackPlanner})
}

func (f *SkynetWorkflowsFeature) saveConfiguredOrigin(ctx context.Context, origin workflowOrigin) error {
	if f.sysConfigs == nil {
		return nil
	}
	if origin.Channel != "" {
		if err := f.sysConfigs.Set(ctx, configKeyChannel, origin.Channel); err != nil {
			return err
		}
	}
	if origin.ChatID != "" {
		if err := f.sysConfigs.Set(ctx, configKeyChatID, origin.ChatID); err != nil {
			return err
		}
	}
	if origin.LocalKey != "" {
		if err := f.sysConfigs.Set(ctx, configKeyLocalKey, origin.LocalKey); err != nil {
			return err
		}
	}
	if origin.PeerKind != "" {
		if err := f.sysConfigs.Set(ctx, configKeyPeerKind, origin.PeerKind); err != nil {
			return err
		}
	}
	return nil
}

func (f *SkynetWorkflowsFeature) configuredOrigin(ctx context.Context) workflowOrigin {
	var origin workflowOrigin
	if f.sysConfigs == nil {
		return origin
	}
	if value, err := f.sysConfigs.Get(ctx, configKeyChannel); err == nil {
		origin.Channel = strings.TrimSpace(value)
	}
	if value, err := f.sysConfigs.Get(ctx, configKeyChatID); err == nil {
		origin.ChatID = strings.TrimSpace(value)
	}
	if value, err := f.sysConfigs.Get(ctx, configKeyLocalKey); err == nil {
		origin.LocalKey = strings.TrimSpace(value)
	}
	if value, err := f.sysConfigs.Get(ctx, configKeyPeerKind); err == nil {
		origin.PeerKind = strings.TrimSpace(value)
	}
	return origin
}

func (f *SkynetWorkflowsFeature) dispatchAgent(ctx context.Context, agentKey, content string, origin workflowOrigin) error {
	if f.msgBus == nil {
		return fmt.Errorf("message bus is unavailable")
	}
	if origin.Channel == "" {
		origin = f.configuredOrigin(ctx)
	}
	channel := origin.Channel
	if channel == "" {
		channel = tools.ChannelSystem
	}
	chatID := origin.ChatID
	if origin.LocalKey != "" {
		chatID = origin.LocalKey
	}
	if chatID == "" {
		chatID = "skynet:" + agentKey
	}
	peerKind := origin.PeerKind
	if peerKind == "" && strings.Contains(chatID, ":topic:") {
		peerKind = "group"
	}
	tenantID := storepkg.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		tenantID = tenantIDFromString("")
	}
	meta := map[string]string{
		"skynet_workflow": "true",
	}
	if origin.LocalKey != "" {
		meta["local_key"] = origin.LocalKey
		if threadID := threadIDFromLocalKey(origin.LocalKey); threadID != "" {
			meta[tools.MetaIsForum] = "true"
			meta[tools.MetaMessageThreadID] = threadID
		}
	}
	if !f.msgBus.TryPublishInbound(bus.InboundMessage{
		Channel:  channel,
		SenderID: "system:skynet_workflows",
		ChatID:   chatID,
		Content:  content,
		PeerKind: peerKind,
		TenantID: tenantID,
		AgentID:  agentKey,
		UserID:   "",
		Metadata: meta,
	}) {
		return fmt.Errorf("inbound buffer is full")
	}
	return nil
}

func (f *SkynetWorkflowsFeature) publishExperimentReview(ctx context.Context, item *workflowItem, result string) error {
	if f.msgBus == nil {
		return fmt.Errorf("message bus is unavailable")
	}
	origin := originFromItem(item)
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		origin = f.configuredOrigin(ctx)
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		return fmt.Errorf("review channel is not configured")
	}
	chatID := origin.ChatID
	if origin.LocalKey != "" {
		chatID = origin.LocalKey
	}
	metadata := map[string]string{}
	if origin.LocalKey != "" {
		metadata["local_key"] = origin.LocalKey
		if threadID := threadIDFromLocalKey(origin.LocalKey); threadID != "" {
			metadata[tools.MetaMessageThreadID] = threadID
		}
	}
	f.msgBus.PublishOutbound(bus.OutboundMessage{
		Channel:  origin.Channel,
		ChatID:   chatID,
		Content:  experimentReviewMessage(item, result),
		Metadata: metadata,
	})
	return nil
}

func (f *SkynetWorkflowsFeature) publishWorkflowUpdate(ctx context.Context, item *workflowItem, transition, result string) error {
	if f.msgBus == nil {
		return fmt.Errorf("message bus is unavailable")
	}
	if item == nil {
		return fmt.Errorf("workflow item is nil")
	}
	origin := originFromItem(item)
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		origin = f.configuredOrigin(ctx)
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		return fmt.Errorf("workflow channel is not configured")
	}
	chatID := origin.ChatID
	if origin.LocalKey != "" {
		chatID = origin.LocalKey
	}
	metadata := map[string]string{}
	if origin.LocalKey != "" {
		metadata["local_key"] = origin.LocalKey
		if threadID := threadIDFromLocalKey(origin.LocalKey); threadID != "" {
			metadata[tools.MetaMessageThreadID] = threadID
		}
	}
	content := workflowUpdateMessage(item, transition, result)
	f.msgBus.PublishOutbound(bus.OutboundMessage{
		Channel:  origin.Channel,
		ChatID:   chatID,
		Content:  content,
		Metadata: metadata,
	})
	return nil
}

func (f *SkynetWorkflowsFeature) publishQueueIdleLog(ctx context.Context, kind, tenantID string, origin workflowOrigin) (string, error) {
	if f.msgBus == nil {
		return "skipped", fmt.Errorf("message bus is unavailable")
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		origin = f.configuredOrigin(ctx)
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		return "skipped", fmt.Errorf("workflow channel is not configured")
	}

	now := time.Now().UTC()
	throttle := queueIdleLogInterval(kind)
	configCtx := storepkg.WithTenantID(ctx, tenantIDFromString(tenantID))
	configKey := configKeyQueueIdleLog(kind)
	if f.sysConfigs != nil && throttle > 0 {
		if lastRaw, err := f.sysConfigs.Get(configCtx, configKey); err == nil {
			if lastAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lastRaw)); err == nil && now.Sub(lastAt) < throttle {
				return "throttled", nil
			}
		}
	}

	counts, err := f.store.counts(tenantID)
	if err != nil {
		return "skipped", err
	}
	var queue queueCounts
	for _, entry := range counts {
		if entry.Kind == kind {
			queue = entry
			break
		}
	}
	if queue.Kind == "" {
		queue = queueCounts{Kind: kind}
	}

	chatID := origin.ChatID
	if origin.LocalKey != "" {
		chatID = origin.LocalKey
	}
	metadata := map[string]string{}
	if origin.LocalKey != "" {
		metadata["local_key"] = origin.LocalKey
		if threadID := threadIDFromLocalKey(origin.LocalKey); threadID != "" {
			metadata[tools.MetaMessageThreadID] = threadID
		}
	}
	f.msgBus.PublishOutbound(bus.OutboundMessage{
		Channel:  origin.Channel,
		ChatID:   chatID,
		Content:  queueIdleLogMessage(queue, throttle),
		Metadata: metadata,
	})
	if f.sysConfigs != nil {
		if err := f.sysConfigs.Set(configCtx, configKey, now.Format(time.RFC3339Nano)); err != nil {
			slog.Warn("skynet workflow idle log throttle update failed", "kind", kind, "error", err)
		}
	}
	return "published", nil
}

func workflowUpdateMessage(item *workflowItem, transition, result string) string {
	transition = strings.TrimSpace(transition)
	if transition == "" {
		transition = strings.ToUpper(item.Status)
	}
	result = strings.TrimSpace(result)
	body := strings.TrimSpace(item.Body)
	if len(body) > 700 {
		body = body[:700] + "..."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[SKYNET %s %s]\nItem: %s\nAgent: %s\n\n%s",
		strings.ToUpper(item.Kind), transition, item.ID, item.AgentKey, body)
	if result != "" {
		if len(result) > 900 {
			result = result[:900] + "..."
		}
		fmt.Fprintf(&b, "\n\nResult:\n%s", result)
	}
	return b.String()
}

func queueIdleLogMessage(queue queueCounts, throttle time.Duration) string {
	kind := strings.ToUpper(queue.Kind)
	var b strings.Builder
	fmt.Fprintf(&b, "[SKYNET %s IDLE]\nNo pending %s item.\n\nQueue: pending=%d in_progress=%d review=%d needs_refinement=%d done=%d failed=%d dropped=%d",
		kind,
		queue.Kind,
		queue.Pending,
		queue.InProgress,
		queue.Review,
		queue.Refinement,
		queue.Done,
		queue.Failed,
		queue.Dropped,
	)
	if throttle > 0 {
		fmt.Fprintf(&b, "\nIdle logs for this queue are throttled for %s.", throttle.Round(time.Minute))
	}
	return b.String()
}

func queueIdleLogInterval(kind string) time.Duration {
	switch kind {
	case kindExperiment:
		return time.Hour
	default:
		return 30 * time.Minute
	}
}

func configKeyQueueIdleLog(kind string) string {
	return "beta.skynet_workflows.last_idle_log." + strings.TrimSpace(kind)
}

func experimentReviewMessage(item *workflowItem, result string) string {
	return fmt.Sprintf(`[SKYNET EXPERIMENT REVIEW]
Item: %s

Experiment:
%s

Result:
%s

Reply with approval to transition this into backlog, or send revision feedback to keep iterating.
`, item.ID, item.Body, result)
}

func originFromToolContext(ctx context.Context, args map[string]any) workflowOrigin {
	origin := workflowOrigin{
		Channel:  stringArg(args, "channel"),
		ChatID:   stringArg(args, "chat_id", "target"),
		LocalKey: stringArg(args, "local_key"),
		PeerKind: stringArg(args, "peer_kind"),
	}
	if origin.Channel == "" {
		origin.Channel = tools.ToolChannelFromCtx(ctx)
	}
	if origin.ChatID == "" {
		origin.ChatID = tools.ToolChatIDFromCtx(ctx)
	}
	if origin.LocalKey == "" {
		origin.LocalKey = tools.ToolLocalKeyFromCtx(ctx)
	}
	if origin.PeerKind == "" {
		origin.PeerKind = tools.ToolPeerKindFromCtx(ctx)
	}
	return origin
}

func originFromItem(item *workflowItem) workflowOrigin {
	if item == nil {
		return workflowOrigin{}
	}
	return workflowOrigin{
		Channel:  item.Channel,
		ChatID:   item.ChatID,
		LocalKey: item.LocalKey,
	}
}

func jsonResult(payload any) *tools.Result {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(string(data))
}

func defaultWorkerForKind(kind string) string {
	switch kind {
	case kindBacklog:
		return agentKeyBacklogIterator
	case kindExperiment:
		return agentKeyExperimentIterator
	case kindQA:
		return agentKeyQAIterator
	case kindPR:
		return agentKeyPRComposer
	default:
		return agentKeySkynet
	}
}

func instructionsForKind(kind string) string {
	switch kind {
	case kindBacklog:
		return "Validate the backlog item. If it is a broad rollup, milestone, stale, or missing acceptance criteria, use skynet_backlog action \"refine\" with child backlog bullets. If actionable, implement it in the target repository, run focused verification, write or update the QA report, then complete or fail the item with skynet_backlog. Complete queues QA automatically."
	case kindExperiment:
		return "Build or update a sandbox experiment, validate it, then request channel review with skynet_experiments."
	case kindQA:
		return "Verify the QA item. Pass it if behavior is correct; this queues skynet_pr work automatically. Otherwise use fail_to_backlog with exact failure evidence."
	case kindPR:
		return "Compose a comprehensive PR from the QA-passed work. Inspect git status/commits, cherry-pick or stage only coherent scoped changes, verify, then complete with branch, commits, files, verification, and PR URL/body or fail with blockers."
	default:
		return "Process one item and update its status."
	}
}

func buildCIFailureMessage(targetRepo, logText string, args map[string]any) string {
	return fmt.Sprintf(`[Skynet CI/CD Failure]

Target repository: %s
Repository: %s
Pull request: %s
Branch: %s
Commit: %s
Author: %s
Run URL: %s

Failure log:
%s

Inspect the target repository, reproduce or narrow the failure, patch only the scoped CI fix files, run focused verification, commit the scoped fix, and push the PR branch when auth/remotes allow it. Preserve unrelated dirty work. Report the files changed, verification result, branch, and pushed commit. If a safe fix or push is not possible, add a backlog item with the exact blocker and evidence.
`, targetRepo, stringArg(args, "repository"), stringArg(args, "pull_request"), stringArg(args, "branch"), stringArg(args, "commit"), stringArg(args, "author"), stringArg(args, "run_url"), logText)
}
