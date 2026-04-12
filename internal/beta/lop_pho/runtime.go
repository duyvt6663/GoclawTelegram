package loppho

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	lopphopolldedupe "github.com/nextlevelbuilder/goclaw/internal/beta/lop_pho_poll_dedupe"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/classroles"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type openVoteInput struct {
	TenantID  string
	Channel   string
	ChatID    string
	ThreadID  int
	LocalKey  string
	TargetRaw string
	StartedBy telegramIdentity
	Source    string
}

type openVoteResult struct {
	Poll       *LopPhoPoll `json:"poll,omitempty"`
	Reused     bool        `json:"reused,omitempty"`
	Suppressed bool        `json:"suppressed,omitempty"`
	Pending    bool        `json:"pending,omitempty"`
}

type voteCommand struct {
	feature *LopPhoFeature
}

func (c *voteCommand) Command() string { return voteCommandName }

func (c *voteCommand) Description() string {
	return "Open a lớp phó vote in this group"
}

func (c *voteCommand) EnabledForChannel(channel *telegramchannel.Channel) bool {
	if c == nil || c.feature == nil {
		return false
	}
	return c.feature.commandEnabledForChannel(channel)
}

func (c *voteCommand) Handle(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if c == nil || c.feature == nil || c.feature.store == nil {
		return false
	}
	if !cmdCtx.IsGroup {
		cmdCtx.Reply(ctx, voteCommandName+" chỉ dùng trong group Telegram.")
		return true
	}

	actor := parseActorIdentity(cmdCtx.SenderID)
	if actor.SenderID == "" || !classroles.CanActAsLopTruong(ctx, actor.SenderID) {
		cmdCtx.Reply(ctx, "Chỉ lớp trưởng hoặc lớp phó mới được mở vote_lp.")
		return true
	}

	parts := strings.Fields(cmdCtx.Text)
	if len(parts) < 2 {
		cmdCtx.Reply(ctx, "Cách dùng: /vote_lp @user")
		return true
	}

	result, err := c.feature.openVotePoll(ctx, openVoteInput{
		TenantID:  tenantKey(channel.TenantID()),
		Channel:   channel.Name(),
		ChatID:    cmdCtx.ChatIDStr,
		ThreadID:  normalizeThreadID(cmdCtx.MessageThreadID),
		LocalKey:  cmdCtx.LocalKey,
		TargetRaw: strings.Join(parts[1:], " "),
		StartedBy: actor,
		Source:    voteOpenSourceCommand,
	})
	if err != nil {
		cmdCtx.Reply(ctx, err.Error())
		return true
	}
	if result.Suppressed {
		return true
	}
	if result.Reused && result.Poll != nil {
		cmdCtx.Reply(ctx, fmt.Sprintf("Đã có poll đang mở cho %s rồi. Hiện tại: %d bầu, %d hạ hạnh kiểm.", result.Poll.TargetLabel, result.Poll.BauVotes, result.Poll.HaHanhKiemVotes))
		return true
	}
	if result.Poll == nil {
		return true
	}
	cmdCtx.Reply(ctx, fmt.Sprintf("Đã mở vote lớp phó cho %s. Hiện tại: %d bầu, %d hạ hạnh kiểm.", result.Poll.TargetLabel, result.Poll.BauVotes, result.Poll.HaHanhKiemVotes))
	return true
}

func (f *LopPhoFeature) openVotePoll(ctx context.Context, input openVoteInput) (*openVoteResult, error) {
	if f == nil || f.store == nil {
		return nil, fmt.Errorf("lớp phó feature is unavailable")
	}

	input.TenantID = strings.TrimSpace(input.TenantID)
	input.Channel = strings.TrimSpace(input.Channel)
	input.ChatID = strings.TrimSpace(input.ChatID)
	input.ThreadID = normalizeThreadID(input.ThreadID)
	input.LocalKey = strings.TrimSpace(input.LocalKey)
	input.Source = strings.TrimSpace(input.Source)
	if input.LocalKey == "" {
		input.LocalKey = composeLocalKey(input.ChatID, input.ThreadID)
	}
	if input.TenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	if input.Channel == "" {
		return nil, fmt.Errorf("channel is required")
	}
	if input.ChatID == "" {
		return nil, fmt.Errorf("chat_id is required")
	}

	target, err := f.resolveTargetIdentity(ctx, input.Channel, input.TargetRaw)
	if err != nil {
		return nil, err
	}
	active, err := f.store.getActivePollByTarget(input.TenantID, input.ChatID, input.ThreadID, target)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return &openVoteResult{Poll: active, Reused: true}, nil
	}

	var claim *lopphopolldedupe.DedupeClaim
	if f.dedupe != nil {
		decision, err := f.dedupe.BeginClaim(ctx, lopphopolldedupe.ClaimRequest{
			TenantID:       input.TenantID,
			Channel:        input.Channel,
			ChatID:         input.ChatID,
			ThreadID:       input.ThreadID,
			LocalKey:       input.LocalKey,
			TargetKey:      stableVoteTargetKey(input.TargetRaw, target),
			TargetLabel:    target.Label,
			StartedByID:    firstNonEmpty(input.StartedBy.UserID, input.StartedBy.SenderID),
			StartedByLabel: input.StartedBy.Label,
			Source:         firstNonEmpty(input.Source, voteOpenSourceUnknown),
		})
		if err != nil {
			return nil, fmt.Errorf("claim lớp phó poll dedupe: %w", err)
		}
		if decision != nil && !decision.Acquired {
			result, err := f.resolveSuppressedOpenVote(input.TenantID, input.ChatID, input.ThreadID, target, decision)
			if err != nil {
				return nil, err
			}
			return result, nil
		}
		if decision != nil {
			claim = decision.Claim
		}
	}

	controller := f.resolvePollController(input.Channel)
	if controller == nil {
		f.failOpenVoteClaim(claim, fmt.Errorf("channel %q does not support Telegram polls", input.Channel))
		return nil, fmt.Errorf("channel %q does not support Telegram polls", input.Channel)
	}
	chatID, err := strconv.ParseInt(input.ChatID, 10, 64)
	if err != nil {
		f.failOpenVoteClaim(claim, fmt.Errorf("invalid Telegram chat_id %q", input.ChatID))
		return nil, fmt.Errorf("invalid Telegram chat_id %q", input.ChatID)
	}

	pollID, messageID, err := controller.CreatePoll(
		ctx,
		chatID,
		input.ThreadID,
		buildVoteQuestion(target.Label),
		[]string{pollOptionBau, pollOptionHaHanhKiem},
		pollOpenPeriodSeconds,
	)
	if err != nil {
		f.failOpenVoteClaim(claim, err)
		return nil, fmt.Errorf("create Telegram poll: %w", err)
	}

	poll, err := f.store.createPoll(PollCreate{
		TenantID:  input.TenantID,
		PollID:    pollID,
		Channel:   input.Channel,
		ChatID:    input.ChatID,
		ThreadID:  input.ThreadID,
		LocalKey:  input.LocalKey,
		MessageID: messageID,
		Target:    target,
		StartedBy: input.StartedBy,
	})
	if err != nil {
		f.failOpenVoteClaim(claim, err)
		return nil, err
	}
	f.completeOpenVoteClaim(claim, poll.PollID, poll.MessageID)

	slog.Info("beta lop pho poll created",
		"poll_id", poll.PollID,
		"channel", poll.Channel,
		"chat_id", poll.ChatID,
		"thread_id", poll.ThreadID,
		"target", poll.TargetSenderID,
		"started_by", poll.StartedByLabel,
	)
	return &openVoteResult{Poll: poll}, nil
}

func (f *LopPhoFeature) resolveSuppressedOpenVote(tenantID, chatID string, threadID int, target telegramIdentity, decision *lopphopolldedupe.ClaimDecision) (*openVoteResult, error) {
	if decision == nil {
		return &openVoteResult{Suppressed: true, Pending: true}, nil
	}

	result := &openVoteResult{
		Suppressed: decision.Duplicate,
		Pending:    decision.Pending,
	}

	if decision.Claim != nil && strings.TrimSpace(decision.Claim.PollID) != "" {
		poll, err := f.store.getPollByPollID(tenantID, decision.Claim.PollID)
		switch {
		case err == nil:
			result.Poll = poll
			result.Reused = poll != nil
			result.Pending = false
			return result, nil
		case !errors.Is(err, errLopPhoPollNotFound):
			return nil, err
		}
	}

	active, err := f.store.getActivePollByTarget(tenantID, chatID, threadID, target)
	if err != nil {
		return nil, err
	}
	if active != nil {
		result.Poll = active
		result.Reused = true
		result.Pending = false
	}
	return result, nil
}

func (f *LopPhoFeature) failOpenVoteClaim(claim *lopphopolldedupe.DedupeClaim, cause error) {
	if f == nil || f.dedupe == nil || claim == nil {
		return
	}
	if err := f.dedupe.FailClaim(context.Background(), claim.ID, claim.OwnerToken, firstNonEmpty(errorString(cause), "open vote failed")); err != nil {
		slog.Warn("beta lop pho poll dedupe fail mark failed", "claim_id", claim.ID, "error", err)
	}
}

func (f *LopPhoFeature) completeOpenVoteClaim(claim *lopphopolldedupe.DedupeClaim, pollID string, messageID int) {
	if f == nil || f.dedupe == nil || claim == nil {
		return
	}
	if err := f.dedupe.CompleteClaim(context.Background(), claim.ID, claim.OwnerToken, pollID, messageID); err != nil {
		slog.Warn("beta lop pho poll dedupe complete mark failed", "claim_id", claim.ID, "poll_id", pollID, "error", err)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (f *LopPhoFeature) statusSnapshot(tenantID string, filter PollFilter) (StatusSnapshot, error) {
	roles, err := f.store.listRoles(strings.TrimSpace(tenantID))
	if err != nil {
		return StatusSnapshot{}, err
	}
	polls, err := f.store.listPolls(strings.TrimSpace(tenantID), filter)
	if err != nil {
		return StatusSnapshot{}, err
	}
	return StatusSnapshot{
		Roles: roles,
		Polls: polls,
	}, nil
}

func (f *LopPhoFeature) resolveTargetIdentity(ctx context.Context, channelName, rawTarget string) (telegramIdentity, error) {
	rawTarget = strings.TrimSpace(rawTarget)
	if rawTarget == "" {
		return telegramIdentity{}, fmt.Errorf("target is required")
	}
	if f != nil && f.contacts != nil {
		if resolved, ok := f.lookupContactIdentity(ctx, channelName, rawTarget); ok {
			return resolved, nil
		}
	}
	if ident, ok := parseCanonicalTelegramIdentity(rawTarget); ok {
		return ident, nil
	}
	return telegramIdentity{}, fmt.Errorf("could not resolve %q to a Telegram user", rawTarget)
}

func (f *LopPhoFeature) lookupContactIdentity(ctx context.Context, channelName, rawTarget string) (telegramIdentity, bool) {
	if f == nil || f.contacts == nil {
		return telegramIdentity{}, false
	}
	search := normalizeTelegramTargetLookup(rawTarget)
	if search == "" {
		return telegramIdentity{}, false
	}

	results, err := f.contacts.ListContacts(ctx, store.ContactListOpts{
		Search:      search,
		ChannelType: "telegram",
		ContactType: "user",
		Limit:       20,
	})
	if err != nil || len(results) == 0 {
		return telegramIdentity{}, false
	}

	bestIdx := -1
	bestScore := -1
	ambiguous := false
	for i := range results {
		score := scoreTelegramTargetCandidate(results[i], channelName, rawTarget)
		if score < 0 {
			continue
		}
		if score > bestScore {
			bestIdx = i
			bestScore = score
			ambiguous = false
			continue
		}
		if score == bestScore {
			ambiguous = true
		}
	}
	if bestIdx < 0 || bestScore < 70 || ambiguous {
		return telegramIdentity{}, false
	}
	ident := contactToTelegramIdentity(results[bestIdx])
	return ident, ident.SenderID != ""
}

func (f *LopPhoFeature) handlePollAnswer(event bus.Event) {
	if f == nil || f.store == nil {
		return
	}
	if event.Name != bus.TopicTelegramPollAnswer {
		return
	}

	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return
	}
	pollID, _ := payload["poll_id"].(string)
	voterID, _ := payload["voter_id"].(string)
	if strings.TrimSpace(pollID) == "" || strings.TrimSpace(voterID) == "" {
		return
	}

	choice := pollChoiceFromOptionIDs(extractOptionIDs(payload["option_ids"]))
	voter := parseActorIdentity(voterID)
	if voter.UserID == "" {
		voter.UserID = firstNonEmpty(userIDFromSenderID(voter.SenderID), voter.SenderID)
	}

	f.pollMu.Lock()
	poll, resultChoice, err := f.store.recordVote(tenantKey(event.TenantID), pollID, voter, choice)
	f.pollMu.Unlock()
	if err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "not found") {
			slog.Warn("beta lop pho vote record failed", "poll_id", pollID, "error", err)
		}
		return
	}
	if poll == nil {
		return
	}

	slog.Debug("beta lop pho vote recorded",
		"poll_id", poll.PollID,
		"target", poll.TargetSenderID,
		"bau_votes", poll.BauVotes,
		"ha_hanh_kiem_votes", poll.HaHanhKiemVotes,
	)

	if resultChoice != "" {
		f.resolvePollResult(context.Background(), poll, resultChoice)
	}
}

func (f *LopPhoFeature) handlePollClosed(event bus.Event) {
	if f == nil || f.store == nil {
		return
	}
	if event.Name != bus.TopicTelegramPollClosed {
		return
	}
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return
	}
	pollID, _ := payload["poll_id"].(string)
	if strings.TrimSpace(pollID) == "" {
		return
	}

	f.pollMu.Lock()
	_, err := f.store.markClosed(tenantKey(event.TenantID), pollID)
	f.pollMu.Unlock()
	if err != nil {
		slog.Warn("beta lop pho close mark failed", "poll_id", pollID, "error", err)
	}
}

func (f *LopPhoFeature) resolvePollResult(ctx context.Context, poll *LopPhoPoll, resultChoice string) {
	if poll == nil {
		return
	}

	switch resultChoice {
	case voteChoiceBau:
		role, created, err := f.store.grantRole(poll.TenantID, telegramIdentity{
			UserID:   poll.TargetUserID,
			SenderID: poll.TargetSenderID,
			Label:    poll.TargetLabel,
		}, poll.PollID)
		if err != nil {
			slog.Warn("beta lop pho role grant failed", "poll_id", poll.PollID, "error", err)
		} else {
			slog.Info("beta lop pho role granted", "poll_id", poll.PollID, "target", poll.TargetSenderID, "created", created, "role_id", role.ID)
		}
		if err := f.stopPoll(ctx, poll); err != nil {
			slog.Warn("beta lop pho stop poll failed", "poll_id", poll.PollID, "error", err)
		}
		if err := f.sendPollAnnouncement(poll, "chúc mừng bạn đã có bầu\n\n"+poll.TargetLabel+" giờ là lớp phó.", f.congratsStickerPath()); err != nil {
			slog.Warn("beta lop pho congratulation failed", "poll_id", poll.PollID, "error", err)
		}
	case voteChoiceHaHanhKiem:
		muteErr := f.applyDisciplineMute(ctx, poll)
		if muteErr != nil {
			slog.Warn("beta lop pho mute failed", "poll_id", poll.PollID, "target", poll.TargetSenderID, "error", muteErr)
		} else {
			slog.Info("beta lop pho mute applied", "poll_id", poll.PollID, "target", poll.TargetSenderID, "duration_seconds", muteDuration)
		}
		if err := f.stopPoll(ctx, poll); err != nil {
			slog.Warn("beta lop pho stop poll failed", "poll_id", poll.PollID, "error", err)
		}
		message := fmt.Sprintf("%s bị mute 2 giờ vì đủ %d phiếu hạ hạnh kiểm.", poll.TargetLabel, haHanhKiemVoteThreshold)
		if muteErr != nil {
			message = fmt.Sprintf("%s đủ %d phiếu hạ hạnh kiểm nhưng bot không mute được: %v", poll.TargetLabel, haHanhKiemVoteThreshold, muteErr)
		}
		if err := f.sendPollAnnouncement(poll, message, ""); err != nil {
			slog.Warn("beta lop pho discipline announcement failed", "poll_id", poll.PollID, "error", err)
		}
	}
}

func (f *LopPhoFeature) stopPoll(ctx context.Context, poll *LopPhoPoll) error {
	if poll == nil {
		return nil
	}
	controller := f.resolvePollController(poll.Channel)
	if controller == nil {
		return nil
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(poll.ChatID), 10, 64)
	if err != nil {
		return err
	}
	return controller.StopPoll(ctx, chatID, poll.MessageID)
}

func (f *LopPhoFeature) applyDisciplineMute(ctx context.Context, poll *LopPhoPoll) error {
	if poll == nil {
		return fmt.Errorf("poll is required")
	}
	moderator := f.resolveModerator(poll.Channel)
	if moderator == nil {
		return fmt.Errorf("channel does not support mute")
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(poll.ChatID), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid chat_id %q", poll.ChatID)
	}
	userID := firstNonEmpty(strings.TrimSpace(poll.TargetUserID), userIDFromSenderID(poll.TargetSenderID))
	if userID == "" {
		return fmt.Errorf("target does not have a numeric Telegram id")
	}
	telegramUserID, err := strconv.ParseInt(userID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid Telegram user id %q", userID)
	}
	return moderator.MuteMember(ctx, chatID, telegramUserID, 2*time.Hour)
}

func (f *LopPhoFeature) sendPollAnnouncement(poll *LopPhoPoll, text, stickerPath string) error {
	if f == nil || f.msgBus == nil || poll == nil {
		return fmt.Errorf("message bus is unavailable")
	}
	msg := bus.OutboundMessage{
		Channel:  poll.Channel,
		ChatID:   poll.ChatID,
		Content:  text,
		Metadata: outboundMeta(poll),
	}
	if strings.TrimSpace(stickerPath) != "" {
		msg.Media = []bus.MediaAttachment{{
			URL:         stickerPath,
			ContentType: "application/x-tgsticker",
		}}
	}
	f.msgBus.PublishOutbound(msg)
	return nil
}

func outboundMeta(poll *LopPhoPoll) map[string]string {
	if poll == nil {
		return nil
	}
	meta := map[string]string{}
	if localKey := strings.TrimSpace(poll.LocalKey); localKey != "" && localKey != poll.ChatID {
		meta["local_key"] = localKey
	}
	if threadID := normalizeThreadID(poll.ThreadID); threadID > 0 {
		meta["message_thread_id"] = strconv.Itoa(threadID)
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

func (f *LopPhoFeature) congratsStickerPath() string {
	if f == nil {
		return ""
	}
	candidates := []string{defaultCongratsStickerRelPath}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		path := candidate
		if f.workspace != "" && !filepath.IsAbs(path) {
			path = filepath.Join(f.workspace, candidate)
		}
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}
