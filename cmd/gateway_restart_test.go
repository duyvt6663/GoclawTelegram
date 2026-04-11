package cmd

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

func TestGatewayRestartStateRecordsFirstReasonOnly(t *testing.T) {
	state := &gatewayRestartState{}
	if !state.Request("first restart") {
		t.Fatal("expected first restart request to be accepted")
	}
	if state.Request("second restart") {
		t.Fatal("expected duplicate restart request to be ignored")
	}
	requested, reason := state.Snapshot()
	if !requested {
		t.Fatal("expected restart to be marked requested")
	}
	if reason != "first restart" {
		t.Fatalf("restart reason = %q, want %q", reason, "first restart")
	}
}

func TestGatewayRestartReasonExtractsTypedPayload(t *testing.T) {
	evt := bus.Event{
		Name:    bus.TopicGatewayRestartRequested,
		Payload: bus.GatewayRestartRequestedPayload{Reason: "auto deploy"},
	}
	if got := gatewayRestartReason(evt); got != "auto deploy" {
		t.Fatalf("gatewayRestartReason() = %q, want %q", got, "auto deploy")
	}
}
