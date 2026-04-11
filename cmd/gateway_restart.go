package cmd

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

const gatewayRestartGracePeriod = 3 * time.Second

type gatewayRestartState struct {
	mu        sync.Mutex
	requested bool
	reason    string
}

func (s *gatewayRestartState) Request(reason string) bool {
	if s == nil {
		return false
	}
	reason = strings.TrimSpace(reason)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requested {
		return false
	}
	s.requested = true
	s.reason = reason
	return true
}

func (s *gatewayRestartState) Snapshot() (bool, string) {
	if s == nil {
		return false, ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requested, s.reason
}

func gatewayRestartReason(evt bus.Event) string {
	if payload, ok := evt.Payload.(bus.GatewayRestartRequestedPayload); ok {
		return strings.TrimSpace(payload.Reason)
	}
	if payload, ok := evt.Payload.(*bus.GatewayRestartRequestedPayload); ok && payload != nil {
		return strings.TrimSpace(payload.Reason)
	}
	if reason, ok := evt.Payload.(string); ok {
		return strings.TrimSpace(reason)
	}
	return ""
}

func execGatewayRestartIfRequested(state *gatewayRestartState) {
	if requested, reason := state.Snapshot(); !requested {
		return
	} else {
		exePath, err := os.Executable()
		if err != nil {
			slog.Error("gateway restart skipped: resolve executable failed", "error", err, "reason", reason)
			return
		}
		if resolved, resolveErr := filepathEvalSymlinks(exePath); resolveErr == nil && strings.TrimSpace(resolved) != "" {
			exePath = resolved
		}
		argv := append([]string(nil), os.Args...)
		if len(argv) == 0 {
			argv = []string{exePath}
		} else if strings.TrimSpace(argv[0]) == "" {
			argv[0] = exePath
		}
		slog.Info("gateway restart exec", "path", exePath, "reason", reason)
		if err := syscall.Exec(exePath, argv, os.Environ()); err != nil {
			slog.Error("gateway restart exec failed", "error", err, "path", exePath, "reason", reason)
		}
	}
}

// filepathEvalSymlinks is split for testing.
var filepathEvalSymlinks = func(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
