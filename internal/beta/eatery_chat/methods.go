package eaterychat

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func registerMethods(_ *EateryChatFeature, router *gateway.MethodRouter) {
	router.Register("beta.eatery_chat.status", func(_ context.Context, client *gateway.Client, req *protocol.RequestFrame) {
		client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{
			"feature": featureName,
			"status":  "stub",
		}))
	})
}
