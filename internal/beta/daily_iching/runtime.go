package dailyiching

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

const featureName = "daily_iching"

type ConfigStatus struct {
	Config         DailyIChingConfig `json:"config"`
	Progress       ProgressState     `json:"progress"`
	LocalDate      string            `json:"local_date"`
	IndexReady     bool              `json:"index_ready"`
	SourceCount    int               `json:"source_count"`
	HexagramCount  int               `json:"hexagram_count"`
	TodayPosted    bool              `json:"today_posted"`
	Current        *HexagramStatus   `json:"current,omitempty"`
	Next           HexagramStatus    `json:"next"`
	CurrentSummary string            `json:"current_summary,omitempty"`
}

type HexagramStatus struct {
	Number        int    `json:"number"`
	Name          string `json:"name"`
	Title         string `json:"title"`
	SequenceIndex int    `json:"sequence_index"`
	Transition    string `json:"transition,omitempty"`
}

type transitionNote struct {
	Kind string
	Text string
}

type groundingContext struct {
	OverviewText      string
	PracticalText     string
	PhilosophicalText string
	Source            string
}

type lessonDelivery struct {
	Body          string
	Hexagram      hexagramMeta
	SequenceIndex int
	LocalDate     string
}

type ichingCommand struct {
	feature *DailyIChingFeature
}

func (c *ichingCommand) Command() string { return "/iching" }

func (c *ichingCommand) Description() string {
	return "Show status, post the next quẻ, or ask for a deeper explanation"
}

func (c *ichingCommand) Handle(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if c == nil || c.feature == nil || c.feature.store == nil {
		return false
	}

	args := strings.Fields(cmdCtx.Text)
	if len(args) == 0 {
		return false
	}
	args = args[1:]

	message, err := c.feature.handleCommand(ctx, tenantKey(channel.TenantID()), channel.Name(), cmdCtx, args)
	if err != nil {
		cmdCtx.Reply(ctx, err.Error())
		return true
	}
	if strings.TrimSpace(message) != "" {
		cmdCtx.Reply(ctx, message)
	}
	return true
}

func (f *DailyIChingFeature) handleCommand(ctx context.Context, tenantID, channelName string, cmdCtx telegramchannel.DynamicCommandContext, args []string) (string, error) {
	action := "status"
	if len(args) > 0 {
		switch normalizeComparableText(args[0]) {
		case "status", "trang thai":
			action = "status"
			args = args[1:]
		case "next", "tiep", "ke tiep":
			action = "next"
			args = args[1:]
		case "deeper", "deep", "sau hon":
			action = "deeper"
			args = args[1:]
		}
	}

	cfg, err := f.resolveCommandConfig(tenantID, channelName, cmdCtx, args)
	if err != nil {
		return "", err
	}
	if cfg == nil {
		return "", fmt.Errorf("daily i ching is not configured here yet")
	}

	localDate := cfg.localDate(time.Now().UTC())
	switch action {
	case "next":
		delivery, posted, err := f.postNextLesson(ctx, cfg, localDate, triggerKindCommand, true)
		if err != nil {
			return "", err
		}
		if !posted || delivery == nil {
			return "No lesson was posted.", nil
		}
		return fmt.Sprintf("Đã đăng quẻ %d %s cho %s.", delivery.Hexagram.Number, delivery.Hexagram.Name, cfg.Name), nil
	case "deeper":
		delivery, err := f.postDeeperLesson(ctx, cfg, localDate, triggerKindCommand)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Đã đăng phần đọc sâu cho quẻ %d %s.", delivery.Hexagram.Number, delivery.Hexagram.Name), nil
	default:
		status, err := f.statusForConfig(cfg, localDate)
		if err != nil {
			return "", err
		}
		return formatStatusText(status), nil
	}
}

func (f *DailyIChingFeature) resolveCommandConfig(tenantID, channelName string, cmdCtx telegramchannel.DynamicCommandContext, args []string) (*DailyIChingConfig, error) {
	if len(args) > 0 {
		cfg, err := f.store.getConfigByKey(tenantID, args[0])
		if err == nil {
			if !cfg.Enabled {
				return nil, fmt.Errorf("daily i ching config %q is disabled", cfg.Key)
			}
			return cfg, nil
		}
		if !errors.Is(err, errDailyIChingConfigNotFound) {
			return nil, err
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
			if errors.Is(err, errDailyIChingConfigNotFound) {
				return nil, fmt.Errorf("no daily i ching config is set for this group yet")
			}
			return nil, err
		}
		if !cfg.Enabled {
			return nil, fmt.Errorf("daily i ching is disabled for this group")
		}
		return cfg, nil
	}

	configs, err := f.store.listConfigs(tenantID)
	if err != nil {
		return nil, err
	}
	var enabled []DailyIChingConfig
	for _, cfg := range configs {
		if cfg.Enabled {
			enabled = append(enabled, cfg)
		}
	}
	if len(enabled) == 1 {
		return &enabled[0], nil
	}
	if len(enabled) == 0 {
		return nil, fmt.Errorf("no enabled daily i ching configs are available")
	}
	return nil, fmt.Errorf("multiple daily i ching configs are enabled. Use /iching <config_key> next")
}

func (f *DailyIChingFeature) runScheduler(ctx context.Context) {
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

func (f *DailyIChingFeature) runDueChecks(ctx context.Context, now time.Time) {
	configs, err := f.store.listEnabledConfigs()
	if err != nil {
		slog.Warn("beta daily iching: failed to list configs", "error", err)
		return
	}

	for i := range configs {
		cfg := configs[i]
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := f.runDueConfig(runCtx, &cfg, now); err != nil {
			slog.Warn("beta daily iching: due check failed", "config", cfg.Key, "error", err)
		}
		cancel()
	}
}

func (f *DailyIChingFeature) runDueConfig(ctx context.Context, cfg *DailyIChingConfig, now time.Time) error {
	localNow := cfg.localNow(now)
	minuteOfDay := localNow.Hour()*60 + localNow.Minute()
	localDate := localNow.Format("2006-01-02")

	postMinute, err := parseTimeOfDay(cfg.PostTime)
	if err != nil {
		return fmt.Errorf("invalid post_time for %s: %w", cfg.Key, err)
	}
	if !withinWindow(minuteOfDay, postMinute) {
		return nil
	}
	_, _, err = f.postNextLesson(ctx, cfg, localDate, triggerKindScheduled, false)
	return err
}

func (f *DailyIChingFeature) postNextLesson(ctx context.Context, cfg *DailyIChingConfig, localDate, triggerKind string, allowSameDayAdvance bool) (*lessonDelivery, bool, error) {
	f.postMu.Lock()
	defer f.postMu.Unlock()

	if !allowSameDayAdvance {
		alreadyPosted, err := f.store.hasPost(cfg.TenantID, cfg.ID, localDate, postKindLesson)
		if err != nil {
			return nil, false, err
		}
		if alreadyPosted {
			return nil, false, nil
		}
	}

	progress, err := f.store.getOrCreateProgress(cfg.TenantID, cfg.ID)
	if err != nil {
		return nil, false, err
	}

	nextIndex := nextSequenceIndex(progress.SequenceIndex)
	meta, ok := hexagramAtSequenceIndex(nextIndex)
	if !ok {
		return nil, false, fmt.Errorf("hexagram sequence index %d is invalid", nextIndex)
	}

	var prev *hexagramMeta
	if progress.CurrentHexagram > 0 {
		current, ok := hexagramByNumber(progress.CurrentHexagram)
		if ok {
			prev = &current
		}
	}

	transition := buildTransitionNote(prev, meta)
	grounding, err := f.groundingForHexagram(meta.Number)
	if err != nil {
		return nil, false, err
	}
	body := renderLessonText(meta, nextIndex, transition, grounding, false)
	if err := f.sendTextToConfig(ctx, cfg, body); err != nil {
		return nil, false, err
	}

	now := time.Now().UTC()
	if err := f.store.recordPost(&LessonPost{
		TenantID:      cfg.TenantID,
		ConfigID:      cfg.ID,
		LocalDate:     localDate,
		PostKind:      postKindLesson,
		TriggerKind:   triggerKind,
		SequenceIndex: nextIndex,
		Hexagram:      meta.Number,
		CreatedAt:     now,
	}); err != nil {
		return nil, false, err
	}
	if err := f.store.updateProgress(cfg.TenantID, cfg.ID, nextIndex, meta.Number, localDate, now); err != nil {
		return nil, false, err
	}

	return &lessonDelivery{
		Body:          body,
		Hexagram:      meta,
		SequenceIndex: nextIndex,
		LocalDate:     localDate,
	}, true, nil
}

func (f *DailyIChingFeature) postDeeperLesson(ctx context.Context, cfg *DailyIChingConfig, localDate, triggerKind string) (*lessonDelivery, error) {
	f.postMu.Lock()
	defer f.postMu.Unlock()

	progress, err := f.store.getOrCreateProgress(cfg.TenantID, cfg.ID)
	if err != nil {
		return nil, err
	}
	if progress.CurrentHexagram <= 0 || progress.SequenceIndex <= 0 {
		return nil, fmt.Errorf("no current hexagram yet; post the next lesson first")
	}

	meta, ok := hexagramByNumber(progress.CurrentHexagram)
	if !ok {
		return nil, fmt.Errorf("current hexagram %d is invalid", progress.CurrentHexagram)
	}

	var prev *hexagramMeta
	if progress.SequenceIndex > 1 {
		if value, ok := hexagramAtSequenceIndex(previousSequenceIndex(progress.SequenceIndex)); ok {
			prev = &value
		}
	}
	transition := buildTransitionNote(prev, meta)
	grounding, err := f.groundingForHexagram(meta.Number)
	if err != nil {
		return nil, err
	}
	body := renderLessonText(meta, progress.SequenceIndex, transition, grounding, true)
	if err := f.sendTextToConfig(ctx, cfg, body); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if err := f.store.recordPost(&LessonPost{
		TenantID:      cfg.TenantID,
		ConfigID:      cfg.ID,
		LocalDate:     localDate,
		PostKind:      postKindDeeper,
		TriggerKind:   triggerKind,
		SequenceIndex: progress.SequenceIndex,
		Hexagram:      meta.Number,
		CreatedAt:     now,
	}); err != nil {
		return nil, err
	}

	return &lessonDelivery{
		Body:          body,
		Hexagram:      meta,
		SequenceIndex: progress.SequenceIndex,
		LocalDate:     localDate,
	}, nil
}

func (f *DailyIChingFeature) statusForConfig(cfg *DailyIChingConfig, localDate string) (ConfigStatus, error) {
	progress, err := f.store.getOrCreateProgress(cfg.TenantID, cfg.ID)
	if err != nil {
		return ConfigStatus{}, err
	}
	todayPosted, err := f.store.hasPost(cfg.TenantID, cfg.ID, localDate, postKindLesson)
	if err != nil {
		return ConfigStatus{}, err
	}

	index := f.indexSnapshot()
	status := ConfigStatus{
		Config:        *cfg,
		Progress:      *progress,
		LocalDate:     localDate,
		IndexReady:    index != nil && len(index.Sections) == len(kingWenSequence),
		TodayPosted:   todayPosted,
		SourceCount:   len(index.Sources),
		HexagramCount: len(index.Sections),
	}

	if progress.CurrentHexagram > 0 {
		meta, ok := hexagramByNumber(progress.CurrentHexagram)
		if ok {
			currentTransition := transitionNote{}
			if progress.SequenceIndex > 1 {
				if prev, ok := hexagramAtSequenceIndex(previousSequenceIndex(progress.SequenceIndex)); ok {
					currentTransition = buildTransitionNote(&prev, meta)
				}
			}
			status.Current = &HexagramStatus{
				Number:        meta.Number,
				Name:          meta.Name,
				Title:         meta.Title,
				SequenceIndex: progress.SequenceIndex,
				Transition:    currentTransition.Text,
			}
			status.CurrentSummary = fmt.Sprintf("Quẻ %d %s", meta.Number, meta.Name)
		}
	}

	nextIndex := nextSequenceIndex(progress.SequenceIndex)
	nextMeta, _ := hexagramAtSequenceIndex(nextIndex)
	var prev *hexagramMeta
	if progress.CurrentHexagram > 0 {
		current, ok := hexagramByNumber(progress.CurrentHexagram)
		if ok {
			prev = &current
		}
	}
	nextTransition := buildTransitionNote(prev, nextMeta)
	status.Next = HexagramStatus{
		Number:        nextMeta.Number,
		Name:          nextMeta.Name,
		Title:         nextMeta.Title,
		SequenceIndex: nextIndex,
		Transition:    nextTransition.Text,
	}
	return status, nil
}

func (f *DailyIChingFeature) groundingForHexagram(number int) (groundingContext, error) {
	index := f.indexSnapshot()
	section := index.sectionByNumber(number)
	if section == nil {
		return groundingContext{}, fmt.Errorf("hexagram %d is missing from the local book index", number)
	}

	overview := summarizeChunks(section.Chunks, 2)
	practical := summarizeChunks(selectSectionChunks(section, "ha"), 2)
	philosophical := summarizeChunks(selectSectionChunks(section, "thuong"), 2)
	if practical == "" {
		practical = overview
	}
	if philosophical == "" {
		philosophical = overview
	}

	return groundingContext{
		OverviewText:      overview,
		PracticalText:     practical,
		PhilosophicalText: philosophical,
		Source:            section.DisplaySource,
	}, nil
}

func selectSectionChunks(section *hexagramSection, kind string) []bookChunk {
	if section == nil || len(section.Chunks) == 0 {
		return nil
	}
	type scoredChunk struct {
		score int
		index int
	}

	var ranked []scoredChunk
	for i, chunk := range section.Chunks {
		score := 0
		switch kind {
		case "ha":
			if strings.Contains(chunk.Normalized, "duoi day ban ve phan hinh nhi ha") {
				score += 40
			}
			if chunk.HasHa {
				score += 20
			}
		case "thuong":
			if strings.Contains(chunk.Normalized, "hinh nhi thuong") {
				score += 25
			}
			if strings.Contains(chunk.Normalized, "dai tuong") {
				score += 10
			}
			if chunk.HasThuong {
				score += 20
			}
		}
		score += countTokenHits(chunk.Normalized, tokenizeComparableText(section.Name+" "+section.Title))
		if score > 0 {
			ranked = append(ranked, scoredChunk{score: score, index: i})
		}
	}
	if len(ranked) == 0 {
		if len(section.Chunks) > 3 {
			return append([]bookChunk(nil), section.Chunks[:3]...)
		}
		return append([]bookChunk(nil), section.Chunks...)
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].index < ranked[j].index
	})

	seen := make(map[int]struct{})
	var selected []bookChunk
	for _, item := range ranked {
		for _, idx := range []int{item.index, item.index + 1} {
			if idx < 0 || idx >= len(section.Chunks) {
				continue
			}
			if _, ok := seen[idx]; ok {
				continue
			}
			seen[idx] = struct{}{}
			selected = append(selected, section.Chunks[idx])
			if len(selected) >= 3 {
				return selected
			}
		}
	}
	return selected
}

func summarizeChunks(chunks []bookChunk, maxSentences int) string {
	if len(chunks) == 0 || maxSentences <= 0 {
		return ""
	}
	var collected []string
	for _, chunk := range chunks {
		for _, sentence := range splitSentences(chunk.Text) {
			sentence = cleanSnippet(sentence)
			if !isReadableSentence(sentence) {
				continue
			}
			collected = append(collected, sentence)
			if len(collected) >= maxSentences {
				return strings.Join(uniqueStrings(collected), " ")
			}
		}
	}
	return strings.Join(uniqueStrings(collected), " ")
}

func splitSentences(value string) []string {
	value = cleanSourceLine(value)
	if value == "" {
		return nil
	}
	var (
		out []string
		b   strings.Builder
	)
	for _, r := range value {
		b.WriteRune(r)
		switch r {
		case '.', '!', '?', ';':
			out = append(out, b.String())
			b.Reset()
		}
	}
	if tail := strings.TrimSpace(b.String()); tail != "" {
		out = append(out, tail)
	}
	return out
}

func isReadableSentence(sentence string) bool {
	sentence = strings.TrimSpace(sentence)
	if sentence == "" {
		return false
	}
	normalized := normalizeComparableText(sentence)
	if normalized == "" {
		return false
	}
	if strings.Contains(normalized, "nguyen duy can") || strings.Contains(normalized, "dich kinh tuong giai") {
		return false
	}
	words := strings.Fields(normalized)
	if len(words) < 6 {
		return false
	}
	length := len([]rune(sentence))
	return length >= 38 && length <= 240
}

func buildTransitionNote(prev *hexagramMeta, current hexagramMeta) transitionNote {
	switch {
	case prev == nil:
		return transitionNote{
			Kind: "opening",
			Text: fmt.Sprintf("Bắt đầu chuỗi học bằng quẻ %d %s: lấy sáng tạo, trục chính và tinh thần mở đường làm nền cho các ngày sau.", current.Number, current.Name),
		}
	case prev.Number == 30 && current.Number == 31:
		return transitionNote{
			Kind: "lower_canon_start",
			Text: "Từ Ly sang Hàm, mạch học rời Thượng Kinh để bước vào Hạ Kinh: từ nền tảng vũ trụ luận chuyển sang nhân sự, cảm ứng và đời sống quan hệ.",
		}
	case prev.Number == 64 && current.Number == 1:
		return transitionNote{
			Kind: "cycle_restart",
			Text: "Sau Vị Tế, vòng học trở lại Càn: Dịch không khép lại ở chỗ hoàn tất, mà mở ra một chu kỳ sáng tạo mới.",
		}
	case current.Number == prev.Number+1 && current.Number%2 == 0:
		return transitionNote{
			Kind: "paired",
			Text: fmt.Sprintf("Từ %s sang %s, chuỗi chuyển sang mặt đối ứng của cùng một thế: không phủ định quẻ trước, mà soi lại nó từ vị trí bổ sung và hiệu chỉnh.", prev.Name, current.Name),
		}
	default:
		return transitionNote{
			Kind: "progression",
			Text: fmt.Sprintf("Sau %s, chuỗi học đi tiếp sang %s như một bước xử lý mới trong trật tự Văn Vương: tình thế đổi thì điểm nhấn cũng đổi.", prev.Name, current.Name),
		}
	}
}

func renderLessonText(meta hexagramMeta, sequenceIndex int, transition transitionNote, grounding groundingContext, deeper bool) string {
	var b strings.Builder
	if deeper {
		fmt.Fprintf(&b, "Đọc sâu Kinh Dịch\nQuẻ %d/64: %s - %s\n\n", meta.Number, meta.Name, meta.Title)
		fmt.Fprintf(&b, "Mạch tiếp nối\n%s\n\n", transition.Text)
		fmt.Fprintf(&b, "Tổng quan\n%s\n\n", buildOverviewParagraph(transition, grounding))
		fmt.Fprintf(&b, "Hình nhi hạ\n%s\n\n", buildPracticalParagraph(grounding))
		fmt.Fprintf(&b, "Hình nhi thượng\n%s\n\n", buildPhilosophicalParagraph(grounding))
		fmt.Fprintf(&b, "Nguồn đọc sâu\n")
		if grounding.OverviewText != "" {
			fmt.Fprintf(&b, "- Mở quẻ: %s\n", grounding.OverviewText)
		}
		if grounding.PracticalText != "" {
			fmt.Fprintf(&b, "- Thực dụng: %s\n", grounding.PracticalText)
		}
		if grounding.PhilosophicalText != "" {
			fmt.Fprintf(&b, "- Triết lý: %s\n", grounding.PhilosophicalText)
		}
		if grounding.Source != "" {
			fmt.Fprintf(&b, "- Nguồn: %s\n\n", grounding.Source)
		} else {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Tự học tiếp\n")
		for _, prompt := range buildDeepStudyPrompts(meta, transition) {
			fmt.Fprintf(&b, "- %s\n", prompt)
		}
		return strings.TrimSpace(b.String())
	}

	fmt.Fprintf(&b, "Kinh Dịch Mỗi Ngày\nQuẻ %d/64: %s - %s\n\n", meta.Number, meta.Name, meta.Title)
	fmt.Fprintf(&b, "Mạch tiếp nối\n%s\n\n", transition.Text)
	fmt.Fprintf(&b, "Tổng quan\n%s\n\n", buildOverviewParagraph(transition, grounding))
	fmt.Fprintf(&b, "Hình nhi hạ\n%s\n\n", buildPracticalParagraph(grounding))
	fmt.Fprintf(&b, "Hình nhi thượng\n%s\n\n", buildPhilosophicalParagraph(grounding))
	fmt.Fprintf(&b, "Ứng dụng hôm nay\n")
	for _, prompt := range buildApplicationPrompts(meta, transition) {
		fmt.Fprintf(&b, "- %s\n", prompt)
	}
	fmt.Fprintf(&b, "\nLệnh nhanh: /iching deeper | /iching next")
	return strings.TrimSpace(b.String())
}

func buildOverviewParagraph(transition transitionNote, grounding groundingContext) string {
	parts := []string{transition.Text}
	if grounding.OverviewText != "" {
		parts = append(parts, "Nguồn hôm nay mở quẻ bằng ý này: "+grounding.OverviewText)
	}
	return strings.Join(parts, " ")
}

func buildPracticalParagraph(grounding groundingContext) string {
	parts := []string{
		"Ở bình diện hình nhi hạ, quẻ này nên đọc như bài học xử trí tình thế, quan hệ và hành động cụ thể.",
	}
	if grounding.PracticalText != "" {
		parts = append(parts, "Sách gợi ra: "+grounding.PracticalText)
	} else {
		parts = append(parts, "Điểm chính là giữ đúng vị, đúng mức và đúng thời trước khi đòi kết quả.")
	}
	return strings.Join(parts, " ")
}

func buildPhilosophicalParagraph(grounding groundingContext) string {
	parts := []string{
		"Ở bình diện hình nhi thượng, quẻ này nên đọc như một mẫu vận động của âm dương, của nội tâm và của nguyên lý đứng sau sự việc.",
	}
	if grounding.PhilosophicalText != "" {
		parts = append(parts, "Nguồn nhấn mạnh: "+grounding.PhilosophicalText)
	} else {
		parts = append(parts, "Điều cốt lõi là thấy nguyên lý trước khi bám chặt vào biểu hiện bề mặt.")
	}
	return strings.Join(parts, " ")
}

func buildApplicationPrompts(meta hexagramMeta, transition transitionNote) []string {
	prompt1 := "Nhìn lại một việc đang chuyển trạng thái trong hôm nay: điều gì cần chỉnh vị thế trước khi hành động?"
	switch transition.Kind {
	case "paired":
		prompt1 = "So lại với quẻ hôm qua: đâu là mặt bổ sung của cùng một vấn đề mà hôm nay mình cần nhìn thêm?"
	case "lower_canon_start":
		prompt1 = "Đưa ý quẻ vào một mối quan hệ hay tình huống cụ thể hôm nay, thay vì chỉ giữ nó ở mức lý thuyết."
	case "cycle_restart":
		prompt1 = "Hãy bắt đầu lại một việc như một chu kỳ mới, không mang tâm lý đã biết hết hoặc đã xong rồi."
	}
	return []string{
		prompt1,
		"Tách phần việc thuộc xử trí trước mắt khỏi phần nguyên lý mình cần giữ cho ổn định.",
		fmt.Sprintf("Chọn một bước nhỏ để sống đúng tinh thần quẻ %s trong hôm nay.", meta.Name),
	}
}

func buildDeepStudyPrompts(meta hexagramMeta, transition transitionNote) []string {
	out := buildApplicationPrompts(meta, transition)
	out[1] = "Ghi lại một câu trong nguồn hôm nay chạm vào mình nhất, rồi đối chiếu nó với hoàn cảnh đang gặp."
	return out
}

func formatStatusText(status ConfigStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Daily I Ching - %s\n", status.Config.Name)
	fmt.Fprintf(&b, "Ngày địa phương: %s | Giờ đăng: %s %s\n", status.LocalDate, status.Config.PostTime, status.Config.Timezone)
	if status.Current != nil {
		fmt.Fprintf(&b, "Hiện tại: quẻ %d %s - %s\n", status.Current.Number, status.Current.Name, status.Current.Title)
	} else {
		b.WriteString("Hiện tại: chưa có quẻ nào được đăng\n")
	}
	fmt.Fprintf(&b, "Tiếp theo: quẻ %d %s - %s\n", status.Next.Number, status.Next.Name, status.Next.Title)
	if status.TodayPosted {
		b.WriteString("Hôm nay: đã đăng bài học\n")
	} else {
		b.WriteString("Hôm nay: chưa đăng bài học\n")
	}
	fmt.Fprintf(&b, "Chỉ mục sách: %d/%d quẻ từ %d nguồn", status.HexagramCount, len(kingWenSequence), status.SourceCount)
	return strings.TrimSpace(b.String())
}

func (f *DailyIChingFeature) sendTextToConfig(ctx context.Context, cfg *DailyIChingConfig, text string) error {
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
