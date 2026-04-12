package dailyichingindexv4

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	dailyiching "github.com/nextlevelbuilder/goclaw/internal/beta/daily_iching"
)

func TestBuildIndexInspectionReportProvidesComparisonOutput(t *testing.T) {
	workspace := findWorkspaceRoot(t)
	report, err := dailyiching.BuildIndexInspectionReport(
		workspace,
		workspace,
		[]string{
			"quân tử tự cường bất tức",
			"hiện long tại điền lợi kiến đại nhân",
			"hình nhi hạ",
		},
		true,
	)
	if err != nil {
		t.Fatalf("BuildIndexInspectionReport() error = %v", err)
	}
	if report.SourceRoot == "" || report.SourceSignature == "" {
		t.Fatalf("report missing source metadata: %#v", report)
	}
	if len(report.Versions) < 3 {
		t.Fatalf("len(report.Versions) = %d, want >= 3", len(report.Versions))
	}
	if len(report.Comparisons) != 3 {
		t.Fatalf("len(report.Comparisons) = %d, want 3", len(report.Comparisons))
	}

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent() error = %v", err)
	}
	t.Logf("index inspection report:\n%s", encoded)
}

func findWorkspaceRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("workspace root not found")
		}
		dir = parent
	}
}
