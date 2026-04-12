package dailyiching

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	bookTextExtractorAuto      = "auto"
	bookTextExtractorPlain     = "plain"
	bookTextExtractorTesseract = "tesseract"

	bookTextExtractorEnv = "GOCLAW_BETA_DAILY_ICHING_EXTRACTOR"
	tesseractLangs      = "vie+eng"
)

var (
	execLookPath = exec.LookPath
	execCommand  = exec.Command
)

func resolveBookTextExtractor() (string, bool, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(bookTextExtractorEnv)))
	switch mode {
	case "", bookTextExtractorAuto:
		if tesseractOCREnabled() {
			return bookTextExtractorTesseract, false, nil
		}
		return bookTextExtractorPlain, false, nil
	case bookTextExtractorPlain:
		return bookTextExtractorPlain, true, nil
	case bookTextExtractorTesseract:
		if err := ensureTesseractOCRAvailable(); err != nil {
			return "", true, err
		}
		return bookTextExtractorTesseract, true, nil
	default:
		return "", true, fmt.Errorf("unsupported %s=%q (want auto, plain, or tesseract)", bookTextExtractorEnv, mode)
	}
}

func tesseractOCREnabled() bool {
	return ensureTesseractOCRAvailable() == nil
}

func ensureTesseractOCRAvailable() error {
	for _, name := range []string{"gs", "tesseract"} {
		if _, err := execLookPath(name); err != nil {
			return fmt.Errorf("%s backend unavailable: missing %s", bookTextExtractorTesseract, name)
		}
	}
	langs, err := tesseractAvailableLanguages()
	if err != nil {
		return err
	}
	if _, ok := langs["vie"]; !ok {
		return fmt.Errorf("%s backend unavailable: tesseract language 'vie' is not installed", bookTextExtractorTesseract)
	}
	return nil
}

func tesseractAvailableLanguages() (map[string]struct{}, error) {
	cmd := execCommand("tesseract", "--list-langs")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list tesseract languages: %w (%s)", err, cleanSnippet(string(out)))
	}
	langs := make(map[string]struct{})
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of available languages") {
			continue
		}
		langs[line] = struct{}{}
	}
	if len(langs) == 0 {
		return nil, fmt.Errorf("list tesseract languages: no languages reported")
	}
	return langs, nil
}

func parsePDFSourceWithTesseract(source bookSourceFile) (sourceDocument, error) {
	tmpDir, err := os.MkdirTemp(filepath.Dir(source.Path), ".daily-iching-ocr-*")
	if err != nil {
		return sourceDocument{}, fmt.Errorf("create OCR temp dir %s: %w", source.DisplayPath, err)
	}
	defer os.RemoveAll(tmpDir)

	outputPattern := filepath.Join(tmpDir, "page-%04d.png")
	renderCmd := execCommand(
		"gs",
		"-q",
		"-dSAFER",
		"-dBATCH",
		"-dNOPAUSE",
		"-sDEVICE=pnggray",
		"-r300",
		"-dTextAlphaBits=4",
		"-dGraphicsAlphaBits=4",
		"-sOutputFile="+outputPattern,
		source.Path,
	)
	if out, err := renderCmd.CombinedOutput(); err != nil {
		return sourceDocument{}, fmt.Errorf("render PDF for OCR %s: %w (%s)", source.DisplayPath, err, cleanSnippet(string(out)))
	}

	pages, err := filepath.Glob(filepath.Join(tmpDir, "page-*.png"))
	if err != nil {
		return sourceDocument{}, fmt.Errorf("list OCR pages %s: %w", source.DisplayPath, err)
	}
	sort.Strings(pages)
	if len(pages) == 0 {
		return sourceDocument{}, fmt.Errorf("render PDF for OCR %s: no pages produced", source.DisplayPath)
	}

	lines := make([]string, 0, len(pages)*200)
	for _, page := range pages {
		ocrCmd := execCommand(
			"tesseract",
			page,
			"stdout",
			"-l",
			tesseractLangs,
			"--psm",
			"6",
			"-c",
			"preserve_interword_spaces=1",
			"quiet",
		)
		out, err := ocrCmd.CombinedOutput()
		if err != nil {
			return sourceDocument{}, fmt.Errorf("OCR page %s in %s: %w (%s)", filepath.Base(page), source.DisplayPath, err, cleanSnippet(string(out)))
		}
		for _, line := range strings.Split(string(out), "\n") {
			lines = append(lines, strings.TrimRight(line, " \t\r"))
		}
		lines = append(lines, "")
	}

	return sourceDocument{Source: source, Lines: lines}, nil
}
