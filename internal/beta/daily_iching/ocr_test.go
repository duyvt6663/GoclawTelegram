package dailyiching

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type extractorStubOptions struct {
	Available        map[string]bool
	TesseractLangs   string
	PDFToTextVersion string
}

func TestResolveBookTextExtractorAutoFallsBackToPlainWhenNoBetterBackend(t *testing.T) {
	t.Setenv(bookTextExtractorEnv, bookTextExtractorAuto)
	withStubbedExtractorCommands(t, extractorStubOptions{})

	got, forced, err := resolveBookTextExtractor(t.TempDir())
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

func TestResolveBookTextExtractorAutoPrefersPDFToText(t *testing.T) {
	t.Setenv(bookTextExtractorEnv, bookTextExtractorAuto)
	withStubbedExtractorCommands(t, extractorStubOptions{
		Available:        map[string]bool{"pdftotext": true},
		PDFToTextVersion: "pdftotext version 26.04.0",
	})

	got, forced, err := resolveBookTextExtractor(t.TempDir())
	if err != nil {
		t.Fatalf("resolveBookTextExtractor() error = %v", err)
	}
	if forced {
		t.Fatal("resolveBookTextExtractor() forced = true, want false")
	}
	if !isPDFToTextExtractor(got) {
		t.Fatalf("resolveBookTextExtractor() = %q, want pdftotext cache key", got)
	}
}

func TestResolveBookTextExtractorForcedTesseractRequiresVie(t *testing.T) {
	t.Setenv(bookTextExtractorEnv, bookTextExtractorTesseract)
	withStubbedExtractorCommands(t, extractorStubOptions{
		Available:      map[string]bool{"tesseract": true, "gs": true},
		TesseractLangs: "eng\nosd\n",
	})

	_, forced, err := resolveBookTextExtractor(t.TempDir())
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

func TestResolveTesseractOCRConfigPrefersLocalTessdataDir(t *testing.T) {
	sourceRoot := t.TempDir()
	tessdataDir := filepath.Join(sourceRoot, localTessdataDirName)
	if err := os.MkdirAll(tessdataDir, 0o755); err != nil {
		t.Fatalf("mkdir tessdata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tessdataDir, "vie.traineddata"), []byte("stub"), 0o644); err != nil {
		t.Fatalf("write vie.traineddata: %v", err)
	}
	withStubbedExtractorCommands(t, extractorStubOptions{
		Available:      map[string]bool{"tesseract": true, "gs": true},
		TesseractLangs: "eng\nvie\n",
	})

	cfg, err := resolveTesseractOCRConfig(sourceRoot)
	if err != nil {
		t.Fatalf("resolveTesseractOCRConfig() error = %v", err)
	}
	if cfg.TessdataDir != tessdataDir {
		t.Fatalf("resolveTesseractOCRConfig() tessdata_dir = %q, want %q", cfg.TessdataDir, tessdataDir)
	}
	if cfg.Langs != "vie" {
		t.Fatalf("resolveTesseractOCRConfig() langs = %q, want %q", cfg.Langs, "vie")
	}
}

func withStubbedExtractorCommands(t *testing.T, opts extractorStubOptions) {
	t.Helper()

	prevLookPath := execLookPath
	prevExecCommand := execCommand
	execLookPath = func(file string) (string, error) {
		if opts.Available[file] {
			return "/usr/bin/" + file, nil
		}
		return "", exec.ErrNotFound
	}
	execCommand = func(name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestOCRHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(os.Args[0], cmdArgs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_OCR_HELPER_PROCESS=1",
			"GO_OCR_HELPER_TESSERACT_LANGS="+opts.TesseractLangs,
			"GO_OCR_HELPER_PDFTOTEXT_VERSION="+opts.PDFToTextVersion,
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
	case name == "pdftotext" && len(cmdArgs) == 1 && cmdArgs[0] == "-v":
		_, _ = io.WriteString(os.Stdout, os.Getenv("GO_OCR_HELPER_PDFTOTEXT_VERSION"))
		os.Exit(0)
	default:
		os.Exit(2)
	}
}
