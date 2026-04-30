package eaterychat

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	confidenceThreshold = 0.68
	defaultListLimit    = 25
	maxListLimit        = 100
	defaultRecommendMax = 5
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

func cleanText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func normalizeComparableText(value string) string {
	value = strings.ToLower(cleanText(value))
	value = strings.NewReplacer("đ", "d", "Đ", "D").Replace(value)
	normalized, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), value)
	if err == nil {
		value = normalized
	}

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
	return strings.Join(strings.Fields(b.String()), " ")
}

func normalizeLocationKey(address, mapLink, district string) string {
	key := normalizeComparableText(strings.Join([]string{address, mapLink}, " "))
	if key != "" {
		return key
	}
	return normalizeComparableText(district)
}

func normalizeLimit(limit, fallback int) int {
	switch {
	case limit <= 0:
		return fallback
	case limit > maxListLimit:
		return maxListLimit
	default:
		return limit
	}
}

func normalizeTag(tag string) string {
	tag = normalizeComparableText(tag)
	switch tag {
	case "re", "binh dan", "budget", "cheap eats":
		return "cheap"
	case "hen ho", "nguoi yeu", "couple", "romantic":
		return "date"
	case "nhom", "team", "party", "dong nguoi":
		return "group"
	case "yen tinh", "cozy", "relax":
		return "chill"
	case "dia phuong", "quan quen":
		return "local"
	case "sang", "fine dining", "expensive":
		return "premium"
	default:
		return tag
	}
}

func normalizeTags(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		tag := normalizeTag(value)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

func mergeTags(groups ...[]string) []string {
	var merged []string
	for _, group := range groups {
		merged = append(merged, group...)
	}
	return normalizeTags(merged)
}

func splitStringList(value string) []string {
	value = strings.NewReplacer("\n", ",", ";", ",").Replace(value)
	parts := strings.Split(value, ",")
	return normalizeTags(parts)
}

func stringListArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	switch value := args[key].(type) {
	case []string:
		return normalizeTags(value)
	case []any:
		values := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
		return normalizeTags(values)
	case string:
		return splitStringList(value)
	default:
		return nil
	}
}

func intArg(args map[string]any, key string) int {
	if args == nil {
		return 0
	}
	switch value := args[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		value = strings.TrimSpace(value)
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return 0
}

func sourceMetaFromToolContext(ctx context.Context) sourceMeta {
	senderID := cleanText(store.SenderIDFromContext(ctx))
	userID := cleanText(store.UserIDFromContext(ctx))
	contributorID := senderID
	if contributorID == "" {
		contributorID = userID
	}
	contributorLabel := contributorID
	if contributorLabel == "" {
		contributorLabel = "unknown"
	}

	return sourceMeta{
		Channel:          cleanText(tools.ToolChannelFromCtx(ctx)),
		ChatID:           cleanText(tools.ToolChatIDFromCtx(ctx)),
		ContributorID:    contributorID,
		ContributorLabel: contributorLabel,
	}
}

func applyContributor(source sourceMeta, contributor string) sourceMeta {
	contributor = cleanText(contributor)
	if contributor == "" {
		return source
	}
	source.ContributorLabel = contributor
	if source.ContributorID == "" || source.ContributorID == "unknown" {
		source.ContributorID = contributor
	}
	return source
}

func toolJSONResult(value any) *tools.Result {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(string(data))
}

func inputFromToolArgs(args map[string]any) IngestRequest {
	return IngestRequest{
		Text:        tools.GetParamString(args, "text", ""),
		Confirm:     tools.GetParamBool(args, "confirm", false),
		Tags:        stringListArg(args, "tags"),
		Contributor: tools.GetParamString(args, "contributor", ""),
	}
}

func confirmFromToolArgs(args map[string]any) ConfirmRequest {
	return ConfirmRequest{
		SuggestionID: tools.GetParamString(args, "suggestion_id", ""),
		Overrides: EateryOverrides{
			Name:      tools.GetParamString(args, "name", ""),
			Address:   tools.GetParamString(args, "address", ""),
			MapLink:   tools.GetParamString(args, "map_link", ""),
			District:  tools.GetParamString(args, "district", ""),
			Category:  tools.GetParamString(args, "category", ""),
			PriceHint: tools.GetParamString(args, "price_hint", ""),
			Tags:      stringListArg(args, "tags"),
			Notes:     tools.GetParamString(args, "notes", ""),
		},
	}
}

func recommendFromToolArgs(args map[string]any) RecommendRequest {
	return RecommendRequest{
		Prompt:    tools.GetParamString(args, "prompt", ""),
		District:  tools.GetParamString(args, "district", ""),
		Category:  tools.GetParamString(args, "category", ""),
		Tags:      stringListArg(args, "tags"),
		MaxBudget: intArg(args, "max_budget"),
		GroupSize: intArg(args, "group_size"),
		Search:    tools.GetParamString(args, "search", ""),
		Limit:     intArg(args, "limit"),
	}
}

func listFromToolArgs(args map[string]any) ListRequest {
	return ListRequest{
		District:  tools.GetParamString(args, "district", ""),
		Category:  tools.GetParamString(args, "category", ""),
		Tag:       tools.GetParamString(args, "tag", ""),
		Search:    tools.GetParamString(args, "search", ""),
		MaxBudget: intArg(args, "max_budget"),
		Limit:     intArg(args, "limit"),
	}
}

func validateParsedForInsert(parsed ParsedEatery) error {
	if cleanText(parsed.Name) == "" {
		return fmt.Errorf("name is required before adding the eatery")
	}
	if cleanText(parsed.Address) == "" && cleanText(parsed.MapLink) == "" && cleanText(parsed.District) == "" {
		return fmt.Errorf("address, map_link, or district is required before adding the eatery")
	}
	return nil
}
