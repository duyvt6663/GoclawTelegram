package russianroulette

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	cryptorand "crypto/rand"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	featureName = "russian_roulette"

	defaultChamberSize         = 6
	minChamberSize             = 2
	maxChamberSize             = 12
	defaultTurnCooldownSeconds = 8
	maxTurnCooldownSeconds     = 60
	defaultPenaltyDurationSecs = 60
	maxPenaltyDurationSecs     = 3600
	defaultPenaltyTag          = "Walking Disaster"
	leaderboardDefaultLimit    = 10
	leaderboardMaxLimit        = 25
	telegramFileIDURLPrefix    = "telegram-file-id:"
	roundStatusLobby           = "lobby"
	roundStatusActive          = "active"
	roundStatusCompleted       = "completed"
	roundStatusCancelled       = "cancelled"
	penaltyModeNone            = "none"
	penaltyModeMute            = "mute"
	penaltyModeTag             = "tag"
	defaultConfigDisplayName   = "Russian Roulette"
)

func tenantKey(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return strings.TrimSpace(id.String())
}

func tenantKeyFromCtx(ctx context.Context) string {
	return tenantKey(store.TenantIDFromContext(ctx))
}

func normalizeConfigKey(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}

	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-', r == '_', r == ' ', r == '/', r == ':':
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func normalizeThreadID(threadID int) int {
	if threadID <= 1 {
		return 0
	}
	return threadID
}

func parseCompositeChatTarget(target string) (string, int) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", 0
	}
	if idx := strings.Index(target, ":topic:"); idx > 0 {
		threadID, _ := strconv.Atoi(target[idx+7:])
		return target[:idx], normalizeThreadID(threadID)
	}
	if idx := strings.Index(target, ":thread:"); idx > 0 {
		threadID, _ := strconv.Atoi(target[idx+8:])
		return target[:idx], normalizeThreadID(threadID)
	}
	return target, 0
}

func composeLocalKey(chatID string, threadID int) string {
	chatID = strings.TrimSpace(chatID)
	threadID = normalizeThreadID(threadID)
	if chatID == "" {
		return ""
	}
	if threadID == 0 {
		return chatID
	}
	return fmt.Sprintf("%s:topic:%d", chatID, threadID)
}

func defaultConfigKey(channel, chatID string, threadID int) string {
	return normalizeConfigKey(channel + "-" + composeLocalKey(chatID, threadID))
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func intToBool(value int) bool {
	return value != 0
}

func dbTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func boolArg(args map[string]any, key string) (*bool, bool) {
	value, ok := args[key].(bool)
	if !ok {
		return nil, false
	}
	return &value, true
}

func intArg(args map[string]any, key string) (int, bool) {
	switch value := args[key].(type) {
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func chatTargetFromToolContext(ctx context.Context, explicitChatID string, explicitThreadID int) (string, int) {
	explicitThreadID = normalizeThreadID(explicitThreadID)
	if explicitChatID != "" {
		chatID, threadID := parseCompositeChatTarget(explicitChatID)
		if explicitThreadID > 0 {
			threadID = explicitThreadID
		}
		return chatID, threadID
	}

	if localKey := tools.ToolLocalKeyFromCtx(ctx); localKey != "" {
		chatID, threadID := parseCompositeChatTarget(localKey)
		if explicitThreadID > 0 {
			threadID = explicitThreadID
		}
		return chatID, threadID
	}

	return strings.TrimSpace(tools.ToolChatIDFromCtx(ctx)), explicitThreadID
}

type playerIdentity struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	NumericID int64  `json:"numeric_id,omitempty"`
}

func normalizePlayerLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "@") {
		return value
	}
	if strings.ContainsFunc(value, unicode.IsSpace) {
		return value
	}
	if strings.ContainsAny(value, ".-_") || strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsNumber(r)
	}) >= 0 {
		return "@" + value
	}
	return value
}

func parsePlayerIdentity(rawID, rawLabel string) playerIdentity {
	rawID = strings.TrimSpace(rawID)
	rawLabel = strings.TrimSpace(rawLabel)
	if rawID == "" {
		return playerIdentity{}
	}

	id := rawID
	label := rawLabel
	if before, after, ok := strings.Cut(rawID, "|"); ok {
		id = strings.TrimSpace(before)
		if label == "" {
			label = normalizePlayerLabel(after)
		}
	}
	if label == "" {
		label = normalizePlayerLabel(rawLabel)
	}
	if label == "" {
		label = id
	}
	numericID, _ := strconv.ParseInt(id, 10, 64)
	return playerIdentity{
		ID:        id,
		Label:     label,
		NumericID: numericID,
	}
}

func secondsRemaining(until *time.Time, now time.Time) int {
	if until == nil {
		return 0
	}
	remaining := int(until.Sub(now).Seconds())
	if remaining < 0 {
		return 0
	}
	if remaining == 0 && now.Before(*until) {
		return 1
	}
	return remaining
}

func formatDurationSeconds(seconds int) string {
	if seconds <= 0 {
		return "0s"
	}
	if seconds%3600 == 0 {
		return fmt.Sprintf("%dh", seconds/3600)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	if seconds > 60 {
		return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%ds", seconds)
}

func sortPlayersBySeat(players []RoulettePlayer) {
	sort.Slice(players, func(i, j int) bool {
		if players[i].SeatOrder == players[j].SeatOrder {
			return players[i].CreatedAt.Before(players[j].CreatedAt)
		}
		return players[i].SeatOrder < players[j].SeatOrder
	})
}

func alivePlayerCount(players []RoulettePlayer) int {
	count := 0
	for _, player := range players {
		if player.IsAlive {
			count++
		}
	}
	return count
}

func currentPlayer(players []RoulettePlayer, order int) *RoulettePlayer {
	for i := range players {
		if players[i].IsAlive && players[i].SeatOrder == order {
			return &players[i]
		}
	}
	return nil
}

func nextAlivePlayer(players []RoulettePlayer, afterOrder int) *RoulettePlayer {
	if len(players) == 0 {
		return nil
	}
	sortPlayersBySeat(players)
	for i := range players {
		if players[i].IsAlive && players[i].SeatOrder > afterOrder {
			return &players[i]
		}
	}
	for i := range players {
		if players[i].IsAlive {
			return &players[i]
		}
	}
	return nil
}

func labelList(players []RoulettePlayer, aliveOnly bool) string {
	labels := make([]string, 0, len(players))
	for _, player := range players {
		if aliveOnly && !player.IsAlive {
			continue
		}
		if !aliveOnly && player.IsAlive {
			continue
		}
		labels = append(labels, player.UserLabel)
	}
	if len(labels) == 0 {
		return "nobody"
	}
	return strings.Join(labels, ", ")
}

func randomIntn(n int) int {
	if n <= 1 {
		return 0
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return int(time.Now().UnixNano() % int64(n))
	}
	return int(value.Int64())
}

func randomChamberPosition(chamberSize int) int {
	return randomIntn(chamberSize) + 1
}

func shufflePlayers(players []RoulettePlayer) {
	for i := len(players) - 1; i > 0; i-- {
		j := randomIntn(i + 1)
		players[i], players[j] = players[j], players[i]
	}
	for i := range players {
		players[i].SeatOrder = i + 1
	}
}

func outboundMeta(cfg RouletteConfig) map[string]string {
	meta := map[string]string{
		"local_key": composeLocalKey(cfg.ChatID, cfg.ThreadID),
	}
	if cfg.ThreadID > 0 {
		meta["message_thread_id"] = strconv.Itoa(cfg.ThreadID)
	}
	return meta
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
