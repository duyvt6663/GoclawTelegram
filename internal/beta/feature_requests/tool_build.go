package featurerequests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	buildTimeout              = 2 * time.Hour
	buildMaxAttempts          = 3
	buildFailureExcerptSize   = 4000
	buildArtifactsMarker      = "BUILD_ARTIFACTS:"
	BuildFollowupSenderPrefix = "notification:feature_build:"
)

type codexRunner func(ctx context.Context, workspace, prompt string) (string, error)
type buildVerifier func(ctx context.Context, workspace, output string, buildStartedAt time.Time, requireFreshArtifacts bool) (string, error)

type buildArtifactsManifest struct {
	FeatureRoot string   `json:"feature_root"`
	Files       []string `json:"files"`
}

type buildFollowupContext struct {
	AgentKey   string
	Channel    string
	ChatID     string
	PeerKind   string
	LocalKey   string
	SessionKey string
	UserID     string
	TenantID   uuid.UUID
}

func BuildFollowupSenderID(featureID string) string {
	featureID = strings.TrimSpace(featureID)
	if featureID == "" {
		return BuildFollowupSenderPrefix
	}
	return BuildFollowupSenderPrefix + featureID
}

func IsBuildFollowupSender(senderID string) bool {
	return strings.HasPrefix(strings.TrimSpace(senderID), BuildFollowupSenderPrefix)
}

// buildFeatureTool runs a codex agent to plan and execute a beta feature.
type buildFeatureTool struct {
	feature   *FeatureRequestsFeature
	workspace string
	runner    codexRunner
	verifier  buildVerifier
	maxTries  int
}

func (t *buildFeatureTool) Name() string { return "build_feature" }
func (t *buildFeatureTool) Description() string {
	return "Run a Codex agent to plan and build an approved beta feature. " +
		"The feature must be in 'approved' status (either passed the 5-vote approval poll or was directly approved by @duyvt6663). Failed builds can be retried. " +
		"Launches a background Codex CLI process that plans the architecture and implements the feature code. " +
		"Use after a feature has been approved."
}

func (t *buildFeatureTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"feature_id": map[string]any{
				"type":        "string",
				"description": "The ID of the approved feature request to build",
			},
		},
		"required": []string{"feature_id"},
	}
}

func (t *buildFeatureTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	featureID := strings.TrimSpace(tools.GetParamString(args, "feature_id", ""))
	if featureID == "" {
		return tools.ErrorResult("feature_id is required")
	}

	req, err := t.feature.store.getByID(tenantKeyFromCtx(ctx), featureID)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("Feature not found: %v", err))
	}

	if !canBuildFeatureStatus(req.Status) {
		return tools.ErrorResult(fmt.Sprintf("Feature '%s' is %s. Only approved or failed features can be built.", req.Title, req.Status))
	}

	retrying := req.Status == StatusFailed
	workspace := resolveBuildWorkspace(t.workspace)
	if workspace == "" {
		return tools.ErrorResult("Build workspace is not a GoClaw source checkout. Start the gateway from the repo root or set GOCLAW_FEATURE_BUILD_WORKSPACE.")
	}
	buildStartedAt := time.Now()
	followup := captureBuildFollowupContext(ctx, req)

	// Mark as building.
	req.Status = StatusBuilding
	req.BuildLog = appendBuildAttemptLog(req.BuildLog, "Build started at "+buildStartedAt.Format(time.RFC3339))
	if err := t.feature.store.update(req); err != nil {
		return tools.ErrorResult(fmt.Sprintf("Failed to update status: %v", err))
	}

	t.announceBuild(req, buildStartAnnouncement(req.Title, retrying))

	// Launch codex in background.
	go t.runCodex(req, retrying, workspace, buildStartedAt, followup)

	result := map[string]any{
		"feature_id": req.ID,
		"title":      req.Title,
		"status":     StatusBuilding,
		"retrying":   retrying,
		"message":    buildResultMessage(req.Title, retrying),
	}
	out, _ := json.Marshal(result)
	return tools.NewResult(string(out))
}

func (t *buildFeatureTool) runCodex(req *FeatureRequest, retrying bool, workspace string, buildStartedAt time.Time, followup *buildFollowupContext) {
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()

	prompt := buildCodexPrompt(req)
	attempts := t.buildAttemptLimit()

	var (
		output string
		err    error
	)

	for attempt := 1; attempt <= attempts; attempt++ {
		attemptStartedAt := time.Now()
		output, err = t.runCodexPrompt(ctx, workspace, prompt)
		err = describeBuildProcessError(ctx, "codex", err)
		if err == nil {
			verifyOutput, verifyErr := t.runBuildVerification(ctx, workspace, output, buildStartedAt, !retrying)
			output = appendVerificationOutput(output, verifyOutput)
			err = verifyErr
		}
		output = truncateBuildOutput(output)
		req.BuildLog += output

		if err == nil {
			req.Status = StatusCompleted
			req.BuildLog += "\n\nBuild completed successfully."
			req.BuildLog += fmt.Sprintf("\nBuild duration: %s", time.Since(buildStartedAt).Round(time.Second))
			slog.Info("beta feature_requests: codex completed",
				"feature_id", req.ID, "title", req.Title, "attempt", attempt, "attempts", attempts)
			break
		}

		req.BuildLog += fmt.Sprintf("\n\nBuild failed: %v", err)
		req.BuildLog += fmt.Sprintf("\nAttempt duration: %s", time.Since(attemptStartedAt).Round(time.Second))
		summary := summarizeBuildFailure(output, err)

		slog.Warn("beta feature_requests: codex failed",
			"feature_id", req.ID, "error", err, "attempt", attempt, "attempts", attempts)

		if attempt >= attempts || !shouldAutoRepairBuildFailure(output, err) {
			req.Status = StatusFailed
			break
		}

		nextAttempt := attempt + 1
		req.BuildLog = appendBuildAttemptLog(req.BuildLog,
			fmt.Sprintf("Automatic repair attempt %d/%d queued at %s. Previous failure summary: %s",
				nextAttempt, attempts, time.Now().Format(time.RFC3339), summary))
		if updateErr := t.feature.store.update(req); updateErr != nil {
			slog.Warn("beta feature_requests: failed to persist repair attempt state",
				"feature_id", req.ID, "attempt", nextAttempt, "error", updateErr)
		}
		t.announceBuild(req, buildRepairAnnouncement(req.Title, nextAttempt, attempts, summary))

		prompt = buildRepairPrompt(req, output, summary, nextAttempt, attempts)
	}

	if req.Status != StatusCompleted {
		req.Status = StatusFailed
	}

	if updateErr := t.feature.store.update(req); updateErr != nil {
		slog.Warn("beta feature_requests: failed to update after build",
			"feature_id", req.ID, "error", updateErr)
	}

	// Announce completion to the chat.
	if req.Channel != "" && req.ChatID != "" {
		var msg string
		if req.Status == StatusCompleted {
			msg = buildSuccessAnnouncement(req.Title, retrying)
		} else {
			msg = buildFailureAnnouncement(req.Title, retrying, summarizeBuildFailure(output, err))
		}
		t.announceBuild(req, msg)
	}

	t.enqueueBuildFollowup(req, followup, retrying, summarizeBuildFailure(output, err))
}

func (t *buildFeatureTool) announceBuild(req *FeatureRequest, content string) {
	if t == nil || t.feature == nil || t.feature.msgBus == nil || req == nil {
		return
	}
	if strings.TrimSpace(req.Channel) == "" || strings.TrimSpace(req.ChatID) == "" || strings.TrimSpace(content) == "" {
		return
	}
	t.feature.msgBus.PublishOutbound(bus.OutboundMessage{
		Channel:  req.Channel,
		ChatID:   req.ChatID,
		Content:  content,
		Metadata: outboundMeta(req),
	})
}

func (t *buildFeatureTool) buildAttemptLimit() int {
	if t != nil && t.maxTries > 0 {
		return t.maxTries
	}
	return buildMaxAttempts
}

func (t *buildFeatureTool) runBuildVerification(ctx context.Context, workspace, output string, buildStartedAt time.Time, requireFreshArtifacts bool) (string, error) {
	if t != nil && t.verifier != nil {
		return t.verifier(ctx, workspace, output, buildStartedAt, requireFreshArtifacts)
	}
	return verifyBuildOutput(ctx, workspace, output, buildStartedAt, requireFreshArtifacts)
}

func (t *buildFeatureTool) runCodexPrompt(ctx context.Context, workspace, prompt string) (string, error) {
	if t != nil && t.runner != nil {
		return t.runner(ctx, workspace, prompt)
	}

	cmd := exec.CommandContext(ctx, "codex", codexBuildArgs(prompt)...)
	if strings.TrimSpace(workspace) != "" {
		cmd.Dir = workspace
	}
	env, err := buildProcessEnv()
	if err != nil {
		return "", fmt.Errorf("prepare build env: %w", err)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.Info("beta feature_requests: codex started")

	err = cmd.Run()

	output := stdout.String()
	if errOut := stderr.String(); errOut != "" {
		if output == "" {
			output = "STDERR:\n" + errOut
		} else {
			output += "\n\nSTDERR:\n" + errOut
		}
	}
	return output, err
}

func codexBuildArgs(prompt string) []string {
	return []string{
		"exec",
		"--full-auto",
		"--skip-git-repo-check",
		prompt,
	}
}

func canBuildFeatureStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case StatusApproved, StatusFailed:
		return true
	default:
		return false
	}
}

func buildResultMessage(title string, retrying bool) string {
	if retrying {
		return fmt.Sprintf("Queued a background retry for '%s'. The chat will get an automatic update when it succeeds or fails. Use feature_detail to inspect logs.", title)
	}
	return fmt.Sprintf("Queued a background build for '%s'. The chat will get an automatic update when it succeeds or fails. Use feature_detail to inspect logs.", title)
}

func buildStartAnnouncement(title string, retrying bool) string {
	if retrying {
		return fmt.Sprintf("Retrying feature <b>%s</b> in the background after a previous failed build. I will post another update when the retry succeeds or fails.", htmlEscape(title))
	}
	return fmt.Sprintf("Feature <b>%s</b> build started in the background. I will post another update when it succeeds or fails.", htmlEscape(title))
}

func buildSuccessAnnouncement(title string, retrying bool) string {
	if retrying {
		return fmt.Sprintf("Feature <b>%s</b> retry completed successfully. Use <code>feature_detail</code> to see the build log.", htmlEscape(title))
	}
	return fmt.Sprintf("Feature <b>%s</b> has been built successfully. Use <code>feature_detail</code> to see the build log.", htmlEscape(title))
}

func buildRepairAnnouncement(title string, attempt, attempts int, summary string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Feature <b>%s</b> hit a build issue. Starting automatic repair attempt %d/%d.", htmlEscape(title), attempt, attempts)
	if summary != "" {
		fmt.Fprintf(&b, " Summary: <code>%s</code>.", htmlEscape(summary))
	}
	b.WriteString(" This repair pass is allowed to fix shared/common GoClaw code when that is what blocks the feature.")
	return b.String()
}

func buildFailureAnnouncement(title string, retrying bool, summary string) string {
	var b strings.Builder
	if retrying {
		fmt.Fprintf(&b, "Feature <b>%s</b> retry failed.", htmlEscape(title))
	} else {
		fmt.Fprintf(&b, "Feature <b>%s</b> build failed.", htmlEscape(title))
	}
	if summary != "" {
		fmt.Fprintf(&b, " Summary: <code>%s</code>.", htmlEscape(summary))
	}
	b.WriteString(" Use <code>feature_detail</code> to inspect the full log.")
	b.WriteString(" You can run <code>build_feature</code> again to retry.")
	return b.String()
}

func shouldAutoRepairBuildFailure(output string, runErr error) bool {
	if runErr == nil {
		return false
	}

	combined := strings.ToLower(strings.TrimSpace(output + "\n" + runErr.Error()))
	switch {
	case combined == "":
		return true
	case strings.Contains(combined, "context deadline exceeded"),
		strings.Contains(combined, "deadline exceeded"),
		strings.Contains(combined, "timed out"),
		strings.Contains(combined, "signal: killed"),
		strings.Contains(combined, "terminated by signal"),
		strings.Contains(combined, "subprocess was killed"),
		strings.Contains(combined, "failed to record rollout items"),
		strings.Contains(combined, "channel closed"),
		strings.Contains(combined, "quota"),
		strings.Contains(combined, "rate limit"),
		strings.Contains(combined, "not authenticated"),
		strings.Contains(combined, "login required"),
		strings.Contains(combined, "authentication required"),
		strings.Contains(combined, "not inside a trusted directory"),
		strings.Contains(combined, "--skip-git-repo-check"),
		strings.Contains(combined, "sandbox-exec: sandbox_apply"),
		strings.Contains(combined, "build workspace is not a goclaw source checkout"),
		strings.Contains(combined, "executable file not found"),
		strings.Contains(combined, "command not found"):
		return false
	case strings.Contains(combined, "operation not permitted") &&
		(strings.Contains(combined, "/library/caches/go-build") ||
			strings.Contains(combined, "/pkg/mod/cache") ||
			strings.Contains(combined, "gocache") ||
			strings.Contains(combined, "gomodcache")):
		return false
	case strings.Contains(combined, "unexpected argument") && strings.Contains(combined, "usage: codex"):
		return false
	default:
		return true
	}
}

func describeBuildProcessError(ctx context.Context, label string, err error) error {
	if err == nil {
		return nil
	}

	label = strings.TrimSpace(label)
	if label == "" {
		label = "subprocess"
	}

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("%s timed out after %s and the subprocess was killed", label, buildTimeout)
	case errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("%s was canceled and the subprocess was killed", label)
	}

	lowerErr := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(lowerErr, "signal: killed") {
		return fmt.Errorf("%s terminated by signal: killed", label)
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ProcessState != nil {
		state := strings.TrimSpace(exitErr.ProcessState.String())
		if state == "" {
			return err
		}
		lowerState := strings.ToLower(state)
		if strings.Contains(lowerState, "signal:") {
			return fmt.Errorf("%s terminated by %s", label, state)
		}
	}

	return err
}

func summarizeBuildFailure(output string, runErr error) string {
	lines := strings.Split(output, "\n")
	important := make([]string, 0, 2)
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case line == "STDERR:":
			continue
		case strings.Contains(lower, "could not update path"):
			continue
		case strings.HasPrefix(lower, "usage: codex"):
			continue
		case strings.HasPrefix(lower, "for more information, try"):
			continue
		case strings.Contains(lower, "not inside a trusted directory"),
			strings.Contains(lower, "--skip-git-repo-check"):
			important = append(important, line)
		case strings.Contains(lower, "operation not permitted") &&
			(strings.Contains(lower, "/library/caches/go-build") ||
				strings.Contains(lower, "/pkg/mod/cache") ||
				strings.Contains(lower, "gocache") ||
				strings.Contains(lower, "gomodcache")):
			important = append(important, line)
		case strings.Contains(lower, "error:"),
			strings.Contains(lower, "failed"),
			strings.Contains(lower, "panic"),
			strings.Contains(lower, "undefined"),
			strings.Contains(lower, "exit status"):
			important = append(important, line)
		}
		if len(important) == 2 {
			break
		}
	}
	if len(important) == 0 && runErr != nil {
		important = append(important, runErr.Error())
	}
	return truncateRunes(strings.Join(important, " | "), 220)
}

func truncateBuildOutput(output string) string {
	if len(output) <= 50000 {
		return output
	}
	return output[:25000] + "\n\n... (truncated) ...\n\n" + output[len(output)-25000:]
}

func appendBuildAttemptLog(existing, entry string) string {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return existing
	}
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return entry + "\n"
	}
	return existing + "\n\n---\n\n" + entry + "\n"
}

func appendVerificationOutput(output, verification string) string {
	verification = strings.TrimSpace(verification)
	if verification == "" {
		return output
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return "VERIFICATION:\n" + verification
	}
	return output + "\n\nVERIFICATION:\n" + verification
}

func captureBuildFollowupContext(ctx context.Context, req *FeatureRequest) *buildFollowupContext {
	if ctx == nil {
		return nil
	}

	agentKey := strings.TrimSpace(tools.ToolAgentKeyFromCtx(ctx))
	if agentKey == "" {
		agentKey = strings.TrimSpace(store.AgentKeyFromContext(ctx))
	}

	channel := strings.TrimSpace(tools.ToolChannelFromCtx(ctx))
	if channel == "" && req != nil {
		channel = strings.TrimSpace(req.Channel)
	}

	chatID := strings.TrimSpace(tools.ToolChatIDFromCtx(ctx))
	if chatID == "" && req != nil {
		chatID = strings.TrimSpace(req.ChatID)
	}

	peerKind := strings.TrimSpace(tools.ToolPeerKindFromCtx(ctx))
	localKey := strings.TrimSpace(tools.ToolLocalKeyFromCtx(ctx))
	if localKey == "" && req != nil {
		localKey = strings.TrimSpace(req.LocalKey)
	}
	sessionKey := strings.TrimSpace(tools.ToolSessionKeyFromCtx(ctx))

	if agentKey == "" || channel == "" || chatID == "" {
		return nil
	}
	if peerKind == "" && sessionKey == "" {
		return nil
	}

	return &buildFollowupContext{
		AgentKey:   agentKey,
		Channel:    channel,
		ChatID:     chatID,
		PeerKind:   peerKind,
		LocalKey:   localKey,
		SessionKey: sessionKey,
		UserID:     strings.TrimSpace(store.UserIDFromContext(ctx)),
		TenantID:   store.TenantIDFromContext(ctx),
	}
}

func (t *buildFeatureTool) enqueueBuildFollowup(req *FeatureRequest, followup *buildFollowupContext, retrying bool, summary string) {
	if t == nil || t.feature == nil || t.feature.msgBus == nil || req == nil || followup == nil {
		return
	}
	if strings.TrimSpace(followup.AgentKey) == "" || strings.TrimSpace(followup.Channel) == "" || strings.TrimSpace(followup.ChatID) == "" {
		return
	}

	meta := map[string]string{
		tools.MetaOriginChannel:  followup.Channel,
		tools.MetaOriginPeerKind: followup.PeerKind,
		"feature_id":             req.ID,
		"feature_status":         req.Status,
	}
	if strings.TrimSpace(followup.UserID) != "" {
		meta[tools.MetaOriginUserID] = followup.UserID
	}
	if strings.TrimSpace(followup.LocalKey) != "" {
		meta[tools.MetaOriginLocalKey] = followup.LocalKey
	}
	if strings.TrimSpace(followup.SessionKey) != "" {
		meta[tools.MetaOriginSessionKey] = followup.SessionKey
	}

	msg := bus.InboundMessage{
		Channel:  tools.ChannelSystem,
		SenderID: BuildFollowupSenderID(req.ID),
		ChatID:   followup.ChatID,
		Content:  buildFollowupMessage(req, retrying, summary),
		UserID:   followup.UserID,
		TenantID: followup.TenantID,
		AgentID:  followup.AgentKey,
		Metadata: meta,
	}

	if !t.feature.msgBus.TryPublishInbound(msg) {
		slog.Warn("beta feature_requests: build follow-up dropped (inbound buffer full)",
			"feature_id", req.ID, "agent", followup.AgentKey)
	}
}

func buildFollowupMessage(req *FeatureRequest, retrying bool, summary string) string {
	var b strings.Builder
	b.WriteString("[System Message] A background feature build you started has finished.\n\n")
	fmt.Fprintf(&b, "Feature: %s\nFeature ID: %s\nRecorded status: %s\n", req.Title, req.ID, req.Status)
	if summary = strings.TrimSpace(summary); summary != "" {
		fmt.Fprintf(&b, "\nRuntime summary: %s\n", summary)
	}
	b.WriteString("\nNext step:\n")
	b.WriteString("1. Call feature_detail for this feature_id and inspect the latest build log.\n")
	b.WriteString("2. Then send the chat a concise update explaining what happened and what should happen next.\n")
	if req.Status == StatusCompleted {
		b.WriteString("Do not claim success unless feature_detail confirms the implementation and verification actually passed.\n")
		b.WriteString("If the build really succeeded and the feature should be turned on now, you may call activate_beta_feature and any feature-specific configure/run tools that are available in this gateway.\n")
	} else if retrying {
		b.WriteString("This was already a retry. Do not automatically call build_feature again from this follow-up turn unless the log shows a single clear, bounded next attempt is warranted.\n")
	} else {
		b.WriteString("Do not automatically call build_feature again from this follow-up turn unless the log shows a single clear, bounded retry is warranted.\n")
	}
	return b.String()
}

func resolveBuildWorkspace(defaultWorkspace string) string {
	candidates := make([]string, 0, 4)
	if v := strings.TrimSpace(os.Getenv("GOCLAW_FEATURE_BUILD_WORKSPACE")); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates, defaultWorkspace)
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Dir(exe))
	}
	return resolveBuildWorkspaceCandidates(candidates...)
}

func resolveBuildWorkspaceCandidates(candidates ...string) string {
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		if looksLikeGoClawRepoRoot(abs) {
			return abs
		}
	}
	return ""
}

func looksLikeGoClawRepoRoot(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	required := []string{
		"go.mod",
		filepath.Join("internal", "beta"),
		filepath.Join("skills", "beta-feature", "SKILL.md"),
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			return false
		}
	}
	return true
}

func verifyBuildOutput(ctx context.Context, workspace, output string, buildStartedAt time.Time, requireFreshArtifacts bool) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if !looksLikeGoClawRepoRoot(workspace) {
		return "", fmt.Errorf("build workspace is not a GoClaw source checkout: %s", workspace)
	}

	manifest, err := extractBuildArtifacts(output)
	if err != nil {
		return fmt.Sprintf("Artifact manifest check failed: %v", err), fmt.Errorf("artifact manifest check failed: %w", err)
	}
	if err := verifyBuildArtifacts(workspace, manifest, buildStartedAt, requireFreshArtifacts); err != nil {
		return fmt.Sprintf("Artifact manifest check failed: %v", err), fmt.Errorf("artifact manifest check failed: %w", err)
	}

	var detail strings.Builder
	fmt.Fprintf(&detail, "Artifact manifest verified for %s.", manifest.FeatureRoot)

	buildOut, err := runBuildVerificationCommand(ctx, workspace, "go", "build", "./...")
	if buildOut != "" {
		fmt.Fprintf(&detail, "\n\n$ go build ./...\n%s", buildOut)
	}
	if err != nil {
		return strings.TrimSpace(detail.String()), fmt.Errorf("go build ./... verification failed: %w", err)
	}
	detail.WriteString("\n\ngo build ./... passed.")

	vetOut, err := runBuildVerificationCommand(ctx, workspace, "go", "vet", "./...")
	if vetOut != "" {
		fmt.Fprintf(&detail, "\n\n$ go vet ./...\n%s", vetOut)
	}
	if err != nil {
		return strings.TrimSpace(detail.String()), fmt.Errorf("go vet ./... verification failed: %w", err)
	}
	detail.WriteString("\n\ngo vet ./... passed.")

	return strings.TrimSpace(detail.String()), nil
}

func extractBuildArtifacts(output string) (buildArtifactsManifest, error) {
	idx := strings.LastIndex(output, buildArtifactsMarker)
	if idx < 0 {
		return buildArtifactsManifest{}, errors.New("missing BUILD_ARTIFACTS manifest in Codex output")
	}

	manifestLine := strings.TrimSpace(output[idx+len(buildArtifactsMarker):])
	if newline := strings.IndexByte(manifestLine, '\n'); newline >= 0 {
		manifestLine = strings.TrimSpace(manifestLine[:newline])
	}
	if manifestLine == "" {
		return buildArtifactsManifest{}, errors.New("BUILD_ARTIFACTS manifest is empty")
	}

	var manifest buildArtifactsManifest
	if err := json.Unmarshal([]byte(manifestLine), &manifest); err != nil {
		return buildArtifactsManifest{}, fmt.Errorf("invalid BUILD_ARTIFACTS JSON: %w", err)
	}
	if strings.TrimSpace(manifest.FeatureRoot) == "" {
		return buildArtifactsManifest{}, errors.New("BUILD_ARTIFACTS.feature_root is required")
	}
	if len(manifest.Files) == 0 {
		return buildArtifactsManifest{}, errors.New("BUILD_ARTIFACTS.files must list at least one file")
	}
	return manifest, nil
}

func verifyBuildArtifacts(workspace string, manifest buildArtifactsManifest, buildStartedAt time.Time, requireFreshArtifacts bool) error {
	featureRoot, err := cleanBuildRelativePath(manifest.FeatureRoot)
	if err != nil {
		return fmt.Errorf("feature_root: %w", err)
	}
	if !strings.HasPrefix(featureRoot, "internal/beta/") {
		return fmt.Errorf("feature_root must be under internal/beta/, got %q", featureRoot)
	}
	switch featureRoot {
	case "internal/beta", "internal/beta/all", "internal/beta/_example", "internal/beta/feature_requests":
		return fmt.Errorf("feature_root points to builder infrastructure instead of a new feature: %q", featureRoot)
	}

	rootInfo, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(featureRoot)))
	if err != nil {
		return fmt.Errorf("feature_root %q: %w", featureRoot, err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("feature_root %q is not a directory", featureRoot)
	}

	var (
		hasFeatureFile   bool
		hasFeatureSource bool
		hasFreshArtifact bool
	)
	for _, rawPath := range manifest.Files {
		rel, err := cleanBuildRelativePath(rawPath)
		if err != nil {
			return fmt.Errorf("manifest file %q: %w", rawPath, err)
		}
		info, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("manifest file %q: %w", rel, err)
		}
		if info.IsDir() {
			return fmt.Errorf("manifest file %q is a directory, want a file", rel)
		}
		if strings.HasPrefix(rel, featureRoot+"/") {
			hasFeatureSource = true
			if rel == featureRoot+"/feature.go" {
				hasFeatureFile = true
			}
		}
		if requireFreshArtifacts && !info.ModTime().Before(buildStartedAt.Add(-1*time.Second)) {
			hasFreshArtifact = true
		}
	}
	if !hasFeatureSource {
		return fmt.Errorf("manifest must include at least one file under %s", featureRoot)
	}
	if !hasFeatureFile {
		return fmt.Errorf("manifest must include %s/feature.go", featureRoot)
	}
	if requireFreshArtifacts && !hasFreshArtifact {
		return fmt.Errorf("no manifest file was updated during this build attempt")
	}
	return nil
}

func cleanBuildRelativePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is empty")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be repo-relative, got %q", path)
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes repo root: %q", path)
	}
	return clean, nil
}

func runBuildVerificationCommand(ctx context.Context, workspace string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workspace
	env, err := buildProcessEnv()
	if err != nil {
		return "", fmt.Errorf("prepare build env: %w", err)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	output := strings.TrimSpace(stdout.String())
	if errOut := strings.TrimSpace(stderr.String()); errOut != "" {
		if output == "" {
			output = "STDERR:\n" + errOut
		} else {
			output += "\n\nSTDERR:\n" + errOut
		}
	}
	return truncateBuildOutput(output), err
}

func buildProcessEnv() ([]string, error) {
	cacheRoot := filepath.Join(os.TempDir(), "goclaw-feature-build")
	paths := []struct {
		key string
		dir string
	}{
		{key: "GOCACHE", dir: filepath.Join(cacheRoot, "gocache")},
		{key: "GOMODCACHE", dir: filepath.Join(cacheRoot, "gomodcache")},
		{key: "GOTMPDIR", dir: filepath.Join(cacheRoot, "tmp")},
	}

	env := os.Environ()
	for _, path := range paths {
		if err := os.MkdirAll(path.dir, 0o755); err != nil {
			return nil, fmt.Errorf("prepare %s at %s: %w", path.key, path.dir, err)
		}
		env = setEnvValue(env, path.key, path.dir)
	}
	return env, nil
}

func setEnvValue(env []string, key, value string) []string {
	if len(env) == 0 {
		return []string{key + "=" + value}
	}

	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	out = append(out, prefix+value)
	return out
}

func buildCodexPrompt(req *FeatureRequest) string {
	return fmt.Sprintf(`You are building a new beta feature for the GoClaw project.

## Feature: %s

## Description:
%s

## Instructions:
1. Read the beta-feature SKILL.md at skills/beta-feature/SKILL.md for the beta feature architecture guide
2. Create the feature as a new beta feature folder under internal/beta/ following the guide
3. Implement all necessary tools, RPC methods, HTTP routes, and DB tables
4. Register the feature in internal/beta/all/all.go
5. If shared/common GoClaw code or builder infrastructure blocks the feature, fix that too
6. You are explicitly allowed to change common/shared code when required to unblock this feature; no extra approval is needed
7. Run go build ./... and go vet ./... to verify compilation
8. Keep iterating until build + vet pass inside this run; do not stop at analysis
9. Write a brief plan summary as a comment in feature.go
10. End your final response with exactly one single-line manifest in this format:
BUILD_ARTIFACTS: {"feature_root":"internal/beta/<feature_folder>","files":["internal/beta/<feature_folder>/feature.go","internal/beta/all/all.go"]}
Only list repo-relative paths that actually exist after your changes.

Keep the implementation focused and minimal. Follow existing patterns in the codebase.`,
		req.Title, req.Description)
}

func buildRepairPrompt(req *FeatureRequest, output, summary string, attempt, attempts int) string {
	excerpt := truncateRunes(strings.TrimSpace(output), buildFailureExcerptSize)
	if excerpt == "" {
		excerpt = "(no output captured)"
	}

	return fmt.Sprintf(`You are continuing a failed background build for a GoClaw beta feature.

## Feature: %s

## Description:
%s

## Repair Attempt:
%d of %d

## Previous Failure Summary:
%s

## Repair Rules:
1. Fix the failure that blocked the previous attempt.
2. You are explicitly allowed to modify shared/common GoClaw code, tooling, and beta infrastructure when that is what blocks the feature.
3. Re-run go build ./... and go vet ./... inside this run.
4. If the next failure reveals another shared-code blocker, fix that too instead of stopping.
5. Do not ask for approval. Do not stop at analysis. Leave the repo in a state where this feature builds cleanly if possible.
6. End your final response with exactly one single-line manifest in this format:
BUILD_ARTIFACTS: {"feature_root":"internal/beta/<feature_folder>","files":["internal/beta/<feature_folder>/feature.go","internal/beta/all/all.go"]}
Only list repo-relative paths that actually exist after your changes.

## Previous Failure Log Excerpt:
%s`,
		req.Title, req.Description, attempt, attempts, summary, excerpt)
}
