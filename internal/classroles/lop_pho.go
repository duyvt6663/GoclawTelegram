package classroles

import (
	"context"
	"strings"
	"sync"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const LopTruong = "@duyvt6663"

type LopPhoChecker func(ctx context.Context, senderID string) bool

var roleState struct {
	mu      sync.RWMutex
	checker LopPhoChecker
}

func SetLopPhoChecker(checker LopPhoChecker) {
	roleState.mu.Lock()
	roleState.checker = checker
	roleState.mu.Unlock()
}

func IsLopTruong(senderID string) bool {
	senderID = strings.TrimSpace(senderID)
	if senderID == "" {
		return false
	}
	return channels.SenderMatchesList(senderID, []string{LopTruong})
}

func IsLopPho(ctx context.Context, senderID string) bool {
	senderID = strings.TrimSpace(senderID)
	if senderID == "" {
		return false
	}

	roleState.mu.RLock()
	checker := roleState.checker
	roleState.mu.RUnlock()
	if checker == nil {
		return false
	}
	return checker(ctx, senderID)
}

func CanActAsLopTruong(ctx context.Context, senderID string) bool {
	return IsLopTruong(senderID) || IsLopPho(ctx, senderID)
}

func CanCurrentActorActAsLopTruong(ctx context.Context) bool {
	return CanActAsLopTruong(ctx, store.SenderIDFromContext(ctx))
}
