package zlibrarymcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
)

type inputError struct {
	message string
}

func (e *inputError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func newInputError(message string) error {
	return &inputError{message: strings.TrimSpace(message)}
}

func isInputError(err error) bool {
	var target *inputError
	return errors.As(err, &target)
}

func tenantKey(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

func tenantKeyFromCtx(ctx context.Context) string {
	return tenantKey(storepkg.TenantIDFromContext(ctx))
}

func normalizeSearchBooksRequest(req SearchBooksRequest) (map[string]any, error) {
	query := normalizeText(req.Query)
	if query == "" {
		return nil, newInputError("query is required")
	}
	count := req.Count
	if count == 0 {
		count = 10
	}
	if count < 1 || count > 25 {
		return nil, newInputError("count must be between 1 and 25")
	}
	if req.FromYear != 0 && req.ToYear != 0 && req.FromYear > req.ToYear {
		return nil, newInputError("from_year must be less than or equal to to_year")
	}

	args := map[string]any{
		"query": query,
		"exact": req.Exact,
		"count": count,
	}
	if req.FromYear != 0 {
		args["fromYear"] = req.FromYear
	}
	if req.ToYear != 0 {
		args["toYear"] = req.ToYear
	}
	if len(req.Languages) > 0 {
		args["languages"] = cleanStringSlice(req.Languages)
	}
	if len(req.Extensions) > 0 {
		args["extensions"] = cleanStringSlice(req.Extensions)
	}
	if len(req.ContentTypes) > 0 {
		args["content_types"] = cleanStringSlice(req.ContentTypes)
	}
	return args, nil
}

func normalizeDownloadBookRequest(req DownloadBookRequest, downloadRoot string) (map[string]any, error) {
	if len(req.BookDetails) == 0 {
		return nil, newInputError("book_details is required")
	}
	outputDir, err := safeOutputDir(downloadRoot, req.OutputSubdir)
	if err != nil {
		return nil, err
	}
	args := map[string]any{
		"bookDetails": req.BookDetails,
		"outputDir":   outputDir,
	}
	if req.ProcessForRAG {
		args["process_for_rag"] = true
	}
	if format := normalizeText(req.ProcessedOutputFormat); format != "" {
		args["processed_output_format"] = format
	}
	return args, nil
}

func safeOutputDir(downloadRoot, subdir string) (string, error) {
	if strings.TrimSpace(downloadRoot) == "" {
		return "", fmt.Errorf("download root is unavailable")
	}
	cleanRoot, err := filepath.Abs(downloadRoot)
	if err != nil {
		return "", err
	}
	subdir = normalizeText(subdir)
	if subdir == "" {
		return cleanRoot, nil
	}
	if filepath.IsAbs(subdir) {
		return "", newInputError("output_subdir must be relative")
	}
	cleanSubdir := filepath.Clean(subdir)
	if cleanSubdir == "." {
		return cleanRoot, nil
	}
	if strings.HasPrefix(cleanSubdir, ".."+string(filepath.Separator)) || cleanSubdir == ".." {
		return "", newInputError("output_subdir cannot traverse outside the feature download directory")
	}
	return filepath.Join(cleanRoot, cleanSubdir), nil
}

func normalizeText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func cleanStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if cleaned := normalizeText(value); cleaned != "" {
			out = append(out, cleaned)
		}
	}
	return out
}

func mustJSON(value any) []byte {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
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

func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func stringSliceArg(args map[string]any, key string) []string {
	switch value := args[key].(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func mapArg(args map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if value, ok := args[key].(map[string]any); ok {
			return value
		}
	}
	return nil
}
