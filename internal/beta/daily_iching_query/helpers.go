package dailyichingquery

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

var (
	hexagramNumberRe = regexp.MustCompile(`\bque\s+(?:so\s+)?([1-9]|[1-5][0-9]|6[0-4])\b`)
	lineOrdinalRe    = regexp.MustCompile(`\bhao\s*([1-6])\b`)
)

func tenantKey(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return strings.TrimSpace(id.String())
}

func tenantKeyFromCtx(ctx context.Context) string {
	return tenantKey(storepkg.TenantIDFromContext(ctx))
}

func tenantScopedContext(ctx context.Context, tenantID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return ctx
	}
	parsed, err := uuid.Parse(tenantID)
	if err != nil {
		return ctx
	}
	return storepkg.WithTenantID(ctx, parsed)
}

func toolJSONResult(data any) *tools.Result {
	encoded, err := json.Marshal(data)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(string(encoded))
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func containsWholePhrase(haystack, phrase string) bool {
	haystack = strings.TrimSpace(haystack)
	phrase = strings.TrimSpace(phrase)
	if haystack == "" || phrase == "" {
		return false
	}
	return strings.Contains(" "+haystack+" ", " "+phrase+" ")
}

func normalizeSpace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func trimRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
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
