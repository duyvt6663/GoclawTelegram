package dailyiching

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestResolveBookTextExtractorAutoFallsBackToPlainWhenVieMissing(t *testing.T) {
	t.Setenv(bookTextExtractorEnv, bookTextExtractorAuto)
	withStubbedOCRCommands(t, "eng\nosd\n")

	got, forced, err := resolveBookTextExtractor()
	if err != nil {
		t.Fatalf("resolveBookTextExtractor() error = %v", err)
	}
	if forced {
		t.Fatal("resolveBookTextExtractor() forced = true, want false")
	}
	if got != bookTextExtractorPlain {
		t.Fatalf("resolveBookTextExtractor() = %q, want %q", got, bookTextExtractorPlain)
	}
}

func TestResolveBookTextExtractorForcedTesseractRequiresVie(t *testing.T) {
	t.Setenv(bookTextExtractorEnv, bookTextExtractorTesseract)
	withStubbedOCRCommands(t, "eng\nosd\n")

	_, forced, err := resolveBookTextExtractor()
	if err == nil {
		t.Fatal("resolveBookTextExtractor() error = nil, want missing-vie error")
	}
	if !forced {
		t.Fatal("resolveBookTextExtractor() forced = false, want true")
	}
	if !strings.Contains(err.Error(), "vie") {
		t.Fatalf("resolveBookTextExtractor() error = %q, want missing 'vie'", err)
	}
}

func withStubbedOCRCommands(t *testing.T, tesseractLangsOutput string) {
	t.Helper()

	prevLookPath := execLookPath
	prevExecCommand := execCommand
	execLookPath = func(file string) (string, error) {
		switch file {
		case "gs", "tesseract":
			return "/usr/bin/" + file, nil
		default:
			return "", exec.ErrNotFound
		}
	}
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestOCRHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_OCR_HELPER_PROCESS=1",
			"GO_OCR_HELPER_TESSERACT_LANGS="+tesseractLangsOutput,
		)
		return cmd
	}
	t.Cleanup(func() {
		execLookPath = prevLookPath
		execCommand = prevExecCommand
	})
}

func TestOCRHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_OCR_HELPER_PROCESS") != "1" {
		return
	}

	args := os.Args
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep+1 >= len(args) {
		os.Exit(2)
	}

	name := args[sep+1]
	cmdArgs := args[sep+2:]
	switch {
	case name == "tesseract" && len(cmdArgs) == 1 && cmdArgs[0] == "--list-langs":
		_, _ = io.WriteString(os.Stdout, "List of available languages in \"/tmp\" (0):\n")
		_, _ = io.WriteString(os.Stdout, os.Getenv("GO_OCR_HELPER_TESSERACT_LANGS"))
		os.Exit(0)
	default:
		os.Exit(2)
	}
}
