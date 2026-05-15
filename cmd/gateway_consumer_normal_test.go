package cmd

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

func TestResolveInboundUserIDPreservesSkynetWorkflowScope(t *testing.T) {
	msg := bus.InboundMessage{
		Channel:  "builder-bot",
		ChatID:   "-1003865644303:topic:37674",
		PeerKind: "group",
		Metadata: map[string]string{"skynet_workflow": "true"},
	}

	if got := resolveInboundUserID(msg, "group"); got != "" {
		t.Fatalf("resolveInboundUserID() = %q, want empty Skynet workflow user scope", got)
	}
}

func TestResolveInboundUserIDUsesGroupScopeForNormalMessages(t *testing.T) {
	msg := bus.InboundMessage{
		Channel:  "builder-bot",
		ChatID:   "-1003865644303",
		PeerKind: "group",
		UserID:   "1565106682",
		Metadata: map[string]string{},
	}

	if got, want := resolveInboundUserID(msg, "group"), "group:builder-bot:-1003865644303"; got != want {
		t.Fatalf("resolveInboundUserID() = %q, want %q", got, want)
	}
}
