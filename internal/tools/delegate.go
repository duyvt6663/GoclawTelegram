package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/hooks"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tracing"
)

const defaultMaxDelegationLoad = 5
const defaultProgressDelay = 90 * time.Second

// DelegationTask tracks an active delegation for concurrency control and cancellation.
type DelegationTask struct {
	ID             string     `json:"id"`
	SourceAgentID  uuid.UUID  `json:"source_agent_id"`
	SourceAgentKey string     `json:"source_agent_key"`
	TargetAgentID  uuid.UUID  `json:"target_agent_id"`
	TargetAgentKey     string `json:"target_agent_key"`
	TargetDisplayName  string `json:"-"`
	UserID         string     `json:"user_id"`
	Task           string     `json:"task"`
	Status         string     `json:"status"` // "running", "completed", "failed", "cancelled"
	Mode           string     `json:"mode"`   // "sync" or "async"
	SessionKey     string     `json:"session_key"`
	CreatedAt      time.Time  `json:"created_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`

	// Origin metadata for async announce routing
	OriginChannel  string `json:"-"`
	OriginChatID   string `json:"-"`
	OriginPeerKind string `json:"-"`
	OriginLocalKey string `json:"-"` // composite key with topic/thread suffix for routing

	// Trace context for announce linking (same pattern as SubagentTask)
	OriginTraceID    uuid.UUID `json:"-"`
	OriginRootSpanID uuid.UUID `json:"-"`

	// Team tracking
	TeamID     uuid.UUID `json:"-"` // from link.TeamID (for delegation history)
	TeamTaskID uuid.UUID `json:"-"`

	cancelFunc context.CancelFunc `json:"-"`
}

// DelegateOpts configures a single delegation call.
type DelegateOpts struct {
	TargetAgentKey    string
	Task              string
	Context           string        // optional extra context
	Mode              string        // "sync" (default) or "async"
	TeamTaskID        uuid.UUID     // optional: auto-complete this team task on success
	EstimatedDuration time.Duration // optional: progress notification fires after this delay (default 90s)
	Label             string        // optional: short label for auto-created task subject (falls back to Task)
}

// DelegateRunRequest is the request passed to the AgentRunFunc callback.
// Mirrors agent.RunRequest without importing the agent package (avoids import cycle).
type DelegateRunRequest struct {
	SessionKey        string
	Message           string
	UserID            string
	Channel           string
	ChatID            string
	PeerKind          string
	RunID             string
	Stream            bool
	ExtraSystemPrompt string
	MaxIterations     int // per-delegation override (0 = use agent default)
}

// DelegateRunResult is the result from AgentRunFunc.
type DelegateRunResult struct {
	Content      string
	Iterations   int
	MediaPaths   []string // media file paths from tool results (e.g. generated images)
	Deliverables []string // actual content from tool outputs (e.g. written file text, image prompt)
}

// DelegateArtifacts holds forwarded artifacts from delegation results.
// Used to accumulate artifacts from intermediate completions until the final
// announce fires. New artifact types (files, voice, etc.) should be added here.
type DelegateArtifacts struct {
	Media            []string                // file paths to forward (images, documents, audio, etc.)
	Results          []DelegateResultSummary // result summaries from completed delegations
	CompletedTaskIDs []string                // team task IDs auto-completed by delegations (for announce context)
}

// DelegateResultSummary is a compact representation of a delegation result
// included in the final announce so the lead has all results in one message.
type DelegateResultSummary struct {
	AgentKey     string
	Content      string
	HasMedia     bool
	Deliverables []string // actual content from tool outputs
}

// AgentRunFunc runs an agent by key with the given request.
// This callback is injected from the cmd layer to avoid tools→agent import cycle.
type AgentRunFunc func(ctx context.Context, agentKey string, req DelegateRunRequest) (*DelegateRunResult, error)

// DelegateResult is the outcome of a delegation.
type DelegateResult struct {
	Content      string
	Iterations   int
	DelegationID string   // for async: the delegation ID to track/cancel
	TeamTaskID   string   // auto-created or provided team task ID (for tracing)
	MediaPaths   []string // media file paths from delegation result
}

// linkSettings holds per-user restriction rules from agent_links.settings JSONB.
// NOTE: This is NOT the same as other_config.description (summoning prompt).
type linkSettings struct {
	RequireRole string   `json:"require_role"`
	UserAllow   []string `json:"user_allow"`
	UserDeny    []string `json:"user_deny"`
}

// DelegateManager manages inter-agent delegation lifecycle.
// Similar to SubagentManager but delegates to fully-configured named agents.
type DelegateManager struct {
	runAgent     AgentRunFunc
	linkStore    store.AgentLinkStore
	agentStore   store.AgentStore
	teamStore    store.TeamStore     // optional: enables auto-complete of team tasks
	sessionStore store.SessionStore  // optional: enables session cleanup
	msgBus       *bus.MessageBus     // for event broadcast + async announce (PublishInbound)
	hookEngine   *hooks.Engine       // optional: quality gate evaluation

	active            sync.Map // delegationID → *DelegationTask
	pendingArtifacts  sync.Map // sourceAgentID string → *DelegateArtifacts
	progressSent      sync.Map // "sourceAgentID:chatID" → true (dedup grouped notifications)
	completedMu       sync.Mutex
	completedSessions []string // session keys pending cleanup
}

// NewDelegateManager creates a new delegation manager.
func NewDelegateManager(
	runAgent AgentRunFunc,
	linkStore store.AgentLinkStore,
	agentStore store.AgentStore,
	msgBus *bus.MessageBus,
) *DelegateManager {
	return &DelegateManager{
		runAgent:   runAgent,
		linkStore:  linkStore,
		agentStore: agentStore,
		msgBus:     msgBus,
	}
}

// SetTeamStore enables auto-completion of team tasks on delegation success.
func (dm *DelegateManager) SetTeamStore(ts store.TeamStore) {
	dm.teamStore = ts
}

// SetSessionStore enables session cleanup after team tasks complete.
func (dm *DelegateManager) SetSessionStore(ss store.SessionStore) {
	dm.sessionStore = ss
}

// SetHookEngine enables quality gate evaluation on delegation results.
func (dm *DelegateManager) SetHookEngine(engine *hooks.Engine) {
	dm.hookEngine = engine
}

// Delegate executes a synchronous delegation to another agent.
func (dm *DelegateManager) Delegate(ctx context.Context, opts DelegateOpts) (*DelegateResult, error) {
	task, _, err := dm.prepareDelegation(ctx, opts, "sync")
	if err != nil {
		return nil, err
	}

	dm.active.Store(task.ID, task)
	defer func() {
		now := time.Now()
		task.CompletedAt = &now
		dm.active.Delete(task.ID)
	}()

	dm.injectDependencyResults(ctx, &opts)
	message := buildDelegateMessage(opts)
	dm.emitEvent("delegation.started", task)
	slog.Info("delegation started", "id", task.ID, "target", opts.TargetAgentKey, "mode", "sync")

	// Propagate parent trace ID so the delegate trace links back.
	// Clear senderID — delegations are system-initiated, the delegate agent
	// should not inherit the caller's group writer permissions (the delegate
	// agent has its own writer list, and would incorrectly deny writes).
	delegateCtx := store.WithSenderID(ctx, "")
	if parentTraceID := tracing.TraceIDFromContext(ctx); parentTraceID != uuid.Nil {
		delegateCtx = tracing.WithDelegateParentTraceID(delegateCtx, parentTraceID)
	}

	startTime := time.Now()
	result, err := dm.runAgent(delegateCtx, opts.TargetAgentKey, dm.buildRunRequest(task, message))
	duration := time.Since(startTime)
	if err != nil {
		task.Status = "failed"
		dm.emitEvent("delegation.failed", task)
		dm.saveDelegationHistory(task, "", err, duration)
		return nil, fmt.Errorf("delegation to %q failed: %w", opts.TargetAgentKey, err)
	}

	// Apply quality gates before marking completed.
	if result, err = dm.applyQualityGates(delegateCtx, task, opts, result); err != nil {
		task.Status = "failed"
		dm.emitEvent("delegation.failed", task)
		dm.saveDelegationHistory(task, "", err, duration)
		return nil, fmt.Errorf("delegation to %q failed quality gate: %w", opts.TargetAgentKey, err)
	}

	task.Status = "completed"
	dm.emitEvent("delegation.completed", task)
	dm.trackCompleted(task)
	dm.autoCompleteTeamTask(task, result.Content, result.Deliverables)
	dm.saveDelegationHistory(task, result.Content, nil, duration)
	slog.Info("delegation completed", "id", task.ID, "target", opts.TargetAgentKey, "iterations", result.Iterations)

	return &DelegateResult{Content: result.Content, Iterations: result.Iterations, DelegationID: task.ID, TeamTaskID: task.TeamTaskID.String(), MediaPaths: result.MediaPaths}, nil
}

// DelegateAsync spawns a delegation in the background and announces the result back.
func (dm *DelegateManager) DelegateAsync(ctx context.Context, opts DelegateOpts) (*DelegateResult, error) {
	task, _, err := dm.prepareDelegation(ctx, opts, "async")
	if err != nil {
		return nil, err
	}

	taskCtx, taskCancel := context.WithCancel(context.Background())
	task.cancelFunc = taskCancel
	dm.active.Store(task.ID, task)

	// Capture parent trace ID before goroutine (ctx.Background() loses it)
	parentTraceID := tracing.TraceIDFromContext(ctx)
	if parentTraceID != uuid.Nil {
		taskCtx = tracing.WithDelegateParentTraceID(taskCtx, parentTraceID)
	}

	dm.injectDependencyResults(ctx, &opts)
	message := buildDelegateMessage(opts)
	dm.emitEvent("delegation.started", task)
	slog.Info("delegation started (async)", "id", task.ID, "target", opts.TargetAgentKey)

	runReq := dm.buildRunRequest(task, message)

	go func() {
		defer func() {
			now := time.Now()
			task.CompletedAt = &now
			dm.active.Delete(task.ID)
		}()

		// Progress notification timer — fires once after delay if still running
		progressDelay := opts.EstimatedDuration
		if progressDelay <= 0 {
			progressDelay = defaultProgressDelay
		}
		progressTimer := time.AfterFunc(progressDelay, func() {
			dm.sendProgressNotification(task)
		})
		defer progressTimer.Stop()

		startTime := time.Now()
		result, runErr := dm.runAgent(taskCtx, opts.TargetAgentKey, runReq)
		duration := time.Since(startTime)

		// Count sibling delegations still running (exclude self)
		siblings := dm.ListActive(task.SourceAgentID)
		siblingCount := 0
		for _, s := range siblings {
			if s.ID != task.ID {
				siblingCount++
			}
		}
		// Announce result to parent via message bus
		if dm.msgBus != nil && task.OriginChannel != "" {
			elapsed := time.Since(task.CreatedAt)

			if siblingCount > 0 {
				// Intermediate completion: accumulate artifacts + result summary.
				// The final announce includes all sibling results so the lead doesn't
				// need to call team_tasks to aggregate.
				arts := &DelegateArtifacts{}
				if result != nil {
					arts.Media = result.MediaPaths
					arts.Results = []DelegateResultSummary{{
						AgentKey:     task.TargetAgentKey,
						Content:      result.Content,
						HasMedia:     len(result.MediaPaths) > 0,
						Deliverables: result.Deliverables,
					}}
				} else if runErr != nil {
					arts.Results = []DelegateResultSummary{{
						AgentKey: task.TargetAgentKey,
						Content:  fmt.Sprintf("[failed] %s", runErr.Error()),
					}}
				}
				if task.TeamTaskID != uuid.Nil {
					arts.CompletedTaskIDs = []string{task.TeamTaskID.String()}
				}
				dm.accumulateArtifacts(task.SourceAgentID, arts)
				slog.Info("delegation announce suppressed (siblings still running)",
					"id", task.ID, "target", task.TargetAgentKey, "siblings", siblingCount)
			} else {
				// Last completion: clear progress dedup so next batch gets fresh notifications.
				dm.progressSent.Delete(task.SourceAgentID.String() + ":" + task.OriginChatID)

				// Last completion: collect all accumulated artifacts + own result
				artifacts := dm.collectArtifacts(task.SourceAgentID)
				if result != nil {
					artifacts.Media = append(artifacts.Media, result.MediaPaths...)
					artifacts.Results = append(artifacts.Results, DelegateResultSummary{
						AgentKey:     task.TargetAgentKey,
						Content:      result.Content,
						HasMedia:     len(result.MediaPaths) > 0,
						Deliverables: result.Deliverables,
					})
				}
				if task.TeamTaskID != uuid.Nil {
					artifacts.CompletedTaskIDs = append(artifacts.CompletedTaskIDs, task.TeamTaskID.String())
				}

				announceMeta := map[string]string{
					"origin_channel":      task.OriginChannel,
					"origin_peer_kind":    task.OriginPeerKind,
					"parent_agent":        task.SourceAgentKey,
					"delegation_id":       task.ID,
					"target_agent":        task.TargetAgentKey,
					"origin_trace_id":     task.OriginTraceID.String(),
					"origin_root_span_id": task.OriginRootSpanID.String(),
				}
				if task.OriginLocalKey != "" {
					announceMeta["origin_local_key"] = task.OriginLocalKey
				}
				announceMsg := bus.InboundMessage{
					Channel:  "system",
					SenderID: fmt.Sprintf("delegate:%s", task.ID),
					ChatID:   task.OriginChatID,
					Content:  formatDelegateAnnounce(task, artifacts, runErr, elapsed),
					UserID:   task.UserID,
					Metadata: announceMeta,
					Media:    artifacts.Media,
				}
				dm.msgBus.PublishInbound(announceMsg)
			}
		}

		if runErr != nil {
			task.Status = "failed"
			dm.emitEvent("delegation.failed", task)
			dm.saveDelegationHistory(task, "", runErr, duration)
		} else {
			// Apply quality gates before marking completed.
			if result, runErr = dm.applyQualityGates(taskCtx, task, opts, result); runErr != nil {
				task.Status = "failed"
				dm.emitEvent("delegation.failed", task)
				dm.saveDelegationHistory(task, "", runErr, duration)
			} else {
				task.Status = "completed"
				dm.emitEvent("delegation.completed", task)
				dm.trackCompleted(task)
				resultContent := ""
				var deliverables []string
				if result != nil {
					resultContent = result.Content
					deliverables = result.Deliverables
				}
				// Auto-complete the team task for EVERY delegation (not just the last one).
				// Each delegation has its own TeamTaskID — the isLastDelegation guard
				// is for announce batching only, not for task completion.
				dm.autoCompleteTeamTask(task, resultContent, deliverables)
				dm.saveDelegationHistory(task, resultContent, nil, duration)
			}
		}
		slog.Info("delegation finished (async)", "id", task.ID, "target", task.TargetAgentKey, "status", task.Status)
	}()

	return &DelegateResult{DelegationID: task.ID, TeamTaskID: task.TeamTaskID.String()}, nil
}

// --- internal helpers ---

func (dm *DelegateManager) prepareDelegation(ctx context.Context, opts DelegateOpts, mode string) (*DelegationTask, *store.AgentLinkData, error) {
	sourceAgentID := store.AgentIDFromContext(ctx)
	if sourceAgentID == uuid.Nil {
		return nil, nil, fmt.Errorf("delegation requires managed mode (no agent ID in context)")
	}

	sourceAgent, err := dm.agentStore.GetByID(ctx, sourceAgentID)
	if err != nil {
		return nil, nil, fmt.Errorf("source agent not found: %w", err)
	}

	targetAgent, err := dm.agentStore.GetByKey(ctx, opts.TargetAgentKey)
	if err != nil {
		return nil, nil, fmt.Errorf("target agent %q not found", opts.TargetAgentKey)
	}

	link, err := dm.linkStore.GetLinkBetween(ctx, sourceAgentID, targetAgent.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to check delegation permission: %w", err)
	}
	if link == nil {
		return nil, nil, fmt.Errorf("no delegation link from this agent to %q. Available targets are listed in AGENTS.md", opts.TargetAgentKey)
	}

	userID := store.UserIDFromContext(ctx)
	if err := checkUserPermission(link.Settings, userID); err != nil {
		return nil, nil, err
	}

	// Resolve team once — used for task enforcement, validation, and access checks.
	var team *store.TeamData
	if dm.teamStore != nil {
		team, _ = dm.teamStore.GetTeamForAgent(ctx, sourceAgentID)
	}

	// Auto-create team task when team_task_id is omitted.
	// This eliminates the two-step create→spawn dance that caused LLM hallucination
	// (LLM would call create+spawn in parallel, hallucinating the task_id).
	if team != nil && opts.TeamTaskID == uuid.Nil {
		subject := opts.Label
		if subject == "" {
			subject = opts.Task
			if len(subject) > 100 {
				subject = subject[:100] + "..."
			}
		}
		taskData := &store.TeamTaskData{
			TeamID:      team.ID,
			Subject:     subject,
			Description: opts.Task,
			Status:      store.TeamTaskStatusPending,
			UserID:      store.UserIDFromContext(ctx),
			Channel:     ToolChannelFromCtx(ctx),
		}
		if err := dm.teamStore.CreateTask(ctx, taskData); err != nil {
			return nil, nil, fmt.Errorf("failed to auto-create team task: %w", err)
		}
		opts.TeamTaskID = taskData.ID
		slog.Info("delegate: auto-created team task",
			"task_id", taskData.ID, "subject", subject, "target", opts.TargetAgentKey)
	}

	// Validate that team_task_id belongs to the agent's team (prevent cross-team task completion).
	if dm.teamStore != nil && opts.TeamTaskID != uuid.Nil {
		teamTask, err := dm.teamStore.GetTask(ctx, opts.TeamTaskID)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"team_task_id %s not found. Use team_tasks action=list to see available tasks, or omit team_task_id to auto-create.",
				opts.TeamTaskID)
		}

		// Guard: scope task to current user_id (prevent cross-group task leak).
		// user_id is the GROUP composite ID (e.g. "group:telegram:-1003701523276"), NOT the sender.
		// Delegate/system channels skip this check — they operate cross-context by design.
		currentUserID := store.UserIDFromContext(ctx)
		channel := ToolChannelFromCtx(ctx)
		if channel != "delegate" && channel != "system" &&
			teamTask.UserID != "" && currentUserID != "" && teamTask.UserID != currentUserID {
			return nil, nil, fmt.Errorf(
				"team_task_id %s belongs to a different context. Omit team_task_id to auto-create a new task.",
				opts.TeamTaskID)
		}

		// Guard: reject completed/cancelled tasks — enforce "one task per delegation".
		if teamTask.Status == store.TeamTaskStatusCompleted || teamTask.Status == "cancelled" {
			ownerLabel := "another agent"
			if teamTask.OwnerAgentKey != "" {
				ownerLabel = teamTask.OwnerAgentKey
			}
			return nil, nil, fmt.Errorf(
				"team_task_id %s is already %s (completed by %q). Omit team_task_id to auto-create a new task.",
				opts.TeamTaskID, teamTask.Status, ownerLabel)
		}

		if team != nil {
			if teamTask.TeamID != team.ID {
				return nil, nil, fmt.Errorf("team_task_id does not belong to your team")
			}
			userID := store.UserIDFromContext(ctx)
			ch := ToolChannelFromCtx(ctx)
			if err := checkTeamAccess(team.Settings, userID, ch); err != nil {
				return nil, nil, fmt.Errorf("team access denied: %w", err)
			}
		}

		// Auto-populate task description from spawn prompt if empty.
		// This ensures the task board has full context for audit/visibility
		// without relying on the LLM to set description at task creation time.
		if teamTask.Description == "" && opts.Task != "" {
			_ = dm.teamStore.UpdateTask(ctx, opts.TeamTaskID, map[string]any{
				"description": opts.Task,
			})
		}

		// Claim task early so status moves to in_progress immediately.
		// This prevents the pending reminder from re-triggering spawns for
		// tasks that are already running. The ClaimTask in autoCompleteTeamTask()
		// will harmlessly fail (WHERE status='pending' won't match).
		_ = dm.teamStore.ClaimTask(ctx, opts.TeamTaskID, targetAgent.ID, teamTask.TeamID)
	}

	linkCount := dm.ActiveCountForLink(sourceAgentID, targetAgent.ID)
	if link.MaxConcurrent > 0 && linkCount >= link.MaxConcurrent {
		return nil, nil, fmt.Errorf("delegation link to %q is at capacity (%d/%d active). Try again later or handle the task yourself",
			opts.TargetAgentKey, linkCount, link.MaxConcurrent)
	}

	targetCount := dm.ActiveCountForTarget(targetAgent.ID)
	maxLoad := parseMaxDelegationLoad(targetAgent.OtherConfig)
	if targetCount >= maxLoad {
		return nil, nil, fmt.Errorf("agent %q is at capacity (%d/%d active delegations). Either wait and retry, use a different agent, or handle the task yourself",
			opts.TargetAgentKey, targetCount, maxLoad)
	}

	channel := ToolChannelFromCtx(ctx)
	chatID := ToolChatIDFromCtx(ctx)
	peerKind := ToolPeerKindFromCtx(ctx)
	localKey := ToolLocalKeyFromCtx(ctx)

	delegationID := uuid.NewString()[:12]
	task := &DelegationTask{
		ID:             delegationID,
		SourceAgentID:  sourceAgentID,
		SourceAgentKey: sourceAgent.AgentKey,
		TargetAgentID:  targetAgent.ID,
		TargetAgentKey:    opts.TargetAgentKey,
		TargetDisplayName: targetAgent.DisplayName,
		UserID:         userID,
		Task:           opts.Task,
		Status:         "running",
		Mode:           mode,
		SessionKey: fmt.Sprintf("delegate:%s:%s:%s",
			sourceAgentID.String()[:8], opts.TargetAgentKey, delegationID),
		CreatedAt:        time.Now(),
		OriginChannel:    channel,
		OriginChatID:     chatID,
		OriginPeerKind:   peerKind,
		OriginLocalKey:   localKey,
		OriginTraceID:    tracing.TraceIDFromContext(ctx),
		OriginRootSpanID: tracing.ParentSpanIDFromContext(ctx),
		TeamTaskID:       opts.TeamTaskID,
	}

	// Carry team_id from the link (for delegation history filtering by team)
	if link.TeamID != nil {
		task.TeamID = *link.TeamID
	}

	return task, link, nil
}

// injectDependencyResults fetches completed dependency results for a task's
// blocked_by prerequisites and prepends them to opts.Context. This ensures the
// delegate agent receives prior results without needing to search for them.
func (dm *DelegateManager) injectDependencyResults(ctx context.Context, opts *DelegateOpts) {
	if dm.teamStore == nil || opts.TeamTaskID == uuid.Nil {
		return
	}
	teamTask, err := dm.teamStore.GetTask(ctx, opts.TeamTaskID)
	if err != nil || len(teamTask.BlockedBy) == 0 {
		return
	}

	var depContext []string
	for _, depID := range teamTask.BlockedBy {
		dep, err := dm.teamStore.GetTask(ctx, depID)
		if err != nil || dep.Result == nil || *dep.Result == "" {
			continue
		}
		result := *dep.Result
		if len(result) > 8000 {
			result = result[:8000] + "\n[...truncated]"
		}
		agentLabel := dep.OwnerAgentKey
		if agentLabel == "" {
			agentLabel = "unknown"
		}
		depContext = append(depContext, fmt.Sprintf(
			"--- Result from dependency task %q (id=%s, by %s) ---\n%s",
			dep.Subject, dep.ID, agentLabel, result))
	}

	if len(depContext) > 0 {
		injected := strings.Join(depContext, "\n\n")
		if opts.Context != "" {
			opts.Context = injected + "\n\n" + opts.Context
		} else {
			opts.Context = injected
		}
	}
}

// sendProgressNotification sends a grouped "still working" message listing all
// active delegations from the same source agent. Uses progressSent to dedup —
// only the first timer that fires sends the notification; subsequent timers skip.
func (dm *DelegateManager) sendProgressNotification(task *DelegationTask) {
	// Skip internal/delegate channels — only notify on real user-facing channels.
	if dm.msgBus == nil || task.OriginChannel == "" || task.OriginChatID == "" ||
		task.OriginChannel == "delegate" || task.OriginChannel == "system" {
		return
	}

	// Dedup: one grouped notification per source agent per chat.
	dedupKey := task.SourceAgentID.String() + ":" + task.OriginChatID
	if _, loaded := dm.progressSent.LoadOrStore(dedupKey, true); loaded {
		return
	}

	// Collect all active delegations from same source agent.
	active := dm.ListActive(task.SourceAgentID)
	if len(active) == 0 {
		dm.progressSent.Delete(dedupKey)
		return
	}

	var lines []string
	for _, t := range active {
		elapsed := time.Since(t.CreatedAt).Round(time.Second)
		label := t.TargetAgentKey
		if t.TargetDisplayName != "" {
			label = fmt.Sprintf("%s (%s)", t.TargetDisplayName, t.TargetAgentKey)
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", label, elapsed))
	}

	content := fmt.Sprintf("⏳ Your team is working on it...\n%s", strings.Join(lines, "\n"))

	dm.msgBus.PublishOutbound(bus.OutboundMessage{
		Channel: task.OriginChannel,
		ChatID:  task.OriginChatID,
		Content: content,
		Metadata: map[string]string{
			"local_key": task.OriginLocalKey,
			"peer_kind": task.OriginPeerKind,
		},
	})
}

func buildDelegateMessage(opts DelegateOpts) string {
	if opts.Context != "" {
		return fmt.Sprintf("[Additional Context]\n%s\n\n[Task]\n%s", opts.Context, opts.Task)
	}
	return opts.Task
}


func (dm *DelegateManager) buildRunRequest(task *DelegationTask, message string) DelegateRunRequest {
	return DelegateRunRequest{
		SessionKey: task.SessionKey,
		Message:    message,
		UserID:     task.UserID,
		Channel:    "delegate",
		ChatID:     task.OriginChatID,
		PeerKind:   task.OriginPeerKind,
		RunID:      fmt.Sprintf("delegate-%s", task.ID),
		Stream:     false,
		ExtraSystemPrompt: "[Delegation Context]\nYou are handling a delegated task from another agent.\n" +
			"- Focus exclusively on the delegated task below.\n" +
			"- Your complete response will be returned to the requesting agent.\n" +
			"- Do NOT try to communicate with the end user directly.\n" +
			"- Do NOT use your persona name or self-references (e.g. do not say your name). Write factual, neutral content.\n" +
			"- Be concise and deliver actionable results.\n" +
			"- IMPORTANT: If the delegated task falls outside your expertise scope (as defined in your SOUL.md), politely refuse and explain that this task is not within your domain. Do NOT attempt tasks outside your scope.",
	}
}
