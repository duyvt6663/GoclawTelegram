package dailyichingindexv4

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func tenantKeyFromCtx(ctx context.Context) string {
	tenantID := storepkg.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		return ""
	}
	return strings.TrimSpace(tenantID.String())
}

func toolJSONResult(data any) *tools.Result {
	encoded, err := json.Marshal(data)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(string(encoded))
}

func stringSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok {
		return nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, _ := value.(string)
		text = strings.TrimSpace(text)
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}
