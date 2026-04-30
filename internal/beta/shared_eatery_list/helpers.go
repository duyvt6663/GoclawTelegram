package sharedeaterylist

import (
	"context"
	"encoding/json"
	"fmt"
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
	defaultListLimit = 25
	maxListLimit     = 100
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

func normalizeLocationKey(location EateryLocation, district string) string {
	parts := []string{
		location.Address,
		location.MapLink,
	}
	key := normalizeComparableText(strings.Join(parts, " "))
	if key != "" {
		return key
	}
	return normalizeComparableText(district)
}

func normalizeListLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultListLimit
	case limit > maxListLimit:
		return maxListLimit
	default:
		return limit
	}
}

func normalizeFilter(filter EateryFilter) EateryFilter {
	filter.District = cleanText(filter.District)
	filter.Category = cleanText(filter.Category)
	filter.PriceRange = cleanText(filter.PriceRange)
	filter.Search = cleanText(filter.Search)
	filter.Limit = normalizeListLimit(filter.Limit)
	return filter
}

func normalizeStringList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = cleanText(value)
		if value == "" {
			continue
		}
		key := normalizeComparableText(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func splitStringList(value string) []string {
	value = strings.NewReplacer("\n", ",", ";", ",").Replace(value)
	parts := strings.Split(value, ",")
	return normalizeStringList(parts)
}

func stringListArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	switch value := args[key].(type) {
	case []string:
		return normalizeStringList(value)
	case []any:
		values := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
		return normalizeStringList(values)
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
	default:
		return 0
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

func applySource(input *EateryInput, source sourceMeta) {
	if input.SourceChannel == "" {
		input.SourceChannel = source.Channel
	}
	if input.SourceChatID == "" {
		input.SourceChatID = source.ChatID
	}
	if input.ContributorLabel == "" && input.Contributor != "" {
		input.ContributorLabel = input.Contributor
	}
	if input.ContributorID == "" {
		input.ContributorID = input.ContributorLabel
	}
	if input.ContributorLabel == "" {
		input.ContributorLabel = source.ContributorLabel
	}
	if input.ContributorID == "" {
		input.ContributorID = source.ContributorID
	}
	if input.ContributorID == "" && input.ContributorLabel != "" {
		input.ContributorID = input.ContributorLabel
	}
}

func validateEateryInput(input EateryInput) (storedEatery, error) {
	location := input.normalizedLocation()
	name := cleanText(input.Name)
	district := cleanText(input.District)
	category := cleanText(input.Category)
	priceRange := cleanText(input.PriceRange)
	notes := cleanText(input.Notes)
	contributorID := cleanText(input.ContributorID)
	contributorLabel := cleanText(input.ContributorLabel)
	sourceChannel := cleanText(input.SourceChannel)
	sourceChatID := cleanText(input.SourceChatID)

	if name == "" {
		return storedEatery{}, fmt.Errorf("name is required")
	}
	if location.Address == "" && location.MapLink == "" && district == "" {
		return storedEatery{}, fmt.Errorf("one of address, map_link, or district is required")
	}

	nameKey := normalizeComparableText(name)
	locationKey := normalizeLocationKey(location, district)
	if nameKey == "" || locationKey == "" {
		return storedEatery{}, fmt.Errorf("name and location must contain searchable text")
	}

	mustTryDishes := normalizeStringList([]string(input.MustTryDishes))
	imageURLs := normalizeStringList([]string(input.ImageURLs))

	searchKey := normalizeComparableText(strings.Join([]string{
		name,
		location.Address,
		location.MapLink,
		district,
		category,
		strings.Join(mustTryDishes, " "),
		notes,
		priceRange,
		contributorLabel,
	}, " "))

	return storedEatery{
		Name:             name,
		NameKey:          nameKey,
		Location:         location,
		LocationKey:      locationKey,
		District:         district,
		DistrictKey:      normalizeComparableText(district),
		Category:         category,
		CategoryKey:      normalizeComparableText(category),
		MustTryDishes:    mustTryDishes,
		ContributorID:    contributorID,
		ContributorLabel: contributorLabel,
		Notes:            notes,
		PriceRange:       priceRange,
		PriceRangeKey:    normalizeComparableText(priceRange),
		ImageURLs:        imageURLs,
		SourceChannel:    sourceChannel,
		SourceChatID:     sourceChatID,
		SearchKey:        searchKey,
	}, nil
}

func toolJSONResult(value any) *tools.Result {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(string(data))
}

func inputFromToolArgs(args map[string]any) EateryInput {
	return EateryInput{
		Name:          tools.GetParamString(args, "name", ""),
		Address:       tools.GetParamString(args, "address", ""),
		MapLink:       tools.GetParamString(args, "map_link", ""),
		District:      tools.GetParamString(args, "district", ""),
		Category:      tools.GetParamString(args, "category", ""),
		MustTryDishes: stringList(stringListArg(args, "must_try_dishes")),
		Contributor:   tools.GetParamString(args, "contributor", ""),
		Notes:         tools.GetParamString(args, "notes", ""),
		PriceRange:    tools.GetParamString(args, "price_range", ""),
		ImageURLs:     stringList(stringListArg(args, "image_urls")),
	}
}

func filterFromToolArgs(args map[string]any) EateryFilter {
	return EateryFilter{
		District:   tools.GetParamString(args, "district", ""),
		Category:   tools.GetParamString(args, "category", ""),
		PriceRange: tools.GetParamString(args, "price_range", ""),
		Search:     tools.GetParamString(args, "search", ""),
		Limit:      intArg(args, "limit"),
	}
}
