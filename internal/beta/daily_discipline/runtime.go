package dailydiscipline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
)

var (
	yesNoOptions    = []string{"Yes", "No"}
	activityOptions = []string{"None", "Gym", "Run", "Sport"}
)

type summaryStats struct {
	LocalDate      string
	Participants   int
	EarlyWake      int
	DisciplineDone int
	ActivityTotal  int
	GymCount       int
	RunCount       int
	SportCount     int
	Responses      []DailyResponse
	Streaks        []streakEntry
}

type streakEntry struct {
	Label string `json:"label"`
	Days  int    `json:"days"`
}

type ResponseView struct {
	DailyResponse
	DisciplineStreak int `json:"discipline_streak,omitempty"`
}

type ConfigStatus struct {
	Config            SurveyConfig   `json:"config"`
	LocalDate         string         `json:"local_date"`
	SurveyPosted      bool           `json:"survey_posted"`
	SummaryPosted     bool           `json:"summary_posted"`
	Participants      int            `json:"participants"`
	EarlyWake         int            `json:"early_wake"`
	DisciplineDone    int            `json:"discipline_done"`
	ActivityTotal     int            `json:"activity_total"`
	ActivityBreakdown map[string]int `json:"activity_breakdown"`
}

type disciplineCommand struct {
	feature *DailyDisciplineFeature
}

func (c *disciplineCommand) Command() string { return "/discipline" }

func (c *disciplineCommand) Description() string {
	return "Submit detailed discipline answers"
}

func (c *disciplineCommand) Handle(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if c == nil || c.feature == nil || c.feature.store == nil {
		return false
	}

	args := strings.Fields(cmdCtx.Text)
	if len(args) == 0 {
		return false
	}
	args = args[1:]

	message, err := c.feature.handleCommandSubmission(ctx, tenantKey(channel.TenantID()), channel.Name(), cmdCtx, args)
	if err != nil {
		cmdCtx.Reply(ctx, err.Error())
		return true
	}
	cmdCtx.Reply(ctx, message)
	return true
}

func (f *DailyDisciplineFeature) handleCommandSubmission(ctx context.Context, tenantID, channelName string, cmdCtx telegramchannel.DynamicCommandContext, args []string) (string, error) {
	cfg, remaining, err := f.resolveCommandConfig(tenantID, channelName, cmdCtx, args)
	if err != nil {
		return "", err
	}
	if cfg == nil {
		return "", fmt.Errorf("daily discipline is not configured here yet")
	}
	if !cmdCtx.IsGroup && !cfg.DMDetailsEnabled {
		return "", fmt.Errorf("DM detail submissions are disabled for %s. Vote in the group polls instead.", cfg.Name)
	}
	if len(remaining) < 3 {
		return "", fmt.Errorf("Usage: /discipline [config_key] <wake yes/no> <discipline yes/no> <activity none/gym/run/sport> [note]")
	}

	wake, ok := normalizeYesNo(remaining[0])
	if !ok {
		return "", fmt.Errorf("wake must be yes or no")
	}
	discipline, ok := normalizeYesNo(remaining[1])
	if !ok {
		return "", fmt.Errorf("discipline must be yes or no")
	}
	activity, ok := normalizeActivity(remaining[2])
	if !ok {
		return "", fmt.Errorf("activity must be none, gym, run, or sport")
	}
	note := strings.TrimSpace(strings.Join(remaining[3:], " "))

	identity := parseUserIdentity(cmdCtx.SenderID)
	if identity.ID == "" {
		return "", fmt.Errorf("unable to resolve Telegram sender identity for this command")
	}

	localDate := cfg.localDate(time.Now().UTC())
	if _, err := f.submitDetailedResponse(ctx, cfg, localDate, identity, stringPtr(wake), stringPtr(discipline), stringPtr(activity), optionalString(note), "command"); err != nil {
		return "", err
	}

	message := fmt.Sprintf(
		"Recorded %s for %s on %s: wake=%s, discipline=%s, activity=%s.",
		cfg.Name, identity.Label, localDate, statusLabel(wake), statusLabel(discipline), activityLabel(activity),
	)
	if note != "" {
		message += " Note saved."
	}
	return message, nil
}

func (f *DailyDisciplineFeature) resolveCommandConfig(tenantID, channelName string, cmdCtx telegramchannel.DynamicCommandContext, args []string) (*SurveyConfig, []string, error) {
	remaining := args
	if len(remaining) > 0 {
		if _, ok := normalizeYesNo(remaining[0]); !ok {
			cfg, err := f.store.getConfigByKey(tenantID, remaining[0])
			if err == nil {
				if !cfg.Enabled {
					return nil, nil, fmt.Errorf("daily discipline config %q is disabled", cfg.Key)
				}
				return cfg, remaining[1:], nil
			}
			if !errors.Is(err, errSurveyConfigNotFound) {
				return nil, nil, err
			}
		}
	}

	if cmdCtx.IsGroup {
		chatID, threadID := parseCompositeChatTarget(cmdCtx.LocalKey)
		if chatID == "" {
			chatID = strings.TrimSpace(cmdCtx.ChatIDStr)
		}
		if threadID == 0 {
			threadID = cmdCtx.MessageThreadID
		}
		cfg, err := f.store.getConfigByTarget(tenantID, channelName, chatID, threadID)
		if err != nil {
			if errors.Is(err, errSurveyConfigNotFound) {
				return nil, nil, fmt.Errorf("No daily discipline config is set for this group yet.")
			}
			return nil, nil, err
		}
		if !cfg.Enabled {
			return nil, nil, fmt.Errorf("Daily discipline is disabled for this group.")
		}
		return cfg, remaining, nil
	}

	configs, err := f.store.listConfigs(tenantID)
	if err != nil {
		return nil, nil, err
	}

	candidates := make([]SurveyConfig, 0, len(configs))
	for _, cfg := range configs {
		if cfg.Enabled && cfg.DMDetailsEnabled {
			candidates = append(candidates, cfg)
		}
	}
	if len(candidates) == 0 {
		for _, cfg := range configs {
			if cfg.Enabled {
				candidates = append(candidates, cfg)
			}
		}
	}
	if len(candidates) == 1 {
		return &candidates[0], remaining, nil
	}
	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("No enabled daily discipline configs are available for DM detail submission.")
	}
	return nil, nil, fmt.Errorf("Multiple daily discipline configs are enabled. Use /discipline <config_key> yes yes run [note].")
}

func (f *DailyDisciplineFeature) runScheduler(ctx context.Context) {
	defer close(f.schedulerDone)

	f.runDueChecks(ctx, time.Now().UTC())

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			f.runDueChecks(ctx, now.UTC())
		}
	}
}

func (f *DailyDisciplineFeature) runDueChecks(ctx context.Context, now time.Time) {
	configs, err := f.store.listEnabledConfigs()
	if err != nil {
		slog.Warn("beta daily discipline: failed to list configs", "error", err)
		return
	}

	for i := range configs {
		cfg := configs[i]
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := f.runDueConfig(runCtx, &cfg, now); err != nil {
			slog.Warn("beta daily discipline: due check failed", "config", cfg.Key, "error", err)
		}
		cancel()
	}
}

func (f *DailyDisciplineFeature) runDueConfig(ctx context.Context, cfg *SurveyConfig, now time.Time) error {
	localNow := cfg.localNow(now)
	minuteOfDay := localNow.Hour()*60 + localNow.Minute()
	localDate := localNow.Format("2006-01-02")

	surveyStart, err := parseTimeOfDay(cfg.SurveyWindowStart)
	if err != nil {
		return fmt.Errorf("invalid survey_window_start for %s: %w", cfg.Key, err)
	}
	surveyEnd, err := parseTimeOfDay(cfg.SurveyWindowEnd)
	if err != nil {
		return fmt.Errorf("invalid survey_window_end for %s: %w", cfg.Key, err)
	}
	if withinWindow(minuteOfDay, surveyStart, surveyEnd) {
		if err := f.ensureSurveyPosted(ctx, cfg, localDate); err != nil {
			return err
		}
	}

	summaryMinute, err := parseTimeOfDay(cfg.SummaryTime)
	if err != nil {
		return fmt.Errorf("invalid summary_time for %s: %w", cfg.Key, err)
	}
	if minuteOfDay >= summaryMinute {
		if err := f.ensureSummaryPosted(ctx, cfg, localDate); err != nil {
			return err
		}
	}

	return nil
}

func (f *DailyDisciplineFeature) ensureSurveyPosted(ctx context.Context, cfg *SurveyConfig, localDate string) error {
	f.postMu.Lock()
	defer f.postMu.Unlock()

	posts, err := f.store.listPostsForDate(cfg.TenantID, cfg.ID, localDate)
	if err != nil {
		return err
	}
	posted := make(map[string]SurveyPost, len(posts))
	for _, post := range posts {
		posted[post.PostKind] = post
	}

	if _, ok := posted[postKindSurveyHeader]; !ok {
		if err := f.sendTextToConfig(ctx, cfg, buildSurveyHeaderText(cfg, localDate)); err != nil {
			return err
		}
		if err := f.store.recordPost(&SurveyPost{
			TenantID:  cfg.TenantID,
			ConfigID:  cfg.ID,
			LocalDate: localDate,
			PostKind:  postKindSurveyHeader,
		}); err != nil {
			return err
		}
	}

	controller := f.resolvePollController(cfg.Channel)
	if controller == nil {
		return fmt.Errorf("channel %q does not support Telegram polls", cfg.Channel)
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(cfg.ChatID), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid Telegram chat_id %q: %w", cfg.ChatID, err)
	}

	for _, spec := range []struct {
		postKind string
		question string
		options  []string
	}{
		{postKind: postKindWakePoll, question: cfg.WakeQuestion, options: yesNoOptions},
		{postKind: postKindDisciplinePoll, question: cfg.DisciplineQuestion, options: yesNoOptions},
		{postKind: postKindActivityPoll, question: cfg.ActivityQuestion, options: activityOptions},
	} {
		if _, ok := posted[spec.postKind]; ok {
			continue
		}
		pollID, messageID, err := controller.CreatePoll(ctx, chatID, cfg.ThreadID, spec.question, spec.options, 0)
		if err != nil {
			return fmt.Errorf("post %s poll: %w", spec.postKind, err)
		}
		if err := f.store.recordPost(&SurveyPost{
			TenantID:  cfg.TenantID,
			ConfigID:  cfg.ID,
			LocalDate: localDate,
			PostKind:  spec.postKind,
			PollID:    pollID,
			MessageID: messageID,
		}); err != nil {
			return err
		}
		slog.Info("beta daily discipline: survey poll posted", "config", cfg.Key, "kind", spec.postKind, "date", localDate, "poll_id", pollID)
	}

	return nil
}

func (f *DailyDisciplineFeature) ensureSummaryPosted(ctx context.Context, cfg *SurveyConfig, localDate string) error {
	f.postMu.Lock()
	defer f.postMu.Unlock()

	alreadyPosted, err := f.store.hasPost(cfg.TenantID, cfg.ID, localDate, postKindSummary)
	if err != nil {
		return err
	}
	if alreadyPosted {
		return nil
	}

	if err := f.closeSurveyPolls(ctx, cfg, localDate); err != nil {
		slog.Warn("beta daily discipline: closing survey polls failed", "config", cfg.Key, "date", localDate, "error", err)
	}

	responses, err := f.store.listResponses(cfg.TenantID, cfg.ID, localDate)
	if err != nil {
		return err
	}
	stats, err := f.buildSummaryStats(cfg, localDate, responses)
	if err != nil {
		return err
	}
	if err := f.sendTextToConfig(ctx, cfg, buildSummaryText(cfg, stats)); err != nil {
		return err
	}
	return f.store.recordPost(&SurveyPost{
		TenantID:  cfg.TenantID,
		ConfigID:  cfg.ID,
		LocalDate: localDate,
		PostKind:  postKindSummary,
	})
}

func (f *DailyDisciplineFeature) closeSurveyPolls(ctx context.Context, cfg *SurveyConfig, localDate string) error {
	controller := f.resolvePollController(cfg.Channel)
	if controller == nil {
		return nil
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(cfg.ChatID), 10, 64)
	if err != nil {
		return err
	}
	posts, err := f.store.listPostsForDate(cfg.TenantID, cfg.ID, localDate)
	if err != nil {
		return err
	}
	for _, post := range posts {
		switch post.PostKind {
		case postKindWakePoll, postKindDisciplinePoll, postKindActivityPoll:
			if err := controller.StopPoll(ctx, chatID, post.MessageID); err != nil {
				slog.Debug("beta daily discipline: stop poll failed", "poll_id", post.PollID, "error", err)
			}
		}
	}
	return nil
}

func (f *DailyDisciplineFeature) sendTextToConfig(ctx context.Context, cfg *SurveyConfig, text string) error {
	if f == nil || f.channelMgr == nil {
		return fmt.Errorf("channel manager unavailable")
	}
	channel, ok := f.channelMgr.GetChannel(cfg.Channel)
	if !ok {
		return fmt.Errorf("channel %q not found", cfg.Channel)
	}
	metadata := map[string]string{}
	if cfg.ThreadID > 0 {
		metadata["message_thread_id"] = strconv.Itoa(cfg.ThreadID)
		metadata["local_key"] = composeLocalKey(cfg.ChatID, cfg.ThreadID)
	}
	return channel.Send(ctx, bus.OutboundMessage{
		Channel:  cfg.Channel,
		ChatID:   cfg.ChatID,
		Content:  text,
		Metadata: metadata,
	})
}

func (f *DailyDisciplineFeature) handlePollAnswer(event bus.Event) {
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

	post, err := f.store.getPostByPollID(tenantKey(event.TenantID), pollID)
	if err != nil {
		return
	}

	optionIDs := extractOptionIDs(payload["option_ids"])
	identity := parseUserIdentity(voterID)
	if identity.ID == "" {
		return
	}

	patch := responsePatch{
		UserID:    identity.ID,
		UserLabel: identity.Label,
		Source:    "poll",
	}

	switch post.PostKind {
	case postKindWakePoll:
		value := wakeStatusFromOptions(optionIDs)
		patch.WakeStatus = &value
	case postKindDisciplinePoll:
		value := wakeStatusFromOptions(optionIDs)
		patch.DisciplineStatus = &value
	case postKindActivityPoll:
		value := activityStatusFromOptions(optionIDs)
		patch.ActivityStatus = &value
	default:
		return
	}

	if _, err := f.store.upsertResponse(post.TenantID, post.ConfigID, post.LocalDate, patch); err != nil {
		slog.Warn("beta daily discipline: failed to record poll answer", "poll_id", pollID, "error", err)
	}
}

func (f *DailyDisciplineFeature) submitDetailedResponse(_ context.Context, cfg *SurveyConfig, localDate string, identity userIdentity, wake, discipline, activity, note *string, source string) (*DailyResponse, error) {
	if cfg == nil {
		return nil, fmt.Errorf("survey config is required")
	}
	if identity.ID == "" {
		return nil, fmt.Errorf("user identity is required")
	}
	patch := responsePatch{
		UserID:    identity.ID,
		UserLabel: identity.Label,
		Source:    source,
	}
	updatedFields := 0
	if wake != nil {
		patch.WakeStatus = wake
		updatedFields++
	}
	if discipline != nil {
		patch.DisciplineStatus = discipline
		updatedFields++
	}
	if activity != nil {
		patch.ActivityStatus = activity
		updatedFields++
	}
	if note != nil {
		patch.Note = note
		if strings.TrimSpace(*note) != "" {
			updatedFields++
		}
	}
	if updatedFields == 0 {
		return nil, fmt.Errorf("at least one discipline field is required")
	}
	return f.store.upsertResponse(cfg.TenantID, cfg.ID, localDate, patch)
}

func (f *DailyDisciplineFeature) buildSummaryStats(cfg *SurveyConfig, localDate string, responses []DailyResponse) (summaryStats, error) {
	stats := summaryStats{
		LocalDate: localDate,
		Responses: responses,
	}

	for _, response := range responses {
		stats.Participants++
		if response.WakeStatus == statusYes {
			stats.EarlyWake++
		}
		if response.DisciplineStatus == statusYes {
			stats.DisciplineDone++
		}
		switch response.ActivityStatus {
		case activityGym:
			stats.ActivityTotal++
			stats.GymCount++
		case activityRun:
			stats.ActivityTotal++
			stats.RunCount++
		case activitySport:
			stats.ActivityTotal++
			stats.SportCount++
		}
	}

	if cfg.StreaksEnabled {
		for _, response := range responses {
			if response.DisciplineStatus != statusYes {
				continue
			}
			days, err := f.currentStreak(cfg.TenantID, cfg.ID, response.UserID, localDate)
			if err != nil || days <= 0 {
				continue
			}
			label := response.UserLabel
			if label == "" {
				label = response.UserID
			}
			stats.Streaks = append(stats.Streaks, streakEntry{Label: label, Days: days})
		}
		sort.Slice(stats.Streaks, func(i, j int) bool {
			if stats.Streaks[i].Days == stats.Streaks[j].Days {
				return stats.Streaks[i].Label < stats.Streaks[j].Label
			}
			return stats.Streaks[i].Days > stats.Streaks[j].Days
		})
	}

	return stats, nil
}

func (f *DailyDisciplineFeature) currentStreak(tenantID, configID, userID, localDate string) (int, error) {
	dates, err := f.store.listDisciplineDays(tenantID, configID, userID)
	if err != nil {
		return 0, err
	}
	if len(dates) == 0 {
		return 0, nil
	}

	expected, err := parseISODate(localDate)
	if err != nil {
		return 0, err
	}
	streak := 0
	for _, dateStr := range dates {
		current, err := parseISODate(dateStr)
		if err != nil {
			continue
		}
		switch {
		case current.After(expected):
			continue
		case current.Equal(expected):
			streak++
			expected = expected.AddDate(0, 0, -1)
		default:
			return streak, nil
		}
	}
	return streak, nil
}

func (f *DailyDisciplineFeature) statusForConfig(cfg *SurveyConfig, localDate string) (ConfigStatus, error) {
	posts, err := f.store.listPostsForDate(cfg.TenantID, cfg.ID, localDate)
	if err != nil {
		return ConfigStatus{}, err
	}
	postedKinds := make(map[string]bool, len(posts))
	for _, post := range posts {
		postedKinds[post.PostKind] = true
	}

	responses, err := f.store.listResponses(cfg.TenantID, cfg.ID, localDate)
	if err != nil {
		return ConfigStatus{}, err
	}
	stats, err := f.buildSummaryStats(cfg, localDate, responses)
	if err != nil {
		return ConfigStatus{}, err
	}

	return ConfigStatus{
		Config:         *cfg,
		LocalDate:      localDate,
		SurveyPosted:   postedKinds[postKindWakePoll] && postedKinds[postKindDisciplinePoll] && postedKinds[postKindActivityPoll],
		SummaryPosted:  postedKinds[postKindSummary],
		Participants:   stats.Participants,
		EarlyWake:      stats.EarlyWake,
		DisciplineDone: stats.DisciplineDone,
		ActivityTotal:  stats.ActivityTotal,
		ActivityBreakdown: map[string]int{
			activityGym:   stats.GymCount,
			activityRun:   stats.RunCount,
			activitySport: stats.SportCount,
		},
	}, nil
}

func (f *DailyDisciplineFeature) responsesForDate(cfg *SurveyConfig, localDate string) ([]ResponseView, error) {
	responses, err := f.store.listResponses(cfg.TenantID, cfg.ID, localDate)
	if err != nil {
		return nil, err
	}
	views := make([]ResponseView, 0, len(responses))
	for _, response := range responses {
		view := ResponseView{DailyResponse: response}
		if cfg.StreaksEnabled && response.DisciplineStatus == statusYes {
			if streak, err := f.currentStreak(cfg.TenantID, cfg.ID, response.UserID, localDate); err == nil {
				view.DisciplineStreak = streak
			}
		}
		views = append(views, view)
	}
	return views, nil
}

func (f *DailyDisciplineFeature) upsertConfigForTenant(tenantID string, cfg SurveyConfig) (*SurveyConfig, error) {
	cfg.TenantID = tenantID
	cfg = cfg.withDefaults()
	if cfg.Key == "" && cfg.Name != "" {
		cfg.Key = normalizeConfigKey(cfg.Name)
	}
	if cfg.Key == "" || !configKeyRe.MatchString(cfg.Key) {
		return nil, fmt.Errorf("config key must be lowercase alphanumeric with optional hyphens")
	}
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = cfg.Key
	}
	if strings.TrimSpace(cfg.Channel) == "" {
		return nil, fmt.Errorf("channel is required")
	}
	if strings.TrimSpace(cfg.ChatID) == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	if _, err := strconv.ParseInt(strings.TrimSpace(cfg.ChatID), 10, 64); err != nil {
		return nil, fmt.Errorf("chat_id must be a numeric Telegram chat id")
	}
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return nil, fmt.Errorf("invalid timezone %q", cfg.Timezone)
	}
	startMinute, err := parseTimeOfDay(cfg.SurveyWindowStart)
	if err != nil {
		return nil, fmt.Errorf("invalid survey_window_start: %w", err)
	}
	endMinute, err := parseTimeOfDay(cfg.SurveyWindowEnd)
	if err != nil {
		return nil, fmt.Errorf("invalid survey_window_end: %w", err)
	}
	summaryMinute, err := parseTimeOfDay(cfg.SummaryTime)
	if err != nil {
		return nil, fmt.Errorf("invalid summary_time: %w", err)
	}
	targetMinute, err := parseTimeOfDay(cfg.TargetWakeTime)
	if err != nil {
		return nil, fmt.Errorf("invalid target_wake_time: %w", err)
	}
	cfg.SurveyWindowStart = minutesToClock(startMinute)
	cfg.SurveyWindowEnd = minutesToClock(endMinute)
	cfg.SummaryTime = minutesToClock(summaryMinute)
	cfg.TargetWakeTime = minutesToClock(targetMinute)
	return f.store.upsertConfig(&cfg)
}

func buildSurveyHeaderText(cfg *SurveyConfig, localDate string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Daily discipline check-in - %s (%s)\n", cfg.Name, localDate)
	fmt.Fprintf(&b, "Target wake time: %s\n", cfg.TargetWakeTime)
	b.WriteString("Vote in the 3 polls below.\n")
	b.WriteString("Optional detail: /discipline yes yes run note")
	return b.String()
}

func buildSummaryText(cfg *SurveyConfig, stats summaryStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Daily discipline summary - %s (%s)\n", cfg.Name, stats.LocalDate)
	fmt.Fprintf(&b, "Participants: %d\n", stats.Participants)
	fmt.Fprintf(&b, "Early wake: %d\n", stats.EarlyWake)
	fmt.Fprintf(&b, "Discipline done: %d\n", stats.DisciplineDone)
	fmt.Fprintf(&b, "Physical activity: %d", stats.ActivityTotal)
	if stats.ActivityTotal > 0 {
		fmt.Fprintf(&b, " (gym %d, run %d, sport %d)", stats.GymCount, stats.RunCount, stats.SportCount)
	}
	b.WriteByte('\n')

	if stats.Participants == 0 {
		b.WriteString("No responses recorded yet.")
		return strings.TrimSpace(b.String())
	}

	if cfg.StreaksEnabled {
		if len(stats.Streaks) == 0 {
			b.WriteString("Active discipline streaks: none\n")
		} else if cfg.NamedResults {
			limit := len(stats.Streaks)
			if limit > 5 {
				limit = 5
			}
			b.WriteString("Top discipline streaks: ")
			for i := 0; i < limit; i++ {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s %dd", stats.Streaks[i].Label, stats.Streaks[i].Days)
			}
			b.WriteByte('\n')
		} else {
			b.WriteString("Active discipline streaks: ")
			b.WriteString(strconv.Itoa(len(stats.Streaks)))
			b.WriteString(" users\n")
		}
	}

	if cfg.NamedResults {
		b.WriteString("\nNamed results:\n")
		for _, response := range stats.Responses {
			label := response.UserLabel
			if label == "" {
				label = response.UserID
			}
			fmt.Fprintf(&b, "- %s: wake %s, discipline %s, activity %s", label, statusLabel(response.WakeStatus), statusLabel(response.DisciplineStatus), activityLabel(response.ActivityStatus))
			if note := strings.TrimSpace(response.Note); note != "" {
				fmt.Fprintf(&b, ", note: %s", truncateNote(note, 120))
			}
			b.WriteByte('\n')
		}
	}

	return strings.TrimSpace(b.String())
}

func wakeStatusFromOptions(optionIDs []int) string {
	if len(optionIDs) == 0 {
		return ""
	}
	if optionIDs[0] == 0 {
		return statusYes
	}
	return statusNo
}

func activityStatusFromOptions(optionIDs []int) string {
	if len(optionIDs) == 0 {
		return ""
	}
	switch optionIDs[0] {
	case 1:
		return activityGym
	case 2:
		return activityRun
	case 3:
		return activitySport
	default:
		return activityNone
	}
}

func extractOptionIDs(raw any) []int {
	switch v := raw.(type) {
	case []int:
		return append([]int(nil), v...)
	case []any:
		out := make([]int, 0, len(v))
		for _, item := range v {
			switch n := item.(type) {
			case int:
				out = append(out, n)
			case int32:
				out = append(out, int(n))
			case int64:
				out = append(out, int(n))
			case float64:
				out = append(out, int(n))
			}
		}
		return out
	default:
		return nil
	}
}

func truncateNote(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max || max <= 0 {
		return string(runes)
	}
	return string(runes[:max]) + "..."
}

func optionalString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func stringPtr(value string) *string { return &value }
