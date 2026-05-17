package skynetworkflows

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
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
			"deploy_repo": map[string]any{"type": "string", "description": "Absolute path of the clean main deployment worktree."},
			"web_port":    map[string]any{"type": "string", "description": "Local web port for main deployment refreshes."},
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
		if deployRepo := stringArg(args, "deploy_repo"); deployRepo != "" {
			if err := t.feature.setDeployRepo(ctx, deployRepo); err != nil {
				return tools.ErrorResult(err.Error())
			}
		}
		if webPort := stringArg(args, "web_port"); webPort != "" {
			if err := t.feature.setWebPort(ctx, webPort); err != nil {
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
			"deploy_repo": t.feature.configuredDeployRepo(ctx),
			"web_port":    t.feature.configuredWebPort(ctx),
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
		"deploy_repo": t.feature.configuredDeployRepo(ctx),
		"web_port":    t.feature.configuredWebPort(ctx),
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
	case kindChangeReq:
		return "Submit and review Skynet change requests. CRs require explicit human review before they can become experiments or backlog work."
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
					"claim_related", "pass", "fail_to_backlog", "request_review", "review_reminders", "approve_to_backlog", "approve_to_experiment", "revise", "drop", "submit",
				},
			},
			"text":            map[string]any{"type": "string", "description": "Raw text or bullet list to add. For refine/split, this is the child backlog bullet list to enqueue."},
			"item_id":         map[string]any{"type": "string", "description": "Workflow item ID for status transitions."},
			"item_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional list of related workflow item IDs for batch claim or batch completion."},
			"result":          map[string]any{"type": "string", "description": "Result, validation summary, review request, failure evidence, or transition notes."},
			"feedback":        map[string]any{"type": "string", "description": "User feedback for experiment revision or transition."},
			"status":          map[string]any{"type": "string", "description": "Optional status filter for list."},
			"limit":           map[string]any{"type": "integer", "description": "Optional list limit, max 100."},
			"source":          map[string]any{"type": "string", "description": "Optional source label."},
			"category":        map[string]any{"type": "string", "description": "Optional change-request category, such as ui_ux or functional."},
			"route":           map[string]any{"type": "string", "description": "Optional change-request route: experiment for UI/UX work, backlog for functional-only work."},
			"suggested_route": map[string]any{"type": "string", "description": "Optional alias for route."},
			"erp":             map[string]any{"type": "string", "description": "Optional ERP/package slug related to a change request."},
			"deployment_url":  map[string]any{"type": "string", "description": "Optional deployment URL reviewed for a change request."},
			"agent_key":       map[string]any{"type": "string", "description": "Optional claiming agent key."},
			"channel":         map[string]any{"type": "string", "description": "Optional channel override."},
			"chat_id":         map[string]any{"type": "string", "description": "Optional chat override."},
			"local_key":       map[string]any{"type": "string", "description": "Optional topic/thread local key override."},
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
	case "add", "parse", "submit":
		if t.kind == kindChangeReq {
			return t.submitChangeRequest(ctx, tenantID, args)
		}
		if action == "submit" {
			return tools.ErrorResult("submit is only valid for skynet_change_requests")
		}
		return t.add(ctx, tenantID, args)
	case "sync_repo":
		switch t.kind {
		case kindBacklog:
			result, err := t.feature.syncRepoBacklog(ctx, tenantID)
			if err != nil {
				return tools.ErrorResult(err.Error())
			}
			return jsonResult(map[string]any{"status": "synced", "repo_sync": result})
		case kindExperiment:
			result, err := t.feature.syncRepoExperiments(ctx, tenantID)
			if err != nil {
				return tools.ErrorResult(err.Error())
			}
			return jsonResult(map[string]any{"status": "synced", "repo_sync": result})
		default:
			return tools.ErrorResult("sync_repo is only valid for skynet_backlog and skynet_experiments")
		}
	case "list":
		var syncResult *repoBacklogSyncResult
		var experimentSyncResult *repoExperimentSyncResult
		statusFilter := stringArg(args, "status")
		if t.kind == kindBacklog && (statusFilter == "" || statusFilter == statusPending) {
			var err error
			syncResult, err = t.feature.syncRepoBacklog(ctx, tenantID)
			if err != nil {
				return tools.ErrorResult(err.Error())
			}
		}
		if t.kind == kindExperiment && (statusFilter == "" || statusFilter == statusPending) {
			var err error
			experimentSyncResult, err = t.feature.syncRepoExperiments(ctx, tenantID)
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
		if experimentSyncResult != nil {
			payload["repo_sync"] = experimentSyncResult
		}
		return jsonResult(payload)
	case "next":
		if t.kind == kindChangeReq {
			return tools.ErrorResult("change requests require human review; use list/review_reminders, then approve_to_experiment or approve_to_backlog")
		}
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
	case "claim_related":
		if t.kind != kindBacklog {
			return tools.ErrorResult("claim_related is only valid for skynet_backlog")
		}
		return t.claimRelatedBacklog(ctx, tenantID, args)
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
	case "review_reminders":
		switch t.kind {
		case kindExperiment:
			return t.publishExperimentReviewReminders(ctx, tenantID, args)
		case kindChangeReq:
			return t.publishChangeRequestReviewReminders(ctx, tenantID, args)
		default:
			return tools.ErrorResult("review_reminders is only valid for skynet_experiments and skynet_change_requests")
		}
	case "approve_to_backlog":
		switch t.kind {
		case kindExperiment:
			return t.approveExperimentToBacklog(ctx, tenantID, args)
		case kindChangeReq:
			return t.approveChangeRequest(ctx, tenantID, args, kindBacklog)
		default:
			return tools.ErrorResult("approve_to_backlog is only valid for skynet_experiments and skynet_change_requests")
		}
	case "approve_to_experiment":
		if t.kind != kindChangeReq {
			return tools.ErrorResult("approve_to_experiment is only valid for skynet_change_requests")
		}
		return t.approveChangeRequest(ctx, tenantID, args, kindExperiment)
	case "revise":
		switch t.kind {
		case kindExperiment:
			return t.reviseExperiment(ctx, tenantID, args)
		case kindChangeReq:
			return t.reviseChangeRequest(ctx, tenantID, args)
		default:
			return tools.ErrorResult("revise is only valid for skynet_experiments and skynet_change_requests")
		}
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
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
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
	if item == nil && t.kind == kindExperiment {
		if _, err := t.feature.syncRepoExperiments(ctx, tenantID); err != nil {
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
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
	payload := map[string]any{
		"status":       "claimed",
		"item":         item,
		"instructions": instructionsForKind(t.kind),
	}
	if t.kind == kindBacklog {
		related, err := t.feature.store.relatedPending(tenantID, item, intArg(args, "related_limit"))
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		payload["related_items"] = related
		if len(related) > 0 {
			payload["batch_hint"] = "If these pending items are tightly related and can be implemented safely in one focused change, call skynet_backlog action \"claim_related\" with item_ids before editing. Otherwise ignore them and handle only the claimed item."
		}
	}
	return jsonResult(payload)
}

func (t *boardTool) claimRelatedBacklog(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	ids := itemIDsArg(args)
	if len(ids) == 0 {
		return tools.ErrorResult("item_ids is required")
	}
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
	items, err := t.feature.store.claimPendingByIDs(tenantID, kindBacklog, claimedBy, agentKey, ids)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	for i := range items {
		item := &items[i]
		if err := t.feature.publishWorkflowUpdate(ctx, item, "CLAIMED RELATED", "claimed as part of a related backlog batch"); err != nil {
			slog.Warn("skynet related backlog claim notification failed", "item_id", item.ID, "error", err)
		}
	}
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
	return jsonResult(map[string]any{"status": "claimed_related", "items": items})
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
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
	return jsonResult(map[string]any{"status": status, "item": item})
}

func (t *boardTool) completeBacklogToQA(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	itemIDs := itemIDsArg(args)
	if len(itemIDs) == 0 {
		return tools.ErrorResult("item_id or item_ids is required")
	}
	result := stringArg(args, "result")
	if result == "" {
		result = "coding completed"
	}
	items := make([]*workflowItem, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		item, err := t.feature.store.getItem(tenantID, itemID)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		items = append(items, item)
	}
	primary := items[0]

	var qaItems []workflowItem
	qaItemID := strings.TrimSpace(primary.Metadata["qa_item"])
	if qaItemID == "" {
		var err error
		itemLines := make([]string, 0, len(items))
		for _, item := range items {
			itemLines = append(itemLines, fmt.Sprintf("- %s\n%s", item.ID, item.Body))
		}
		qaText := fmt.Sprintf(`Verify backlog-completed work from %s.

Backlog item(s):
%s

Implementation result:
%s

QA requirements:
- Inspect the target repository changes and any repo-root qa/ report created by the implementation worker.
- Run focused verification for the changed behavior.
- If behavior passes, call skynet_qa with action "pass" so PR composition is queued.
- If behavior fails, call skynet_qa with action "fail_to_backlog" with exact reproduction evidence.`, primary.ID, strings.Join(itemLines, "\n\n"), result)
		childMetadata := map[string]string{
			"created_by_agent": tools.ToolAgentKeyFromCtx(ctx),
			"source_backlog":   primary.ID,
			"source_backlogs":  strings.Join(itemIDs, ","),
			"transition":       "backlog_completed_to_qa",
		}
		if sourcePath := primary.Metadata["source_path"]; sourcePath != "" {
			childMetadata["parent_source_path"] = sourcePath
		}
		if sourceLine := primary.Metadata["source_line"]; sourceLine != "" {
			childMetadata["parent_source_line"] = sourceLine
		}
		qaItems, err = t.feature.store.addItems(tenantID, kindQA, []string{qaText}, originFromItem(primary), "backlog-complete:"+primary.ID, childMetadata)
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
	updatedItems := make([]workflowItem, 0, len(items))
	for _, item := range items {
		updated, err := t.feature.store.updateStatus(tenantID, item.ID, statusDone, updateResult, map[string]string{
			"transition":       "backlog_completed_to_qa",
			"updated_by_agent": tools.ToolAgentKeyFromCtx(ctx),
			"qa_item_count":    fmt.Sprintf("%d", len(qaItems)),
			"qa_item":          qaItemID,
			"batch_items":      strings.Join(itemIDs, ","),
		})
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		updatedItems = append(updatedItems, *updated)
		if err := t.feature.publishWorkflowUpdate(ctx, updated, "DONE -> QA", updateResult); err != nil {
			slog.Warn("skynet backlog completion-to-QA notification failed", "item_id", updated.ID, "error", err)
		}
	}
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
	return jsonResult(map[string]any{
		"status":   "queued_for_qa",
		"items":    updatedItems,
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
- Before declaring the work missing, inspect current worktrees, git log --all for expected files, branch containment for referenced commits, git stash list, tracked stash diffs, and untracked stash parents such as refs/stash^3. Use git ls-tree -r refs/stash^3 and git show refs/stash^3:<path> when untracked artifacts may hold the completed work.
- If scoped artifacts are found in a stash, recover only those files into a clean PR branch and continue.
- Cherry-pick finished commits when they exist; otherwise stage and commit only the coherent files for this scope.
- Exclude unrelated dirty work and never revert user changes.
- Run focused verification.
- Create a GitHub PR if auth/remotes allow it, or produce a PR-ready title/body and exact blocker if creation is blocked.
- Never backslash-escape Markdown backtick characters in the PR title or body; preserve inline code and fenced code blocks as normal GitHub Markdown.
- Prefer writing the PR body to a temporary Markdown file and using gh pr create --body-file so shell quoting does not force Markdown escaping or command substitution.
- For important architecture PRs, include an Architecture section with Before and After Mermaid diagrams. Validate grammar before publishing: use a valid directive such as flowchart LR, graph TD, sequenceDiagram, stateDiagram-v2, or gantt; keep node IDs simple; quote labels with punctuation; close brackets/arrows; add dateFormat for gantt; and keep sequence participants/messages syntactically valid.
- For frontend feature PRs, render the affected route/component and capture visual evidence with a browser or Playwright snapshot/screenshot, or an equivalent rendered artifact. Include the artifact path/link, viewport, and any render caveat in the PR body.`, item.ID, item.Body, result)
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
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
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
	var backlogChildren []workflowItem
	var experimentChildren []workflowItem
	if childText != "" {
		backlogBullets, experimentBullets := partitionRefinementItems(parseBulletItems(childText))
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
		if len(backlogBullets) > 0 {
			backlogChildren, err = t.feature.store.addItems(
				tenantID,
				kindBacklog,
				backlogBullets,
				originFromItem(item),
				"backlog-refinement:"+item.ID,
				childMetadata,
			)
			if err != nil {
				return tools.ErrorResult(err.Error())
			}
			childItems = append(childItems, backlogChildren...)
		}
		if len(experimentBullets) > 0 {
			experimentMetadata := cloneStringMap(childMetadata)
			experimentMetadata["transition"] = "backlog_refinement_to_experiment"
			experimentMetadata["source_kind"] = "backlog_refinement_experiment"
			experimentChildren, err = t.feature.store.addItems(
				tenantID,
				kindExperiment,
				experimentBullets,
				originFromItem(item),
				"backlog-refinement-experiment:"+item.ID,
				experimentMetadata,
			)
			if err != nil {
				return tools.ErrorResult(err.Error())
			}
			childItems = append(childItems, experimentChildren...)
		}
	}
	if result == "" {
		result = fmt.Sprintf("Backlog item refined into %d child item(s).", len(childItems))
	}
	updated, err := t.feature.store.updateStatus(tenantID, itemID, statusRefinement, result, map[string]string{
		"transition":                     "backlog_refinement",
		"updated_by_agent":               tools.ToolAgentKeyFromCtx(ctx),
		"refined_child_count":            fmt.Sprintf("%d", len(childItems)),
		"refined_backlog_child_count":    fmt.Sprintf("%d", len(backlogChildren)),
		"refined_experiment_child_count": fmt.Sprintf("%d", len(experimentChildren)),
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if err := t.feature.publishWorkflowUpdate(ctx, updated, "NEEDS REFINEMENT", result); err != nil {
		slog.Warn("skynet backlog refinement notification failed", "item_id", updated.ID, "error", err)
	}
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
	return jsonResult(map[string]any{
		"status":           statusRefinement,
		"item":             updated,
		"child_items":      childItems,
		"backlog_items":    backlogChildren,
		"experiment_items": experimentChildren,
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
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
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
		t.feature.refreshRepoWorkflowReference(ctx, tenantID)
		return jsonResult(map[string]any{
			"status":  "review_pending_delivery",
			"item":    updated,
			"warning": err.Error(),
			"message": experimentReviewMessage(updated, result, t.feature.resolveTargetRepo(ctx)),
		})
	}
	_ = item
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
	return jsonResult(map[string]any{"status": "review_requested", "item": updated})
}

func (t *boardTool) publishExperimentReviewReminders(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	limit := intArg(args, "limit")
	published, total, err := t.feature.publishExperimentReviewReminders(ctx, tenantID, originFromToolContext(ctx, args), limit)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	status := "no_review_items"
	if published > 0 {
		status = "review_reminder_published"
	}
	return jsonResult(map[string]any{
		"status":          status,
		"published_count": published,
		"review_count":    total,
	})
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
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
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
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
	return jsonResult(map[string]any{"status": "revision_queued", "old_item": updated, "new_items": newItems})
}

func (t *boardTool) submitChangeRequest(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	text := stringArg(args, "text")
	if text == "" {
		return tools.ErrorResult("text is required")
	}
	category := normalizeChangeRequestCategory(stringArg(args, "category"))
	route := normalizeChangeRequestRoute(stringArg(args, "route", "suggested_route"))
	if route == "" {
		route = inferChangeRequestRoute(text, category)
	}
	if category == "" {
		category = defaultChangeRequestCategory(route)
	}
	result := stringArg(args, "result")
	if result == "" {
		result = "awaiting human review"
	}

	metadata := map[string]string{
		"created_by_agent": tools.ToolAgentKeyFromCtx(ctx),
		"transition":       "change_request_review_requested",
		"suggested_route":  route,
		"category":         category,
		"human_review":     "required",
	}
	if erp := stringArg(args, "erp", "erp_slug"); erp != "" {
		metadata["erp"] = erp
	}
	if deploymentURL := stringArg(args, "deployment_url"); deploymentURL != "" {
		metadata["deployment_url"] = deploymentURL
	}

	items, err := t.feature.store.addItems(tenantID, kindChangeReq, []string{strings.TrimSpace(text)}, originFromToolContext(ctx, args), stringArg(args, "source"), metadata)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	updatedItems := make([]workflowItem, 0, len(items))
	warnings := make([]string, 0)
	for _, item := range items {
		updated, err := t.feature.store.updateStatus(tenantID, item.ID, statusReview, result, metadata)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		updatedItems = append(updatedItems, *updated)
		if err := t.feature.publishChangeRequestReview(ctx, updated, result); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %s", updated.ID, err.Error()))
		}
	}
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)

	status := "review_requested"
	if len(warnings) > 0 {
		status = "review_pending_delivery"
	}
	return jsonResult(map[string]any{
		"status":          status,
		"count":           len(updatedItems),
		"items":           updatedItems,
		"suggested_route": route,
		"category":        category,
		"warnings":        warnings,
	})
}

func (t *boardTool) publishChangeRequestReviewReminders(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
	limit := intArg(args, "limit")
	published, total, err := t.feature.publishChangeRequestReviewReminders(ctx, tenantID, originFromToolContext(ctx, args), limit)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	status := "no_review_items"
	if published > 0 {
		status = "review_reminder_published"
	}
	return jsonResult(map[string]any{
		"status":          status,
		"published_count": published,
		"review_count":    total,
	})
}

func (t *boardTool) approveChangeRequest(ctx context.Context, tenantID string, args map[string]any, targetKind string) *tools.Result {
	itemID := stringArg(args, "item_id")
	if itemID == "" {
		return tools.ErrorResult("item_id is required")
	}
	if targetKind != kindExperiment && targetKind != kindBacklog {
		return tools.ErrorResult("change requests can only be approved to experiment or backlog")
	}
	item, err := t.feature.store.getItem(tenantID, itemID)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if item.Kind != kindChangeReq {
		return tools.ErrorResult("item is not a change request")
	}
	if item.Status != statusReview {
		return tools.ErrorResult("change request must be in review before approval")
	}

	feedback := stringArg(args, "feedback", "result")
	targetText := changeRequestApprovalBody(item, targetKind, feedback)
	metadata := map[string]string{
		"created_by_agent":      tools.ToolAgentKeyFromCtx(ctx),
		"source_change_request": item.ID,
		"transition":            "change_request_approved_to_" + targetKind,
		"human_reviewed":        "true",
		"category":              item.Metadata["category"],
		"suggested_route":       item.Metadata["suggested_route"],
	}
	if metadata["suggested_route"] == "" {
		metadata["suggested_route"] = targetKind
	}
	if erp := item.Metadata["erp"]; erp != "" {
		metadata["erp"] = erp
	}
	if deploymentURL := item.Metadata["deployment_url"]; deploymentURL != "" {
		metadata["deployment_url"] = deploymentURL
	}

	targetItems, err := t.feature.store.addItems(tenantID, targetKind, []string{targetText}, originFromItem(item), "change-request:"+item.ID, metadata)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	queuedID := ""
	if len(targetItems) > 0 {
		queuedID = targetItems[0].ID
	}
	result := fmt.Sprintf("approved by human review and queued as %s item %s", targetKind, queuedID)
	if feedback != "" {
		result += "\n\nReviewer feedback:\n" + feedback
	}
	updated, err := t.feature.store.updateStatus(tenantID, item.ID, statusDone, result, map[string]string{
		"transition":        "change_request_approved_to_" + targetKind,
		"updated_by_agent":  tools.ToolAgentKeyFromCtx(ctx),
		"queued_kind":       targetKind,
		"queued_item":       queuedID,
		"human_reviewed":    "true",
		"review_resolution": "approved",
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if err := t.feature.publishWorkflowUpdate(ctx, updated, "APPROVED -> "+strings.ToUpper(targetKind), result); err != nil {
		slog.Warn("skynet change request approval notification failed", "item_id", updated.ID, "error", err)
	}
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
	return jsonResult(map[string]any{
		"status":       "queued_for_" + targetKind,
		"item":         updated,
		"target_kind":  targetKind,
		"target_items": targetItems,
	})
}

func (t *boardTool) reviseChangeRequest(ctx context.Context, tenantID string, args map[string]any) *tools.Result {
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
	if item.Kind != kindChangeReq {
		return tools.ErrorResult("item is not a change request")
	}

	body := item.Body + "\n\nReview feedback to address:\n" + feedback
	metadata := cloneStringMap(item.Metadata)
	metadata["created_by_agent"] = tools.ToolAgentKeyFromCtx(ctx)
	metadata["source_change_request"] = item.ID
	metadata["transition"] = "change_request_revision_requested"
	metadata["human_review"] = "required"
	newItems, err := t.feature.store.addItems(tenantID, kindChangeReq, []string{body}, originFromItem(item), "change-request-revision:"+item.ID, metadata)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	reviewedItems := make([]workflowItem, 0, len(newItems))
	for _, newItem := range newItems {
		updatedNew, err := t.feature.store.updateStatus(tenantID, newItem.ID, statusReview, "revision requested by human review", metadata)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
		reviewedItems = append(reviewedItems, *updatedNew)
		if err := t.feature.publishChangeRequestReview(ctx, updatedNew, "revision requested by human review"); err != nil {
			slog.Warn("skynet change request revision review notification failed", "item_id", updatedNew.ID, "error", err)
		}
	}
	updated, err := t.feature.store.updateStatus(tenantID, itemID, statusDropped, "superseded by revised change request", map[string]string{
		"transition":        "superseded_by_change_request_revision",
		"updated_by_agent":  tools.ToolAgentKeyFromCtx(ctx),
		"review_resolution": "revision_requested",
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	t.feature.refreshRepoWorkflowReference(ctx, tenantID)
	return jsonResult(map[string]any{"status": "revision_queued", "old_item": updated, "new_items": reviewedItems})
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
			"peer_kind":    map[string]any{"type": "string"},
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
	origin := originFromToolContext(ctx, args)
	if err := t.feature.dispatchAgent(ctx, agentKeyCIFixer, message, origin); err != nil {
		return tools.ErrorResult(err.Error())
	}
	t.feature.publishCIFailureDispatchLog(ctx, args, origin)
	return jsonResult(map[string]any{"status": "dispatched", "agent": agentKeyCIFixer})
}

type feedbackPlanTool struct {
	feature *SkynetWorkflowsFeature
}

type prConflictTool struct {
	feature *SkynetWorkflowsFeature
}

func (t *prConflictTool) Name() string { return "skynet_pr_conflict" }

func (t *prConflictTool) Description() string {
	return "Trigger the Skynet PR conflict resolver from GitHub merge-state data for green or otherwise active pull requests that cannot merge cleanly."
}

func (t *prConflictTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":       map[string]any{"type": "string", "enum": []string{"trigger"}},
			"repository":   map[string]any{"type": "string"},
			"pull_request": map[string]any{"type": "string"},
			"title":        map[string]any{"type": "string"},
			"url":          map[string]any{"type": "string"},
			"branch":       map[string]any{"type": "string"},
			"base_branch":  map[string]any{"type": "string"},
			"commit":       map[string]any{"type": "string"},
			"base_sha":     map[string]any{"type": "string"},
			"author":       map[string]any{"type": "string"},
			"merge_state":  map[string]any{"type": "string"},
			"checks":       map[string]any{"type": "string"},
			"target_repo":  map[string]any{"type": "string"},
			"channel":      map[string]any{"type": "string"},
			"chat_id":      map[string]any{"type": "string"},
			"local_key":    map[string]any{"type": "string"},
			"peer_kind":    map[string]any{"type": "string"},
			"force":        map[string]any{"type": "boolean", "description": "Dispatch even if this PR conflict fingerprint was already seen."},
		},
		"required": []string{"pull_request"},
	}
}

func (t *prConflictTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if stringArg(args, "pull_request", "url") == "" {
		return tools.ErrorResult("pull_request or url is required")
	}
	if targetRepo := stringArg(args, "target_repo"); targetRepo != "" {
		if err := t.feature.setTargetRepo(ctx, targetRepo); err != nil {
			return tools.ErrorResult(err.Error())
		}
	}
	status, err := t.feature.dispatchPRConflictResolver(ctx, args, originFromToolContext(ctx, args))
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return jsonResult(map[string]any{"status": status, "agent": agentKeyPRConflictResolver})
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

func (f *SkynetWorkflowsFeature) publishCIFailureDispatchLog(ctx context.Context, args map[string]any, origin workflowOrigin) {
	if f == nil || f.msgBus == nil {
		return
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		origin = f.configuredOrigin(ctx)
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		return
	}
	chatID := origin.ChatID
	if origin.LocalKey != "" {
		chatID = origin.LocalKey
	}
	metadata := map[string]string{
		"skynet_workflow": "true",
	}
	if origin.LocalKey != "" {
		metadata["local_key"] = origin.LocalKey
		if threadID := threadIDFromLocalKey(origin.LocalKey); threadID != "" {
			metadata[tools.MetaMessageThreadID] = threadID
		}
	}
	if !f.msgBus.TryPublishOutbound(bus.OutboundMessage{
		Channel:  origin.Channel,
		ChatID:   chatID,
		Content:  ciFailureDispatchLogMessage(args),
		Metadata: metadata,
	}) {
		slog.Warn("beta skynet_workflows: CI fixer dispatch log dropped; outbound buffer is full")
	}
}

func (f *SkynetWorkflowsFeature) dispatchPRConflictResolver(ctx context.Context, args map[string]any, origin workflowOrigin) (string, error) {
	if f == nil {
		return "", fmt.Errorf("skynet workflows feature is unavailable")
	}
	if !boolArg(args, "force") {
		duplicate, err := f.prConflictAlreadyDispatched(ctx, args)
		if err != nil {
			slog.Warn("skynet PR conflict dedupe check failed", "error", err)
		}
		if duplicate {
			return "skipped_duplicate", nil
		}
	}
	message := buildPRConflictMessage(f.resolveTargetRepo(ctx), args)
	if err := f.dispatchAgent(ctx, agentKeyPRConflictResolver, message, origin); err != nil {
		return "", err
	}
	f.publishPRConflictDispatchLog(ctx, args, origin)
	if err := f.markPRConflictDispatched(ctx, args); err != nil {
		slog.Warn("skynet PR conflict dedupe mark failed", "error", err)
	}
	return "dispatched", nil
}

func (f *SkynetWorkflowsFeature) prConflictAlreadyDispatched(ctx context.Context, args map[string]any) (bool, error) {
	if f == nil || f.sysConfigs == nil {
		return false, nil
	}
	key := prConflictConfigKey(args)
	fingerprint := prConflictFingerprint(args)
	if key == "" || fingerprint == "" {
		return false, nil
	}
	value, err := f.sysConfigs.Get(ctx, key)
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(value) == fingerprint, nil
}

func (f *SkynetWorkflowsFeature) markPRConflictDispatched(ctx context.Context, args map[string]any) error {
	if f == nil || f.sysConfigs == nil {
		return nil
	}
	key := prConflictConfigKey(args)
	fingerprint := prConflictFingerprint(args)
	if key == "" || fingerprint == "" {
		return nil
	}
	return f.sysConfigs.Set(ctx, key, fingerprint)
}

func (f *SkynetWorkflowsFeature) publishPRConflictDispatchLog(ctx context.Context, args map[string]any, origin workflowOrigin) {
	if f == nil || f.msgBus == nil {
		return
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		origin = f.configuredOrigin(ctx)
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		return
	}
	chatID := origin.ChatID
	if origin.LocalKey != "" {
		chatID = origin.LocalKey
	}
	metadata := map[string]string{
		"skynet_workflow": "true",
	}
	if origin.LocalKey != "" {
		metadata["local_key"] = origin.LocalKey
		if threadID := threadIDFromLocalKey(origin.LocalKey); threadID != "" {
			metadata[tools.MetaMessageThreadID] = threadID
		}
	}
	if !f.msgBus.TryPublishOutbound(bus.OutboundMessage{
		Channel:  origin.Channel,
		ChatID:   chatID,
		Content:  prConflictDispatchLogMessage(args),
		Metadata: metadata,
	}) {
		slog.Warn("beta skynet_workflows: PR conflict dispatch log dropped; outbound buffer is full")
	}
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
		Content:  experimentReviewMessage(item, result, f.resolveTargetRepo(ctx)),
		Metadata: metadata,
	})
	return nil
}

func (f *SkynetWorkflowsFeature) publishExperimentReviewReminders(ctx context.Context, tenantID string, origin workflowOrigin, limit int) (int, int, error) {
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	items, err := f.store.listItems(tenantID, kindExperiment, statusReview, limit)
	if err != nil {
		return 0, 0, err
	}
	total := len(items)
	if counts, err := f.store.counts(tenantID); err == nil {
		for _, entry := range counts {
			if entry.Kind == kindExperiment {
				total = entry.Review
				break
			}
		}
	}
	if len(items) == 0 {
		return 0, total, nil
	}
	if f.msgBus == nil {
		return 0, total, fmt.Errorf("message bus is unavailable")
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		origin = f.configuredOrigin(ctx)
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		origin = originFromItem(&items[0])
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		return 0, total, fmt.Errorf("review channel is not configured")
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
		Content:  experimentReviewReminderMessage(items, total, f.resolveTargetRepo(ctx), time.Now().UTC()),
		Metadata: metadata,
	})
	return len(items), total, nil
}

func (f *SkynetWorkflowsFeature) publishChangeRequestReview(ctx context.Context, item *workflowItem, result string) error {
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
		Content:  changeRequestReviewMessage(item, result, f.resolveTargetRepo(ctx)),
		Metadata: metadata,
	})
	return nil
}

func (f *SkynetWorkflowsFeature) publishChangeRequestReviewReminders(ctx context.Context, tenantID string, origin workflowOrigin, limit int) (int, int, error) {
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	items, err := f.store.listItems(tenantID, kindChangeReq, statusReview, limit)
	if err != nil {
		return 0, 0, err
	}
	total := len(items)
	if counts, err := f.store.counts(tenantID); err == nil {
		for _, entry := range counts {
			if entry.Kind == kindChangeReq {
				total = entry.Review
				break
			}
		}
	}
	if len(items) == 0 {
		return 0, total, nil
	}
	if f.msgBus == nil {
		return 0, total, fmt.Errorf("message bus is unavailable")
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		origin = f.configuredOrigin(ctx)
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		origin = originFromItem(&items[0])
	}
	if origin.Channel == "" || (origin.ChatID == "" && origin.LocalKey == "") {
		return 0, total, fmt.Errorf("review channel is not configured")
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
		Content:  changeRequestReviewReminderMessage(items, total, f.resolveTargetRepo(ctx), time.Now().UTC()),
		Metadata: metadata,
	})
	return len(items), total, nil
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
	case kindChangeReq:
		return 5 * time.Minute
	default:
		return 30 * time.Minute
	}
}

func configKeyQueueIdleLog(kind string) string {
	return "beta.skynet_workflows.last_idle_log." + strings.TrimSpace(kind)
}

func experimentReviewMessage(item *workflowItem, result, targetRepo string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `[SKYNET EXPERIMENT REVIEW]
Item: %s

Experiment:
%s

`, item.ID, item.Body)
	appendExperimentAccessLinks(&b, *item, targetRepo)
	fmt.Fprintf(&b, `
Result:
%s

Reply with approval to transition this into backlog, or send revision feedback to keep iterating.
`, result)
	return b.String()
}

func experimentReviewReminderMessage(items []workflowItem, total int, targetRepo string, now time.Time) string {
	if total <= 0 {
		total = len(items)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🟨 **SKYNET EXPERIMENT REVIEW REMINDER**\nPending experiment reviews: %d\n", total)
	fmt.Fprintf(&b, "This reminder repeats every 5 minutes until review items are accepted, revised, dropped, or otherwise moved out of review.\n")
	for i, item := range items {
		fmt.Fprintf(&b, "\n%d. **%s**\nItem: `%s`\nSource: `%s`\n", i+1, experimentReviewTitle(item), item.ID, item.Source)
		if !item.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, "Waiting since: `%s UTC`", item.UpdatedAt.UTC().Format("2006-01-02 15:04"))
			if now.After(item.UpdatedAt) {
				fmt.Fprintf(&b, " (%s)", now.Sub(item.UpdatedAt).Round(time.Minute))
			}
			b.WriteByte('\n')
		}
		appendExperimentAccessLinks(&b, item, targetRepo)
		b.WriteString("Decision needed: approve to backlog, request revision, or drop.\n")
	}
	if len(items) < total {
		fmt.Fprintf(&b, "\nShowing %d of %d review items.", len(items), total)
	}
	return b.String()
}

func changeRequestReviewMessage(item *workflowItem, result, targetRepo string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🟥 **SKYNET CHANGE REQUEST REVIEW REQUIRED**\nItem: %s\nSuggested route: `%s`\nCategory: `%s`\n\nChange request:\n%s\n\n",
		item.ID,
		changeRequestSuggestedRoute(item),
		changeRequestCategory(item),
		item.Body,
	)
	appendRepoWorkflowReferenceLink(&b, targetRepo)
	if result != "" {
		fmt.Fprintf(&b, "\nReview context:\n%s\n", result)
	}
	b.WriteString("\nDecision needed: approve to experiment for UI/UX validation, approve to backlog for functional-only implementation, request revision, or drop.\n")
	return b.String()
}

func changeRequestReviewReminderMessage(items []workflowItem, total int, targetRepo string, now time.Time) string {
	if total <= 0 {
		total = len(items)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🟥 **SKYNET CHANGE REQUEST REVIEW REMINDER**\nPending change-request reviews: %d\n", total)
	fmt.Fprintf(&b, "RED review items block experiments/backlog until a human approves, revises, or drops them. This reminder repeats every 5 minutes while CRs remain in review.\n")
	appendRepoWorkflowReferenceLink(&b, targetRepo)
	for i, item := range items {
		fmt.Fprintf(&b, "\n%d. **%s**\nItem: `%s`\nSource: `%s`\nSuggested route: `%s`\nCategory: `%s`\n",
			i+1,
			changeRequestTitle(item),
			item.ID,
			item.Source,
			changeRequestSuggestedRoute(&item),
			changeRequestCategory(&item),
		)
		if !item.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, "Waiting since: `%s UTC`", item.UpdatedAt.UTC().Format("2006-01-02 15:04"))
			if now.After(item.UpdatedAt) {
				fmt.Fprintf(&b, " (%s)", now.Sub(item.UpdatedAt).Round(time.Minute))
			}
			b.WriteByte('\n')
		}
		if erp := strings.TrimSpace(item.Metadata["erp"]); erp != "" {
			fmt.Fprintf(&b, "ERP: `%s`\n", erp)
		}
		if deploymentURL := strings.TrimSpace(item.Metadata["deployment_url"]); deploymentURL != "" {
			fmt.Fprintf(&b, "Deployment: %s\n", deploymentURL)
		}
		b.WriteString("Decision needed: approve to experiment, approve to backlog, request revision, or drop.\n")
	}
	if len(items) < total {
		fmt.Fprintf(&b, "\nShowing %d of %d review items.", len(items), total)
	}
	return b.String()
}

func appendRepoWorkflowReferenceLink(b *strings.Builder, targetRepo string) {
	if link := repoGitHubFileURL(targetRepo, repoWorkflowReferenceRelPath); link != "" {
		fmt.Fprintf(b, "Workflow queue reference: [%s](%s)\n", repoWorkflowReferenceRelPath, link)
		return
	}
	fmt.Fprintf(b, "Workflow queue reference: `%s`\n", repoWorkflowReferenceRelPath)
}

func changeRequestTitle(item workflowItem) string {
	for _, line := range strings.Split(item.Body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 120 {
			return line[:120] + "..."
		}
		return line
	}
	return item.ID
}

func changeRequestSuggestedRoute(item *workflowItem) string {
	if item == nil {
		return ""
	}
	route := normalizeChangeRequestRoute(item.Metadata["suggested_route"])
	if route == "" {
		route = inferChangeRequestRoute(item.Body, item.Metadata["category"])
	}
	return route
}

func changeRequestCategory(item *workflowItem) string {
	if item == nil {
		return ""
	}
	category := normalizeChangeRequestCategory(item.Metadata["category"])
	if category == "" {
		category = defaultChangeRequestCategory(changeRequestSuggestedRoute(item))
	}
	return category
}

func changeRequestApprovalBody(item *workflowItem, targetKind, feedback string) string {
	targetLabel := "implementation backlog"
	nextStep := "Implement the approved functional change, run focused verification, and route completed work to QA."
	if targetKind == kindExperiment {
		targetLabel = "UI/UX experiment"
		nextStep = "Validate the approved UI/UX direction in the experiment surface before any production backlog implementation."
	}
	return fmt.Sprintf(`Approved change request for %s.

Change request ID: %s
Suggested route: %s
Category: %s

Request:
%s

Reviewer feedback:
%s

Next step:
%s
`, targetLabel, item.ID, changeRequestSuggestedRoute(item), changeRequestCategory(item), item.Body, feedback, nextStep)
}

func normalizeChangeRequestRoute(route string) string {
	route = strings.ToLower(strings.TrimSpace(route))
	route = strings.ReplaceAll(route, "-", "_")
	switch route {
	case "experiment", "experiments", "ui", "ux", "ui_ux", "design", "visual":
		return kindExperiment
	case "backlog", "functional", "function", "implementation", "impl":
		return kindBacklog
	default:
		return ""
	}
}

func normalizeChangeRequestCategory(category string) string {
	category = strings.ToLower(strings.TrimSpace(category))
	category = strings.ReplaceAll(category, "-", "_")
	switch category {
	case "ui", "ux", "ui_ux", "visual", "design", "interaction":
		return "ui_ux"
	case "functional", "function", "implementation", "system", "module", "backend":
		return "functional"
	default:
		return category
	}
}

func inferChangeRequestRoute(text, category string) string {
	category = normalizeChangeRequestCategory(category)
	if category == "ui_ux" {
		return kindExperiment
	}
	lower := strings.ToLower(text)
	for _, token := range []string{"ui/ux", "visual", "layout", "design", "interaction", "copy", "affordance", "responsive", "accessibility"} {
		if strings.Contains(lower, token) {
			return kindExperiment
		}
	}
	if containsLowerWord(lower, "ui") || containsLowerWord(lower, "ux") {
		return kindExperiment
	}
	return kindBacklog
}

func defaultChangeRequestCategory(route string) string {
	if normalizeChangeRequestRoute(route) == kindExperiment {
		return "ui_ux"
	}
	return "functional"
}

func containsLowerWord(text, word string) bool {
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if field == word {
			return true
		}
	}
	return false
}

func appendExperimentAccessLinks(b *strings.Builder, item workflowItem, targetRepo string) {
	readmePath := experimentReadmePath(item)
	if readmePath != "" {
		if link := repoGitHubFileURL(targetRepo, readmePath); link != "" {
			fmt.Fprintf(b, "README: [%s](%s)\n", readmePath, link)
		} else {
			fmt.Fprintf(b, "README: `%s`\n", readmePath)
		}
	}
	mockPath := experimentMockPath(item)
	if mockPath != "" {
		if link := repoGitHubFileURL(targetRepo, mockPath); link != "" {
			fmt.Fprintf(b, "Mock: [%s](%s)\n", mockPath, link)
		} else {
			fmt.Fprintf(b, "Mock: `%s`\n", mockPath)
		}
	}
	if slug := experimentSlug(item); slug != "" {
		fmt.Fprintf(b, "Route: `/experiments/%s`\n", slug)
	}
}

func experimentReviewTitle(item workflowItem) string {
	for _, line := range strings.Split(item.Body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if idx := strings.Index(line, "]"); idx > 0 {
				title := strings.TrimSpace(line[idx+1:])
				if title != "" {
					return title
				}
			}
		}
		if len(line) > 120 {
			return line[:120] + "..."
		}
		return line
	}
	return item.ID
}

func experimentReadmePath(item workflowItem) string {
	if path := cleanRepoRelPath(item.Metadata["experiment_readme"]); path != "" {
		return path
	}
	for _, line := range strings.Split(item.Body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			if idx := strings.Index(line, "]"); idx > 0 {
				if path := cleanRepoRelPath(line[1:idx]); path != "" {
					return path
				}
			}
		}
		if path := strings.TrimSpace(strings.TrimPrefix(line, "Path:")); path != line {
			if dir := cleanRepoRelPath(path); dir != "" {
				return strings.TrimSuffix(dir, "/") + "/README.md"
			}
		}
	}
	if dir := experimentDirPath(item); dir != "" {
		return strings.TrimSuffix(dir, "/") + "/README.md"
	}
	return ""
}

func experimentMockPath(item workflowItem) string {
	if dir := experimentDirPath(item); dir != "" {
		return strings.TrimSuffix(dir, "/") + "/Mock.tsx"
	}
	return ""
}

func experimentDirPath(item workflowItem) string {
	if path := cleanRepoRelPath(item.Metadata["experiment_path"]); path != "" {
		return path
	}
	readme := cleanRepoRelPath(item.Metadata["experiment_readme"])
	if readme == "" {
		readme = experimentReadmePathFromBody(item.Body)
	}
	if readme != "" {
		return strings.TrimSuffix(filepath.ToSlash(filepath.Dir(readme)), ".")
	}
	for _, line := range strings.Split(item.Body, "\n") {
		line = strings.TrimSpace(line)
		if path := strings.TrimSpace(strings.TrimPrefix(line, "Path:")); path != line {
			return cleanRepoRelPath(path)
		}
	}
	return ""
}

func experimentReadmePathFromBody(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			if idx := strings.Index(line, "]"); idx > 0 {
				return cleanRepoRelPath(line[1:idx])
			}
		}
	}
	return ""
}

func experimentSlug(item workflowItem) string {
	if slug := strings.TrimSpace(item.Metadata["experiment_slug"]); slug != "" {
		return slug
	}
	if dir := experimentDirPath(item); dir != "" {
		return filepath.Base(dir)
	}
	return ""
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
	case kindChangeReq:
		return agentKeyERPUXWalker
	default:
		return agentKeySkynet
	}
}

func instructionsForKind(kind string) string {
	switch kind {
	case kindBacklog:
		return "Validate the backlog item. Review related_items and, only if they are tightly related, claim them with skynet_backlog action \"claim_related\" before editing. If the work is a broad rollup, milestone, stale, or missing acceptance criteria, use action \"refine\" with child backlog bullets. If actionable, implement it in the target repository, run focused verification, write or update the QA report, then complete the item or batch with item_id/item_ids. Complete queues QA automatically."
	case kindExperiment:
		return "Read the linked experiment README/Mock/registry entry, build or validate the sandbox experiment, append findings as needed, then request channel review with skynet_experiments. If user feedback explicitly accepts the experiment, use approve_to_backlog."
	case kindQA:
		return "Verify the QA item. Pass it if behavior is correct; this queues skynet_pr work automatically. Otherwise use fail_to_backlog with exact failure evidence."
	case kindPR:
		return "Compose a comprehensive PR from the QA-passed work. Inspect git status/commits, cherry-pick or stage only coherent scoped changes, verify, then complete with branch, commits, files, verification, and PR URL/body or fail with blockers."
	case kindChangeReq:
		return "Do not implement a change request directly. Keep it in human review until a human approves it to experiment or backlog, requests revision, or drops it."
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

func buildPRConflictMessage(targetRepo string, args map[string]any) string {
	return fmt.Sprintf(`[Skynet PR Merge Conflict]

Target repository: %s
Repository: %s
Pull request: %s
Title: %s
URL: %s
Base branch: %s
Base SHA: %s
PR branch: %s
Head commit: %s
Author: %s
Merge state: %s
Checks: %s

This PR has a merge conflict. Resolve it even if CI/Lighthouse checks are already green.

Conflict-resolution requirements:
- Fetch the latest base branch and PR branch.
- Do not resolve inside a dirty target checkout. Create and use a per-PR clean git worktree next to the target repository, for example ../ResearchCrafters-conflict-pr-12.
- Check out the PR branch in that clean worktree without disturbing unrelated local work.
- Merge or rebase the base branch into the PR branch using the repository's normal practice.
- Resolve only conflict files and direct fallout from the merge.
- Prefer the base branch for shared CI/CD workflow/process harness changes unless the PR intentionally changes them.
- Preserve the feature behavior and QA evidence from the PR branch.
- Run focused verification for conflict areas and a lightweight health check when practical.
- Commit and push the resolved PR branch if auth/remotes allow it.
- Report conflict files, resolution choices, verification, branch, pushed commit, and worktree path. If safe resolution or push is blocked, add a backlog item with exact blocker evidence.
`, targetRepo, stringArg(args, "repository"), stringArg(args, "pull_request"), stringArg(args, "title"), stringArg(args, "url"), stringArg(args, "base_branch"), stringArg(args, "base_sha"), stringArg(args, "branch"), stringArg(args, "commit"), stringArg(args, "author"), stringArg(args, "merge_state"), stringArg(args, "checks"))
}

func ciFailureDispatchLogMessage(args map[string]any) string {
	lines := []string{
		"[SKYNET CI FIXER DISPATCHED]",
		"Agent: " + agentKeyCIFixer,
	}
	for _, entry := range []struct {
		label string
		keys  []string
	}{
		{label: "Pull request", keys: []string{"pull_request"}},
		{label: "Branch", keys: []string{"branch"}},
		{label: "Commit", keys: []string{"commit"}},
		{label: "Run", keys: []string{"run_url"}},
	} {
		if value := stringArg(args, entry.keys...); value != "" {
			lines = append(lines, entry.label+": "+value)
		}
	}
	lines = append(lines, "Status: accepted by GoClaw; agent is starting.")
	return strings.Join(lines, "\n")
}

func prConflictDispatchLogMessage(args map[string]any) string {
	lines := []string{
		"[SKYNET PR CONFLICT RESOLVER DISPATCHED]",
		"Agent: " + agentKeyPRConflictResolver,
	}
	for _, entry := range []struct {
		label string
		keys  []string
	}{
		{label: "Pull request", keys: []string{"pull_request"}},
		{label: "Title", keys: []string{"title"}},
		{label: "Base", keys: []string{"base_branch"}},
		{label: "Branch", keys: []string{"branch"}},
		{label: "Head", keys: []string{"commit"}},
		{label: "Checks", keys: []string{"checks"}},
		{label: "URL", keys: []string{"url"}},
	} {
		if value := stringArg(args, entry.keys...); value != "" {
			lines = append(lines, entry.label+": "+value)
		}
	}
	lines = append(lines, "Status: accepted by GoClaw; conflict resolver is starting.")
	return strings.Join(lines, "\n")
}

func prConflictFingerprint(args map[string]any) string {
	parts := []string{
		stringArg(args, "repository"),
		normalizePullRequest(stringArg(args, "pull_request")),
		stringArg(args, "base_branch"),
		stringArg(args, "base_sha"),
		stringArg(args, "branch"),
		stringArg(args, "commit"),
		strings.ToUpper(stringArg(args, "merge_state")),
	}
	return strings.Join(parts, "|")
}

func prConflictConfigKey(args map[string]any) string {
	repo := configKeyPart(stringArg(args, "repository"))
	pr := configKeyPart(normalizePullRequest(stringArg(args, "pull_request")))
	if repo == "" && pr == "" {
		return ""
	}
	if repo == "" {
		repo = "unknown-repo"
	}
	if pr == "" {
		pr = "unknown-pr"
	}
	return "beta.skynet_workflows.pr_conflict." + repo + "." + pr
}

func normalizePullRequest(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "#")
	if idx := strings.LastIndex(value, "/pull/"); idx >= 0 {
		value = value[idx+len("/pull/"):]
	}
	return strings.TrimSpace(value)
}

func configKeyPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastSep := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if ok {
			b.WriteRune(r)
			lastSep = false
			continue
		}
		if !lastSep {
			b.WriteByte('_')
			lastSep = true
		}
	}
	return strings.Trim(b.String(), "_")
}
