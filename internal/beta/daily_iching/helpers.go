package dailyiching

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	defaultTimezone = "Asia/Ho_Chi_Minh"
	defaultPostTime = "07:00"

	postKindLesson = "lesson"
	postKindDeeper = "deeper"

	triggerKindScheduled = "scheduled"
	triggerKindManual    = "manual"
	triggerKindCommand   = "command"

	bookIndexVersion = 4
)

var (
	configKeyRe        = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	headingPrefixRe    = regexp.MustCompile(`^\s*([1-9]|[1-5][0-9]|6[0-4])\.\s*`)
	whitespaceCollapse = regexp.MustCompile(`\s+`)
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

func parseTimeOfDay(value string) (int, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("time must use HH:MM")
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid hour")
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid minute")
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, fmt.Errorf("time must be in 00:00-23:59")
	}
	return hour*60 + minute, nil
}

func loadLocation(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultTimezone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func parseISODate(value string) (time.Time, error) {
	return time.Parse("2006-01-02", strings.TrimSpace(value))
}

func resolveLocalDate(cfg *DailyIChingConfig, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return cfg.localDate(time.Now().UTC()), nil
	}
	parsed, err := parseISODate(value)
	if err != nil {
		return "", fmt.Errorf("date must use YYYY-MM-DD")
	}
	return parsed.Format("2006-01-02"), nil
}

func withinWindow(minuteOfDay, dueMinute int) bool {
	return minuteOfDay >= dueMinute
}

func cleanText(value string) string {
	value = canonicalUnicodeText(value)
	value = strings.ReplaceAll(value, "\u00a0", " ")
	value = strings.ReplaceAll(value, "\t", " ")
	return whitespaceCollapse.ReplaceAllString(strings.TrimSpace(value), " ")
}

func cleanSourceLine(value string) string {
	value = canonicalUnicodeText(value)
	value = strings.ReplaceAll(value, "\u00a0", " ")
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = whitespaceCollapse.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func cleanSnippet(value string) string {
	value = cleanSourceLine(value)
	replacer := strings.NewReplacer(" .", ".", " ,", ",", " ;", ";", " :", ":", " )", ")", "( ", "(")
	value = replacer.Replace(value)
	return strings.TrimSpace(value)
}

func stripDiacritics(value string) string {
	normalized, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), value)
	if err != nil {
		return value
	}
	return normalized
}

func canonicalUnicodeText(value string) string {
	if value == "" {
		return ""
	}
	value = norm.NFC.String(value)
	return strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError, r == '�':
			return -1
		case r == '\u00a0':
			return ' '
		case unicode.IsControl(r) && r != '\n' && r != '\t':
			return -1
		default:
			return r
		}
	}, value)
}

func normalizeComparableText(value string) string {
	value = stripDiacritics(strings.ToLower(cleanText(value)))
	var b strings.Builder
	lastSpace := true
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return whitespaceCollapse.ReplaceAllString(strings.TrimSpace(b.String()), " ")
}

func tokenizeComparableText(value string) []string {
	normalized := normalizeComparableText(value)
	if normalized == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, token := range strings.Fields(normalized) {
		if runeLen(token) < 2 {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func ocrTextNoisePenalty(value string) int {
	value = cleanSourceLine(value)
	if value == "" {
		return 100
	}

	normalized := normalizeComparableText(value)
	if normalized == "" {
		return 100
	}

	penalty := 0
	for _, phrase := range []string{
		"dich kinh tuong giai",
		"thu giang nguyen duy can",
		"nha xuat ban tre",
		"tong bien tap",
	} {
		if strings.Contains(normalized, phrase) {
			penalty += 30
		}
	}

	letters := 0
	digits := 0
	other := 0
	for _, r := range value {
		switch {
		case unicode.IsLetter(r):
			letters++
		case unicode.IsDigit(r):
			digits++
		case unicode.IsSpace(r):
		default:
			other++
		}
	}
	if letters == 0 {
		return 100
	}
	if other > letters/3 {
		penalty += 12
	}
	if other > letters {
		penalty += 18
	}

	tokens := strings.Fields(normalized)
	if len(tokens) == 0 {
		return 100
	}
	if digits >= 2 && len(tokens) <= 4 {
		penalty += minInt(digits*2, 12)
	}

	shortTokens := 0
	longTokens := 0
	brokenTokens := 0
	for _, token := range tokens {
		length := runeLen(token)
		if length <= 2 {
			shortTokens++
		}
		if length >= 4 {
			longTokens++
		}
		if strings.IndexFunc(token, unicode.IsDigit) >= 0 {
			brokenTokens++
		}
		switch token {
		case "aa", "ae", "ee", "fe", "ll", "mm", "oe":
			brokenTokens++
		}
	}
	if shortTokens*2 >= len(tokens) {
		penalty += 18
	}
	if len(tokens) <= 3 && longTokens == 0 {
		penalty += 18
	}
	if len(tokens) <= 2 && letters <= 8 {
		penalty += 18
	}
	if brokenTokens > 0 {
		penalty += minInt(brokenTokens*6, 24)
	}
	return penalty
}

func isLikelyNoisyOCRText(value string) bool {
	return ocrTextNoisePenalty(value) >= 30
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func containsAnyToken(haystack string, tokens []string) bool {
	if haystack == "" || len(tokens) == 0 {
		return false
	}
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if strings.Contains(haystack, token) {
			return true
		}
	}
	return false
}

func countTokenHits(haystack string, tokens []string) int {
	if haystack == "" || len(tokens) == 0 {
		return 0
	}
	score := 0
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if strings.Contains(haystack, token) {
			score++
		}
	}
	return score
}

func countTokenOverlap(haystackTokens, queryTokens []string) int {
	if len(haystackTokens) == 0 || len(queryTokens) == 0 {
		return 0
	}
	haystack := make(map[string]struct{}, len(haystackTokens))
	for _, token := range haystackTokens {
		if token == "" {
			continue
		}
		haystack[token] = struct{}{}
	}
	score := 0
	for _, token := range queryTokens {
		if token == "" {
			continue
		}
		if _, ok := haystack[token]; ok {
			score++
		}
	}
	return score
}

func normalizedTokenSet(value string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, token := range strings.Fields(strings.TrimSpace(value)) {
		if token == "" {
			continue
		}
		out[token] = struct{}{}
	}
	return out
}

func consonantSkeletonComparableText(value string) string {
	normalized := normalizeComparableText(value)
	if normalized == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range normalized {
		if !unicode.IsLetter(r) {
			continue
		}
		if r == 'đ' {
			r = 'd'
		}
		switch r {
		case 'a', 'e', 'i', 'o', 'u', 'y':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func hashSignature(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func relativeOrBase(root, path string) string {
	if root != "" {
		if rel, err := filepath.Rel(root, path); err == nil {
			return rel
		}
	}
	return filepath.Base(path)
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

func runeLen(value string) int {
	return utf8.RuneCountInString(value)
}

func trimRunes(value string, limit int) string {
	if limit <= 0 || runeLen(value) <= limit {
		return value
	}
	var b strings.Builder
	count := 0
	for _, r := range value {
		if count >= limit {
			break
		}
		b.WriteRune(r)
		count++
	}
	return strings.TrimSpace(b.String())
}
