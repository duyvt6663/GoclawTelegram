package russianroulette

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func toolJSONResult(data any) *tools.Result {
	encoded, err := json.Marshal(data)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(string(encoded))
}

func resolveToolIdentity(ctx context.Context, args map[string]any) playerIdentity {
	rawUserID := stringArg(args, "user_id")
	rawLabel := stringArg(args, "user_label")
	if rawUserID == "" {
		if senderID := strings.TrimSpace(store.SenderIDFromContext(ctx)); senderID != "" {
			identity := parsePlayerIdentity(senderID, rawLabel)
			if rawLabel == "" {
				rawLabel = identity.Label
			}
			rawUserID = identity.ID
		}
	}
	if rawUserID == "" {
		rawUserID = strings.TrimSpace(store.UserIDFromContext(ctx))
	}
	return parsePlayerIdentity(rawUserID, rawLabel)
}
