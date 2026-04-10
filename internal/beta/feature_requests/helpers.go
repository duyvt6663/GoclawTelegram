package featurerequests

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const featureRequestsLopTruong = "@duyvt6663"

func tenantKey(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return strings.TrimSpace(id.String())
}

func tenantKeyFromCtx(ctx context.Context) string {
	return tenantKey(store.TenantIDFromContext(ctx))
}

func canDirectApproveFeature(ctx context.Context) bool {
	senderID := strings.TrimSpace(store.SenderIDFromContext(ctx))
	if senderID == "" {
		return false
	}
	return channels.SenderMatchesList(senderID, []string{featureRequestsLopTruong})
}

func outboundMeta(req *FeatureRequest) map[string]string {
	if req == nil {
		return nil
	}
	localKey := strings.TrimSpace(req.LocalKey)
	if localKey == "" {
		return nil
	}
	return map[string]string{"local_key": localKey}
}

func updateYesVoters(voters []string, voterID string, yesVote bool) []string {
	voterID = strings.TrimSpace(voterID)
	if voterID == "" {
		return voters
	}

	out := make([]string, 0, len(voters)+1)
	found := false
	for _, existing := range voters {
		if existing == voterID {
			found = true
			if yesVote {
				out = append(out, existing)
			}
			continue
		}
		out = append(out, existing)
	}
	if yesVote && !found {
		out = append(out, voterID)
	}
	return out
}

func extractOptionIDs(raw any) []int {
	switch v := raw.(type) {
	case []int:
		return append([]int(nil), v...)
	case []any:
		out := make([]int, 0, len(v))
		for _, item := range v {
			switch n := item.(type) {
			case int:
				out = append(out, n)
			case int32:
				out = append(out, int(n))
			case int64:
				out = append(out, int(n))
			case float64:
				out = append(out, int(n))
			}
		}
		return out
	default:
		return nil
	}
}

func hasYesVote(optionIDs []int) bool {
	for _, id := range optionIDs {
		if id == 0 {
			return true
		}
	}
	return false
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "..."
}
