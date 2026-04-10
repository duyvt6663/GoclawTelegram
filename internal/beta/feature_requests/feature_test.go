package featurerequests

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"

	_ "modernc.org/sqlite"
)

func TestFeatureStoreSQLiteTenantIsolationAndUpdate(t *testing.T) {
	store := newTestFeatureStore(t)

	reqA := newTestFeatureRequest("req-a", "tenant-a")
	reqA.PollID = "poll-a"
	reqA.LocalKey = "-1001:topic:9"
	reqA.ChatID = "-1001"
	reqA.Channel = "telegram"

	reqB := newTestFeatureRequest("req-b", "tenant-b")
	reqB.PollID = "poll-b"

	if err := store.create(reqA); err != nil {
		t.Fatalf("create tenant-a request: %v", err)
	}
	if err := store.create(reqB); err != nil {
		t.Fatalf("create tenant-b request: %v", err)
	}

	gotA, err := store.getByID("tenant-a", reqA.ID)
	if err != nil {
		t.Fatalf("getByID tenant-a: %v", err)
	}
	if gotA.LocalKey != reqA.LocalKey {
		t.Fatalf("LocalKey = %q, want %q", gotA.LocalKey, reqA.LocalKey)
	}
	if gotA.CreatedAt.IsZero() || gotA.UpdatedAt.IsZero() {
		t.Fatalf("timestamps were not scanned correctly: created=%v updated=%v", gotA.CreatedAt, gotA.UpdatedAt)
	}

	gotA.ChatID = "-1001-updated"
	gotA.LocalKey = "-1001:topic:10"
	if err := store.update(gotA); err != nil {
		t.Fatalf("update tenant-a request: %v", err)
	}
	gotA, err = store.getByID("tenant-a", reqA.ID)
	if err != nil {
		t.Fatalf("getByID tenant-a after update: %v", err)
	}
	if gotA.ChatID != "-1001-updated" {
		t.Fatalf("ChatID after update = %q, want %q", gotA.ChatID, "-1001-updated")
	}
	if gotA.LocalKey != "-1001:topic:10" {
		t.Fatalf("LocalKey after update = %q, want %q", gotA.LocalKey, "-1001:topic:10")
	}

	if _, err := store.getByID("tenant-b", reqA.ID); err == nil {
		t.Fatalf("cross-tenant getByID unexpectedly succeeded")
	}

	listA, err := store.list("tenant-a")
	if err != nil {
		t.Fatalf("list tenant-a: %v", err)
	}
	if len(listA) != 1 || listA[0].ID != reqA.ID {
		t.Fatalf("list tenant-a = %+v, want only %s", listA, reqA.ID)
	}

	if _, err := store.getByPollID("tenant-a", reqA.PollID); err != nil {
		t.Fatalf("getByPollID tenant-a: %v", err)
	}
	if _, err := store.getByPollID("tenant-b", reqA.PollID); err == nil {
		t.Fatalf("cross-tenant getByPollID unexpectedly succeeded")
	}
}

func TestFeatureRequestsFeature_HandlePollAnswerTracksVoteChangesAndApproval(t *testing.T) {
	feature := newTestFeatureFeature(t)

	req := newTestFeatureRequest("topic-bot", "")
	req.Channel = "telegram"
	req.ChatID = "-100777"
	req.LocalKey = "-100777:topic:42"
	req.PollID = "poll-1"

	if err := feature.store.create(req); err != nil {
		t.Fatalf("create request: %v", err)
	}

	feature.handlePollAnswer(bus.Event{
		TenantID: uuid.Nil,
		Payload: map[string]any{
			"poll_id":    req.PollID,
			"voter_id":   "user-1",
			"option_ids": []int{0},
		},
	})

	got, err := feature.store.getByID("", req.ID)
	if err != nil {
		t.Fatalf("getByID after first vote: %v", err)
	}
	if got.Approvals != 1 || got.Status != StatusPending {
		t.Fatalf("after first yes vote = approvals:%d status:%s, want approvals:1 status:%s", got.Approvals, got.Status, StatusPending)
	}

	feature.handlePollAnswer(bus.Event{
		TenantID: uuid.Nil,
		Payload: map[string]any{
			"poll_id":    req.PollID,
			"voter_id":   "user-1",
			"option_ids": []int{1},
		},
	})

	got, err = feature.store.getByID("", req.ID)
	if err != nil {
		t.Fatalf("getByID after vote change: %v", err)
	}
	if got.Approvals != 0 || len(got.Voters) != 0 {
		t.Fatalf("after vote change = approvals:%d voters:%v, want approvals:0 voters:[]", got.Approvals, got.Voters)
	}

	for i := 1; i <= approvalThreshold; i++ {
		feature.handlePollAnswer(bus.Event{
			TenantID: uuid.Nil,
			Payload: map[string]any{
				"poll_id":    req.PollID,
				"voter_id":   fmt.Sprintf("user-%d", i),
				"option_ids": []int{0},
			},
		})
	}

	got, err = feature.store.getByID("", req.ID)
	if err != nil {
		t.Fatalf("getByID after threshold: %v", err)
	}
	if got.Status != StatusApproved || got.Approvals != approvalThreshold {
		t.Fatalf("after threshold = status:%s approvals:%d, want status:%s approvals:%d", got.Status, got.Approvals, StatusApproved, approvalThreshold)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	msg, ok := feature.msgBus.SubscribeOutbound(ctx)
	if !ok {
		t.Fatalf("expected approval outbound message")
	}
	if msg.Channel != req.Channel || msg.ChatID != req.ChatID {
		t.Fatalf("outbound routing = channel:%q chat:%q, want channel:%q chat:%q", msg.Channel, msg.ChatID, req.Channel, req.ChatID)
	}
	if gotKey := msg.Metadata["local_key"]; gotKey != req.LocalKey {
		t.Fatalf("outbound local_key = %q, want %q", gotKey, req.LocalKey)
	}
}

func TestFeatureRequestsFeature_PollCloseRejectsAndLateVoteCanRecover(t *testing.T) {
	feature := newTestFeatureFeature(t)

	req := newTestFeatureRequest("late-vote", "")
	req.Channel = "telegram"
	req.ChatID = "-100888"
	req.LocalKey = "-100888:topic:7"
	req.PollID = "poll-late"
	req.Approvals = approvalThreshold - 1
	req.Voters = []string{"user-1", "user-2", "user-3", "user-4"}

	if err := feature.store.create(req); err != nil {
		t.Fatalf("create request: %v", err)
	}

	feature.handlePollClosed(bus.Event{
		TenantID: uuid.Nil,
		Payload:  map[string]any{"poll_id": req.PollID},
	})

	got, err := feature.store.getByID("", req.ID)
	if err != nil {
		t.Fatalf("getByID after close: %v", err)
	}
	if got.Status != StatusRejected {
		t.Fatalf("after close status = %s, want %s", got.Status, StatusRejected)
	}

	feature.handlePollAnswer(bus.Event{
		TenantID: uuid.Nil,
		Payload: map[string]any{
			"poll_id":    req.PollID,
			"voter_id":   "user-5",
			"option_ids": []int{0},
		},
	})

	got, err = feature.store.getByID("", req.ID)
	if err != nil {
		t.Fatalf("getByID after late vote: %v", err)
	}
	if got.Status != StatusApproved || got.Approvals != approvalThreshold {
		t.Fatalf("after late vote = status:%s approvals:%d, want status:%s approvals:%d", got.Status, got.Approvals, StatusApproved, approvalThreshold)
	}
}

func TestFeaturePollTool_LopTruongCanDirectApproveWithoutPoll(t *testing.T) {
	feature := newTestFeatureFeature(t)

	req := newTestFeatureRequest("vip-approve", "")
	req.Channel = "telegram"
	req.ChatID = "-100777"
	req.LocalKey = "-100777:topic:42"
	if err := feature.store.create(req); err != nil {
		t.Fatalf("create request: %v", err)
	}

	tool := &featurePollTool{feature: feature}
	ctx := newFeaturePollTestContext("123|duyvt6663", "telegram", "-100777", "-100777:topic:42")

	result := tool.Execute(ctx, map[string]any{"feature_id": req.ID})
	if result.IsError {
		t.Fatalf("Execute() error = %s", result.ForLLM)
	}

	got, err := feature.store.getByID("", req.ID)
	if err != nil {
		t.Fatalf("getByID after direct approval: %v", err)
	}
	if got.Status != StatusApproved {
		t.Fatalf("status after direct approval = %s, want %s", got.Status, StatusApproved)
	}
	if got.PollID != "" || got.PollMsgID != 0 {
		t.Fatalf("poll fields after direct approval = poll_id:%q poll_msg_id:%d, want empty/0", got.PollID, got.PollMsgID)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(result.ForLLM), &payload); err != nil {
		t.Fatalf("unmarshal result JSON: %v", err)
	}
	if payload["status"] != StatusApproved {
		t.Fatalf("result status = %v, want %s", payload["status"], StatusApproved)
	}
	if payload["approved_by"] != featureRequestsLopTruong {
		t.Fatalf("result approved_by = %v, want %s", payload["approved_by"], featureRequestsLopTruong)
	}
}

func TestFeaturePollTool_NonLopTruongCreatesPoll(t *testing.T) {
	feature := newTestFeatureFeature(t)
	creator := &stubTelegramPollCreator{pollID: "poll-123", messageID: 77}
	feature.resolve = func(channel string) TelegramPollCreator {
		if channel != "telegram-builder" {
			return nil
		}
		return creator
	}

	req := newTestFeatureRequest("normal-poll", "")
	if err := feature.store.create(req); err != nil {
		t.Fatalf("create request: %v", err)
	}

	tool := &featurePollTool{feature: feature}
	ctx := newFeaturePollTestContext("999|someone_else", "telegram-builder", "-100555", "-100555:topic:9")

	result := tool.Execute(ctx, map[string]any{"feature_id": req.ID})
	if result.IsError {
		t.Fatalf("Execute() error = %s", result.ForLLM)
	}
	if creator.calls != 1 {
		t.Fatalf("CreateSoDauBaiPoll calls = %d, want 1", creator.calls)
	}
	if creator.chatID != -100555 || creator.threadID != 9 {
		t.Fatalf("poll routing = chat:%d thread:%d, want chat:-100555 thread:9", creator.chatID, creator.threadID)
	}

	got, err := feature.store.getByID("", req.ID)
	if err != nil {
		t.Fatalf("getByID after poll creation: %v", err)
	}
	if got.Status != StatusPending {
		t.Fatalf("status after poll creation = %s, want %s", got.Status, StatusPending)
	}
	if got.PollID != creator.pollID || got.PollMsgID != creator.messageID {
		t.Fatalf("poll fields after creation = poll_id:%q poll_msg_id:%d, want %q/%d", got.PollID, got.PollMsgID, creator.pollID, creator.messageID)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(result.ForLLM), &payload); err != nil {
		t.Fatalf("unmarshal result JSON: %v", err)
	}
	if payload["poll_id"] != creator.pollID {
		t.Fatalf("result poll_id = %v, want %s", payload["poll_id"], creator.pollID)
	}
}

func TestCodexBuildArgsUsesNonInteractiveFullAutoExec(t *testing.T) {
	got := codexBuildArgs("build this feature")
	want := []string{"exec", "--full-auto", "--skip-git-repo-check", "build this feature"}
	if len(got) != len(want) {
		t.Fatalf("len(codexBuildArgs()) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("codexBuildArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildTimeoutIsTwoHours(t *testing.T) {
	if buildTimeout != 2*time.Hour {
		t.Fatalf("buildTimeout = %s, want %s", buildTimeout, 2*time.Hour)
	}
}

func TestActivateBetaFeatureToolEnablesAndHotActivatesFeature(t *testing.T) {
	t.Cleanup(func() { beta.ShutdownAll(context.Background()) })

	featureName := "test_activate_beta_feature_tool"
	beta.Register(&testActivatableBetaFeature{name: featureName})

	feature := newTestFeatureFeature(t)
	sysConfigs := newFeatureTestSystemConfigStore()
	feature.sysConfigs = sysConfigs
	feature.betaDeps = beta.Deps{}

	tool := &activateBetaFeatureTool{feature: feature}
	ctx := store.WithTenantID(context.Background(), store.MasterTenantID)

	result := tool.Execute(ctx, map[string]any{"feature_name": featureName})
	if result.IsError {
		t.Fatalf("Execute() error = %s", result.ForLLM)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(result.ForLLM), &payload); err != nil {
		t.Fatalf("unmarshal result JSON: %v", err)
	}
	if payload["enabled"] != true {
		t.Fatalf("enabled = %v, want true", payload["enabled"])
	}
	if payload["runtime_active"] != true {
		t.Fatalf("runtime_active = %v, want true", payload["runtime_active"])
	}
	if payload["hot_activated"] != true {
		t.Fatalf("hot_activated = %v, want true", payload["hot_activated"])
	}

	value, err := sysConfigs.Get(ctx, "beta."+featureName)
	if err != nil {
		t.Fatalf("Get(system config) error = %v", err)
	}
	if value != "true" {
		t.Fatalf("system config value = %q, want %q", value, "true")
	}
}

func TestResolveBuildWorkspaceCandidatesPrefersGoClawCheckout(t *testing.T) {
	invalid := t.TempDir()
	repoRoot := newTestBuildRepo(t)

	got := resolveBuildWorkspaceCandidates(invalid, repoRoot)
	if got != repoRoot {
		t.Fatalf("resolveBuildWorkspaceCandidates() = %q, want %q", got, repoRoot)
	}
}

func TestExtractBuildArtifactsParsesSingleLineManifest(t *testing.T) {
	output := strings.Join([]string{
		"done",
		`BUILD_ARTIFACTS: {"feature_root":"internal/beta/russian_roulette","files":["internal/beta/russian_roulette/feature.go","internal/beta/all/all.go"]}`,
	}, "\n")

	got, err := extractBuildArtifacts(output)
	if err != nil {
		t.Fatalf("extractBuildArtifacts() error = %v", err)
	}
	if got.FeatureRoot != "internal/beta/russian_roulette" {
		t.Fatalf("FeatureRoot = %q", got.FeatureRoot)
	}
	if len(got.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2", len(got.Files))
	}
}

func TestCanBuildFeatureStatusAllowsApprovedAndFailed(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{status: StatusApproved, want: true},
		{status: StatusFailed, want: true},
		{status: StatusPending, want: false},
		{status: StatusBuilding, want: false},
		{status: StatusCompleted, want: false},
	}

	for _, tc := range cases {
		if got := canBuildFeatureStatus(tc.status); got != tc.want {
			t.Fatalf("canBuildFeatureStatus(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestBuildResultAndLifecycleMessagesCoverRetryStates(t *testing.T) {
	if got := buildResultMessage("Russian Roulette", false); !strings.Contains(got, "Queued a background build") {
		t.Fatalf("buildResultMessage(initial) = %q", got)
	}
	if got := buildResultMessage("Russian Roulette", true); !strings.Contains(got, "Queued a background retry") {
		t.Fatalf("buildResultMessage(retry) = %q", got)
	}
	if got := buildStartAnnouncement("Russian Roulette", false); !strings.Contains(got, "build started in the background") {
		t.Fatalf("buildStartAnnouncement(initial) = %q", got)
	}
	if got := buildStartAnnouncement("Russian Roulette", true); !strings.Contains(got, "Retrying feature") {
		t.Fatalf("buildStartAnnouncement(retry) = %q", got)
	}
	if got := buildSuccessAnnouncement("Russian Roulette", true); !strings.Contains(got, "retry completed successfully") {
		t.Fatalf("buildSuccessAnnouncement(retry) = %q", got)
	}
	if got := buildFailureAnnouncement("Russian Roulette", true, "error: boom"); !strings.Contains(got, "retry failed") || !strings.Contains(got, "build_feature") {
		t.Fatalf("buildFailureAnnouncement(retry) = %q", got)
	}
}

func TestSummarizeBuildFailureSkipsNoiseAndKeepsUsefulErrors(t *testing.T) {
	output := strings.Join([]string{
		"WARNING: proceeding, even though we could not update PATH: Operation not permitted (os error 1)",
		"",
		"STDERR:",
		"error: unexpected argument '--approval-mode' found",
		"Usage: codex [OPTIONS] [PROMPT]",
		"Build failed: exit status 2",
	}, "\n")

	got := summarizeBuildFailure(output, fmt.Errorf("exit status 2"))
	if strings.Contains(strings.ToLower(got), "could not update path") {
		t.Fatalf("summarizeBuildFailure() included ignored warning: %q", got)
	}
	if !strings.Contains(got, "unexpected argument '--approval-mode' found") {
		t.Fatalf("summarizeBuildFailure() = %q, want codex flag error", got)
	}
	if !strings.Contains(got, "Build failed: exit status 2") {
		t.Fatalf("summarizeBuildFailure() = %q, want terminal failure line", got)
	}
}

func TestSummarizeBuildFailureKeepsRepoTrustError(t *testing.T) {
	output := strings.Join([]string{
		"STDERR:",
		"WARNING: proceeding, even though we could not update PATH: Operation not permitted (os error 1)",
		"Not inside a trusted directory and --skip-git-repo-check was not specified.",
		"Build failed: exit status 1",
	}, "\n")

	got := summarizeBuildFailure(output, fmt.Errorf("exit status 1"))
	if !strings.Contains(got, "Not inside a trusted directory") {
		t.Fatalf("summarizeBuildFailure() = %q, want repo trust failure", got)
	}
	if !strings.Contains(got, "Build failed: exit status 1") {
		t.Fatalf("summarizeBuildFailure() = %q, want terminal failure line", got)
	}
}

func TestAppendBuildAttemptLogPreservesHistory(t *testing.T) {
	first := appendBuildAttemptLog("", "Build started at 2026-04-10T15:37:13+07:00")
	second := appendBuildAttemptLog(first, "Build started at 2026-04-10T15:40:17+07:00")

	if !strings.Contains(second, "2026-04-10T15:37:13+07:00") {
		t.Fatalf("appendBuildAttemptLog() dropped first attempt: %q", second)
	}
	if !strings.Contains(second, "2026-04-10T15:40:17+07:00") {
		t.Fatalf("appendBuildAttemptLog() dropped second attempt: %q", second)
	}
	if !strings.Contains(second, "---") {
		t.Fatalf("appendBuildAttemptLog() missing separator: %q", second)
	}
}

func TestShouldAutoRepairBuildFailureClassifiesRepoAndCLIProblems(t *testing.T) {
	if !shouldAutoRepairBuildFailure("internal/tools/shared.go:12: undefined: helperX", fmt.Errorf("exit status 1")) {
		t.Fatal("expected shared-code compile failure to be auto-repairable")
	}
	if shouldAutoRepairBuildFailure("Not inside a trusted directory and --skip-git-repo-check was not specified.", fmt.Errorf("exit status 1")) {
		t.Fatal("expected codex repo trust failure to be non-repairable by the inner loop")
	}
	if shouldAutoRepairBuildFailure("error: unexpected argument '--approval-mode' found\nUsage: codex [OPTIONS] [PROMPT]", fmt.Errorf("exit status 2")) {
		t.Fatal("expected codex CLI usage failure to be non-repairable by the inner loop")
	}
	if shouldAutoRepairBuildFailure("authentication required", fmt.Errorf("exit status 1")) {
		t.Fatal("expected auth failure to be non-repairable by the inner loop")
	}
	if shouldAutoRepairBuildFailure("sandbox-exec: sandbox_apply: Operation not permitted", fmt.Errorf("exit status 1")) {
		t.Fatal("expected sandbox startup failure to be non-repairable by the inner loop")
	}
	if shouldAutoRepairBuildFailure("failed to record rollout items: failed to queue rollout items: channel closed", fmt.Errorf("signal: killed")) {
		t.Fatal("expected rollout channel closure to be non-repairable by the inner loop")
	}
	if shouldAutoRepairBuildFailure("", fmt.Errorf("codex terminated by signal: killed")) {
		t.Fatal("expected signal-killed codex run to be non-repairable by the inner loop")
	}
}

func TestBuildRepairPromptAllowsSharedCodeFixes(t *testing.T) {
	req := newTestFeatureRequest("Russian Roulette", "")
	req.Description = "Build a Telegram roulette mini game."

	prompt := buildRepairPrompt(req, "internal/tools/shared.go:12: undefined: helperX", "undefined helperX", 2, 3)
	if !strings.Contains(prompt, "shared/common GoClaw code") {
		t.Fatalf("buildRepairPrompt() = %q, want shared-code allowance", prompt)
	}
	if !strings.Contains(prompt, "Do not ask for approval") {
		t.Fatalf("buildRepairPrompt() = %q, want no-approval instruction", prompt)
	}
	if !strings.Contains(prompt, "undefined helperX") {
		t.Fatalf("buildRepairPrompt() = %q, want previous failure summary", prompt)
	}
}

func TestDescribeBuildProcessErrorExplainsTimeoutKill(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	err := describeBuildProcessError(ctx, "codex", context.DeadlineExceeded)
	if err == nil {
		t.Fatal("describeBuildProcessError() returned nil")
	}
	if !strings.Contains(err.Error(), "timed out after 2h0m0s") {
		t.Fatalf("timeout error = %q, want timeout detail", err.Error())
	}
	if !strings.Contains(err.Error(), "subprocess was killed") {
		t.Fatalf("timeout error = %q, want kill detail", err.Error())
	}
}

func TestDescribeBuildProcessErrorExplainsSignalKill(t *testing.T) {
	err := describeBuildProcessError(context.Background(), "codex", fmt.Errorf("signal: killed"))
	if err == nil {
		t.Fatal("describeBuildProcessError() returned nil")
	}
	if got := err.Error(); got != "codex terminated by signal: killed" {
		t.Fatalf("signal kill error = %q, want %q", got, "codex terminated by signal: killed")
	}
}

func TestBuildFeatureToolRunCodexAutoRepairsAndCompletes(t *testing.T) {
	feature := newTestFeatureFeature(t)

	req := newTestFeatureRequest("auto-repair", "")
	req.Channel = "telegram"
	req.ChatID = "-100777"
	req.LocalKey = "-100777:topic:42"
	req.Status = StatusBuilding
	req.BuildLog = "Build started at 2026-04-10T15:37:13+07:00\n"
	if err := feature.store.create(req); err != nil {
		t.Fatalf("create request: %v", err)
	}

	var prompts []string
	tool := &buildFeatureTool{
		feature:   feature,
		workspace: t.TempDir(),
		maxTries:  3,
		runner: func(_ context.Context, workspace, prompt string) (string, error) {
			prompts = append(prompts, prompt)
			switch len(prompts) {
			case 1:
				if workspace == "" {
					t.Fatal("runner workspace was empty")
				}
				return "internal/tools/shared.go:12: undefined: helperX\nBuild failed: exit status 1", fmt.Errorf("exit status 1")
			case 2:
				return "go build ./...\ngo vet ./...\nall good", nil
			default:
				t.Fatalf("unexpected runner call %d", len(prompts))
				return "", nil
			}
		},
		verifier: func(_ context.Context, workspace, output string, buildStartedAt time.Time, requireFreshArtifacts bool) (string, error) {
			if workspace == "" {
				t.Fatal("verifier workspace was empty")
			}
			return "Artifact manifest verified for internal/beta/auto_repair.\ngo build ./... passed.\ngo vet ./... passed.", nil
		},
	}

	tool.runCodex(req, false, tool.workspace, time.Now(), nil)

	got, err := feature.store.getByID("", req.ID)
	if err != nil {
		t.Fatalf("getByID after auto-repair run: %v", err)
	}
	if got.Status != StatusCompleted {
		t.Fatalf("status after auto-repair run = %s, want %s", got.Status, StatusCompleted)
	}
	if len(prompts) != 2 {
		t.Fatalf("runner call count = %d, want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], "shared/common GoClaw code") {
		t.Fatalf("repair prompt = %q, want shared-code repair instruction", prompts[1])
	}
	if !strings.Contains(got.BuildLog, "Automatic repair attempt 2/3 queued") {
		t.Fatalf("build log missing repair note: %q", got.BuildLog)
	}
	if !strings.Contains(got.BuildLog, "Build completed successfully.") {
		t.Fatalf("build log missing completion note: %q", got.BuildLog)
	}
	if !strings.Contains(got.BuildLog, "Artifact manifest verified") {
		t.Fatalf("build log missing verification note: %q", got.BuildLog)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	msg1, ok := feature.msgBus.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected repair announcement outbound message")
	}
	msg2, ok := feature.msgBus.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected success announcement outbound message")
	}
	combined := msg1.Content + "\n" + msg2.Content
	if !strings.Contains(combined, "automatic repair attempt 2/3") {
		t.Fatalf("outbound messages missing repair announcement: %q", combined)
	}
	if !strings.Contains(combined, "built successfully") {
		t.Fatalf("outbound messages missing success announcement: %q", combined)
	}
}

func TestBuildFeatureToolRunCodexVerificationFailureDoesNotFalsePositive(t *testing.T) {
	feature := newTestFeatureFeature(t)

	req := newTestFeatureRequest("false-positive", "")
	req.Channel = "telegram"
	req.ChatID = "-100999"
	req.LocalKey = "-100999:topic:42"
	req.Status = StatusBuilding
	req.BuildLog = "Build started at 2026-04-10T16:00:06+07:00\n"
	if err := feature.store.create(req); err != nil {
		t.Fatalf("create request: %v", err)
	}

	tool := &buildFeatureTool{
		feature:   feature,
		workspace: t.TempDir(),
		maxTries:  1,
		runner: func(_ context.Context, workspace, prompt string) (string, error) {
			return "I'm blocked by the local environment.\nNo files were changed.", nil
		},
		verifier: func(_ context.Context, workspace, output string, buildStartedAt time.Time, requireFreshArtifacts bool) (string, error) {
			return "Artifact manifest check failed: missing BUILD_ARTIFACTS manifest in Codex output", fmt.Errorf("artifact manifest check failed: missing BUILD_ARTIFACTS manifest in Codex output")
		},
	}

	tool.runCodex(req, false, tool.workspace, time.Now(), nil)

	got, err := feature.store.getByID("", req.ID)
	if err != nil {
		t.Fatalf("getByID after verification failure: %v", err)
	}
	if got.Status != StatusFailed {
		t.Fatalf("status after verification failure = %s, want %s", got.Status, StatusFailed)
	}
	if strings.Contains(got.BuildLog, "Build completed successfully.") {
		t.Fatalf("build log incorrectly marked success: %q", got.BuildLog)
	}
	if !strings.Contains(got.BuildLog, "Artifact manifest check failed") {
		t.Fatalf("build log missing verification failure: %q", got.BuildLog)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	msg, ok := feature.msgBus.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected failure outbound message")
	}
	if !strings.Contains(msg.Content, "build failed") {
		t.Fatalf("outbound failure message = %q", msg.Content)
	}
}

func TestBuildFeatureToolRunCodexPublishesBuildFollowupInbound(t *testing.T) {
	feature := newTestFeatureFeature(t)

	req := newTestFeatureRequest("followup", "")
	req.Channel = "telegram"
	req.ChatID = "-100321"
	req.LocalKey = "-100321:topic:42"
	req.Status = StatusBuilding
	req.BuildLog = "Build started at 2026-04-10T16:00:06+07:00\n"
	if err := feature.store.create(req); err != nil {
		t.Fatalf("create request: %v", err)
	}

	tool := &buildFeatureTool{
		feature:   feature,
		workspace: t.TempDir(),
		maxTries:  1,
		runner: func(_ context.Context, workspace, prompt string) (string, error) {
			return "BUILD_ARTIFACTS: {\"feature_root\":\"internal/beta/followup\",\"files\":[\"internal/beta/followup/feature.go\"]}", nil
		},
		verifier: func(_ context.Context, workspace, output string, buildStartedAt time.Time, requireFreshArtifacts bool) (string, error) {
			return "Artifact manifest verified for internal/beta/followup.\ngo build ./... passed.\ngo vet ./... passed.", nil
		},
	}

	followup := &buildFollowupContext{
		AgentKey:   "builder-bot",
		Channel:    "telegram",
		ChatID:     "-100321",
		PeerKind:   "group",
		LocalKey:   req.LocalKey,
		SessionKey: "agent:builder-bot:telegram:group:-100321:topic:42",
		UserID:     "group:telegram:-100321",
	}

	tool.runCodex(req, false, tool.workspace, time.Now(), followup)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	msg, ok := feature.msgBus.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("expected build follow-up inbound message")
	}
	if msg.Channel != tools.ChannelSystem {
		t.Fatalf("inbound channel = %q, want %q", msg.Channel, tools.ChannelSystem)
	}
	if msg.AgentID != "builder-bot" {
		t.Fatalf("inbound agent = %q, want %q", msg.AgentID, "builder-bot")
	}
	if !IsBuildFollowupSender(msg.SenderID) {
		t.Fatalf("sender_id = %q, want feature-build follow-up sender", msg.SenderID)
	}
	if msg.Metadata[tools.MetaOriginChannel] != "telegram" {
		t.Fatalf("origin channel = %q", msg.Metadata[tools.MetaOriginChannel])
	}
	if msg.Metadata[tools.MetaOriginPeerKind] != "group" {
		t.Fatalf("origin peer_kind = %q", msg.Metadata[tools.MetaOriginPeerKind])
	}
	if msg.Metadata[tools.MetaOriginLocalKey] != req.LocalKey {
		t.Fatalf("origin local_key = %q, want %q", msg.Metadata[tools.MetaOriginLocalKey], req.LocalKey)
	}
	if msg.Metadata[tools.MetaOriginSessionKey] != followup.SessionKey {
		t.Fatalf("origin session_key = %q, want %q", msg.Metadata[tools.MetaOriginSessionKey], followup.SessionKey)
	}
	if !strings.Contains(msg.Content, "feature_detail") {
		t.Fatalf("follow-up content = %q, want feature_detail instruction", msg.Content)
	}
}

func newTestFeatureStore(t *testing.T) *featureStore {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "feature-requests.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = db.Close()
	})

	store := &featureStore{db: db}
	if err := store.migrate(); err != nil {
		t.Fatalf("migrate feature store: %v", err)
	}
	return store
}

func newTestFeatureFeature(t *testing.T) *FeatureRequestsFeature {
	t.Helper()
	return &FeatureRequestsFeature{
		store:  newTestFeatureStore(t),
		msgBus: bus.New(),
	}
}

func newTestFeatureRequest(title, tenantID string) *FeatureRequest {
	now := time.Now().UTC().Truncate(time.Second)
	return &FeatureRequest{
		ID:          uuid.New().String(),
		TenantID:    tenantID,
		Title:       title,
		Description: "test description",
		RequestedBy: "telegram/-100",
		Status:      StatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func newTestBuildRepo(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	for _, rel := range []string{
		"go.mod",
		filepath.Join("internal", "beta", ".keep"),
		filepath.Join("skills", "beta-feature", "SKILL.md"),
	} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	return root
}

func newFeaturePollTestContext(senderID, channel, chatID, localKey string) context.Context {
	ctx := context.Background()
	ctx = store.WithSenderID(ctx, senderID)
	ctx = tools.WithToolPeerKind(ctx, "group")
	ctx = tools.WithToolChannel(ctx, channel)
	ctx = tools.WithToolChatID(ctx, chatID)
	ctx = tools.WithToolLocalKey(ctx, localKey)
	return ctx
}

type stubTelegramPollCreator struct {
	pollID    string
	messageID int
	calls     int
	chatID    int64
	threadID  int
	question  string
	yesOption string
	noOption  string
	openFor   int
	err       error
}

func (s *stubTelegramPollCreator) CreateSoDauBaiPoll(_ context.Context, chatID int64, threadID int, question, yesOption, noOption string, openPeriodSeconds int) (string, int, error) {
	s.calls++
	s.chatID = chatID
	s.threadID = threadID
	s.question = question
	s.yesOption = yesOption
	s.noOption = noOption
	s.openFor = openPeriodSeconds
	if s.err != nil {
		return "", 0, s.err
	}
	return s.pollID, s.messageID, nil
}

type testActivatableBetaFeature struct {
	name string
}

func (f *testActivatableBetaFeature) Name() string { return f.name }

func (f *testActivatableBetaFeature) Init(beta.Deps) error { return nil }

type featureTestSystemConfigStore struct {
	values map[string]string
}

func newFeatureTestSystemConfigStore() *featureTestSystemConfigStore {
	return &featureTestSystemConfigStore{values: make(map[string]string)}
}

func (s *featureTestSystemConfigStore) Get(ctx context.Context, key string) (string, error) {
	value, ok := s.values[s.scopedKey(ctx, key)]
	if !ok {
		return "", fmt.Errorf("not found")
	}
	return value, nil
}

func (s *featureTestSystemConfigStore) Set(ctx context.Context, key, value string) error {
	s.values[s.scopedKey(ctx, key)] = value
	return nil
}

func (s *featureTestSystemConfigStore) Delete(ctx context.Context, key string) error {
	delete(s.values, s.scopedKey(ctx, key))
	return nil
}

func (s *featureTestSystemConfigStore) List(ctx context.Context) (map[string]string, error) {
	result := make(map[string]string)
	prefix := s.scopedKey(ctx, "")
	for scopedKey, value := range s.values {
		if strings.HasPrefix(scopedKey, prefix) {
			result[strings.TrimPrefix(scopedKey, prefix)] = value
		}
	}
	return result, nil
}

func (s *featureTestSystemConfigStore) scopedKey(ctx context.Context, key string) string {
	return store.TenantIDFromContext(ctx).String() + ":" + key
}
