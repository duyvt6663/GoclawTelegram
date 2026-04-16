package jobcrawler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	defaultTimezone          = "Asia/Ho_Chi_Minh"
	defaultPostTime          = "09:00"
	defaultMaxResults        = 5
	maxMaxResults            = 20
	defaultDedupeWindowDays  = 14
	maxDedupeWindowDays      = 90
	defaultLocationMode      = locationModeHybrid
	locationModeRemoteGlobal = "remote_global"
	locationModeVietnam      = "vietnam"
	locationModeHybrid       = "hybrid"
	runStatusRunning         = "running"
	runStatusSuccess         = "success"
	runStatusNoResults       = "no_results"
	runStatusFailed          = "failed"
	triggerKindScheduled     = "scheduled"
	triggerKindManual        = "manual"
	triggerKindDynamic       = "dynamic"
	sourceRemoteOK           = "remoteok"
	sourceWeWorkRemotely     = "weworkremotely"
	sourceLinkedInProxy      = "linkedin_proxy"
	sourceCacheTTL           = 10 * time.Minute
)

var (
	configKeyRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	vietnamHints = []string{
		" vietnam", "vietnam ", "vietnam,", "vietnam)",
		"ho chi minh", "hcmc", "saigon", "ha noi", "hanoi", "da nang", "danang", "can tho", "hai phong",
	}
	remoteHints = []string{
		"remote", "remotely", "anywhere", "global", "worldwide", "work from home", "work-from-home", "distributed",
	}
	asiaHints = []string{
		" asia", "asia ", "apac", "southeast asia", "singapore", "hong kong", "india", "bangalore",
		"tokyo", "japan", "seoul", "korea", "taiwan", "thailand", "indonesia", "jakarta",
		"philippines", "manila", "malaysia", "kuala lumpur", "china", "shanghai",
	}
	defaultSources = []string{sourceRemoteOK, sourceWeWorkRemotely}
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

func withinWindow(minuteOfDay, dueMinute int) bool {
	return minuteOfDay >= dueMinute
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

func floatArg(args map[string]any, key string) (float64, bool) {
	switch value := args[key].(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	default:
		return 0, false
	}
}

func stringSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case []string:
		return normalizeStringSlice(value)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return normalizeStringSlice(out)
	default:
		return nil
	}
}

func normalizeStringSlice(values []string) []string {
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
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeKeywords(values []string) []string {
	values = normalizeStringSlice(values)
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.ToLower(strings.TrimSpace(value)))
	}
	sort.Strings(out)
	return out
}

func normalizeSources(values []string) []string {
	if len(values) == 0 {
		return append([]string(nil), defaultSources...)
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" {
			continue
		}
		if _, ok := sourceSpecs[value]; !ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return append([]string(nil), defaultSources...)
	}
	return out
}

func supportedSourceList() []string {
	out := make([]string, 0, len(sourceSpecs))
	for key := range sourceSpecs {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func effectiveSourceIDs(cfg *JobCrawlerConfig) []string {
	if cfg == nil {
		return append([]string(nil), defaultSources...)
	}
	out := normalizeSources(cfg.Sources)
	if cfg.EnableLinkedInProxySource {
		seen := false
		for _, sourceID := range out {
			if sourceID == sourceLinkedInProxy {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, sourceLinkedInProxy)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), defaultSources...)
	}
	return out
}

func buildLinkedInProxyIntent(cfg *JobCrawlerConfig) string {
	if cfg == nil {
		return "AI engineer machine learning engineer remote"
	}

	parts := []string{"AI engineer", "machine learning engineer"}
	switch cfg.LocationMode {
	case locationModeVietnam:
		parts = append(parts, "Vietnam")
	default:
		if cfg.RemoteOnly || cfg.LocationMode == locationModeRemoteGlobal || cfg.LocationMode == locationModeHybrid {
			parts = append(parts, "remote")
		}
	}
	for _, keyword := range cfg.KeywordsInclude {
		if cleanText(keyword) == "" {
			continue
		}
		parts = append(parts, keyword)
		if len(parts) >= 6 {
			break
		}
	}
	return cleanText(strings.Join(parts, " "))
}

func resolveLinkedInProxyMaxResults(cfg *JobCrawlerConfig) int {
	base := defaultMaxResults * 4
	if cfg != nil && cfg.MaxResults > 0 {
		base = cfg.MaxResults * 4
	}
	switch {
	case base < 18:
		return 18
	case base > 32:
		return 32
	default:
		return base
	}
}

func parseRFC3339(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		utc := ts.UTC()
		return &utc
	}
	if ts, err := time.Parse(time.RFC1123Z, value); err == nil {
		utc := ts.UTC()
		return &utc
	}
	if ts, err := time.Parse(time.RFC1123, value); err == nil {
		utc := ts.UTC()
		return &utc
	}
	return nil
}

func canonicalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "ref" || lower == "source" || lower == "fbclid" || lower == "gclid" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	host := strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")
	parsed.Host = host
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	return parsed.String()
}

func makeJobHash(title, company, rawURL string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(title)) + "|" + strings.ToLower(strings.TrimSpace(company)) + "|" + canonicalizeURL(rawURL)))
	return hex.EncodeToString(sum[:])
}

func encodeStringSlice(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	data, _ := json.Marshal(values)
	return string(data)
}

func decodeStringSlice(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		return nil
	}
	return out
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

func trimText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return strings.TrimSpace(value[:limit-3]) + "..."
}

func cleanText(value string) string {
	value = strings.ReplaceAll(value, "\u00a0", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\t", " ")
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func normalizeLocation(raw string, fallbackRemote bool) normalizedLocation {
	raw = cleanText(raw)
	lower := strings.ToLower(" " + raw + " ")
	isVietnam := false
	for _, hint := range vietnamHints {
		if strings.Contains(lower, hint) {
			isVietnam = true
			break
		}
	}
	isAsia := isVietnam
	if !isAsia {
		for _, hint := range asiaHints {
			if strings.Contains(lower, hint) {
				isAsia = true
				break
			}
		}
	}
	isRemote := fallbackRemote
	for _, hint := range remoteHints {
		if strings.Contains(lower, hint) {
			isRemote = true
			break
		}
	}

	switch {
	case isRemote && isVietnam:
		return normalizedLocation{Label: "Remote / Vietnam", IsRemote: true, IsVietnam: true, IsAsia: true}
	case isVietnam:
		return normalizedLocation{Label: "Vietnam", IsVietnam: true, IsAsia: true}
	case isRemote && isAsia:
		return normalizedLocation{Label: "Remote / Asia", IsRemote: true, IsAsia: true}
	case isAsia:
		return normalizedLocation{Label: "Asia", IsAsia: true}
	case isRemote:
		return normalizedLocation{Label: "Remote / Global", IsRemote: true}
	case raw != "":
		return normalizedLocation{Label: raw}
	default:
		if fallbackRemote {
			return normalizedLocation{Label: "Remote / Global", IsRemote: true}
		}
		return normalizedLocation{Label: "Unknown"}
	}
}

func keywordMatchScore(keyword string, fields ...string) float64 {
	keyword = normalizeComparableText(keyword)
	if keyword == "" {
		return 0
	}
	keywordTokens := strings.Fields(keyword)
	if len(keywordTokens) == 0 {
		return 0
	}
	var score float64
	for idx, field := range fields {
		field = normalizeComparableText(field)
		if field == "" {
			continue
		}
		phraseWeight, tokenWeight := 0.75, 0.4
		switch idx {
		case 0:
			phraseWeight, tokenWeight = 5.5, 3.5
		case 1:
			phraseWeight, tokenWeight = 3.25, 2.0
		case 2:
			phraseWeight, tokenWeight = 1.75, 1.0
		}
		if containsPhrase(field, keyword) {
			score += phraseWeight
		}

		fieldTokens := make(map[string]struct{})
		for _, token := range strings.Fields(field) {
			fieldTokens[token] = struct{}{}
		}
		matchedTokens := 0
		for _, token := range keywordTokens {
			if _, ok := fieldTokens[token]; ok {
				matchedTokens++
			}
		}
		if matchedTokens == 0 {
			continue
		}
		matchRatio := float64(matchedTokens) / float64(len(keywordTokens))
		score += tokenWeight * matchRatio
		if matchedTokens == len(keywordTokens) && !containsPhrase(field, keyword) {
			score += tokenWeight * 0.35
		}
	}
	return score
}

func isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists")
}

func recencyScore(now time.Time, postedAt *time.Time) float64 {
	if postedAt == nil {
		return 0.35
	}
	ageHours := now.Sub(*postedAt).Hours()
	switch {
	case ageHours <= 24:
		return 1.5
	case ageHours <= 72:
		return 1.0
	case ageHours <= 7*24:
		return 0.5
	default:
		return math.Max(0.1, 0.4-(ageHours/(60*24)))
	}
}

func markdownEscapedURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return strings.ReplaceAll(raw, ")", "%29")
}
