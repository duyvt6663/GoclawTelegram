package linkupwebsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
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

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return normalizeQuery(value)
}

func intArg(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case int:
		return value
	case int8:
		return int(value)
	case int16:
		return int(value)
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		if v, err := value.Int64(); err == nil {
			return int(v)
		}
	}
	return 0
}

func normalizeQuery(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func normalizeQueryKey(value string) string {
	return strings.ToLower(normalizeQuery(value))
}

type searchInputError struct {
	message string
}

func (e *searchInputError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func newSearchInputError(message string) error {
	return &searchInputError{message: strings.TrimSpace(message)}
}

func isSearchInputError(err error) bool {
	var target *searchInputError
	return errors.As(err, &target)
}

func normalizeSearchRequest(request SearchRequest) (resolvedSearchRequest, error) {
	query := normalizeQuery(request.Query)
	if query == "" {
		return resolvedSearchRequest{}, newSearchInputError("query is required")
	}

	mode, depth, err := normalizeSearchMode(request.Mode)
	if err != nil {
		return resolvedSearchRequest{}, err
	}

	topKSources, err := normalizeTopKSources(request.TopKSources)
	if err != nil {
		return resolvedSearchRequest{}, err
	}

	return resolvedSearchRequest{
		Query:       query,
		LookupKey:   buildSearchLookupKey(query, mode, topKSources),
		Mode:        mode,
		Depth:       depth,
		TopKSources: topKSources,
	}, nil
}

func normalizeSearchMode(value string) (string, string, error) {
	switch strings.ToLower(normalizeQuery(value)) {
	case "", searchModeFast:
		return searchModeFast, linkupSearchDepthStandard, nil
	case searchModeDeep:
		return searchModeDeep, linkupSearchDepthDeep, nil
	default:
		return "", "", newSearchInputError("mode must be one of: fast, deep")
	}
}

func normalizeTopKSources(value int) (int, error) {
	switch {
	case value == 0:
		return defaultSearchMaxResults, nil
	case value < 0:
		return 0, newSearchInputError("top_k_sources must be greater than 0")
	case value > maxTopKSources:
		return 0, newSearchInputError(fmt.Sprintf("top_k_sources must be between 1 and %d", maxTopKSources))
	default:
		return value, nil
	}
}

func buildSearchLookupKey(query, mode string, topKSources int) string {
	return fmt.Sprintf("%s|%d|%s", strings.TrimSpace(mode), topKSources, normalizeQueryKey(query))
}

func trimRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "..."
}

func formatToolPayload(payload *SearchPayload) string {
	if payload == nil {
		return tools.WrapExternalContent("No Linkup search results were returned.", "Linkup Web Search", false)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Linkup search results for: %s\n\n", payload.Query))
	var meta []string
	if payload.Mode != "" {
		meta = append(meta, fmt.Sprintf("mode=%s", payload.Mode))
	}
	if payload.TopKSources > 0 {
		meta = append(meta, fmt.Sprintf("top_k_sources=%d", payload.TopKSources))
	}
	if payload.Cached {
		meta = append(meta, "cached=true")
	}
	if len(meta) > 0 {
		sb.WriteString(strings.Join(meta, " | "))
		sb.WriteString("\n\n")
	}
	if payload.Answer != "" {
		sb.WriteString("Answer:\n")
		sb.WriteString(trimRunes(payload.Answer, 1400))
		sb.WriteString("\n\n")
	}
	if len(payload.Sources) > 0 {
		sb.WriteString("Sources:\n")
		for i, source := range payload.Sources {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, sourceTitle(source)))
			sb.WriteString(fmt.Sprintf("   %s\n", source.URL))
			if source.Snippet != "" {
				sb.WriteString(fmt.Sprintf("   %s\n", trimRunes(source.Snippet, 260)))
			}
			if i < len(payload.Sources)-1 {
				sb.WriteByte('\n')
			}
		}
	}

	return tools.WrapExternalContent(sb.String(), "Linkup Web Search", false)
}

func sourceTitle(source SearchSource) string {
	if title := strings.TrimSpace(source.Title); title != "" {
		return title
	}
	if url := strings.TrimSpace(source.URL); url != "" {
		return url
	}
	return "Untitled source"
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}
