package dailyiching

import (
	"bytes"
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
	bookTextExtractorPDFToText = "pdftotext"
	bookTextExtractorTesseract = "tesseract"

	bookTextExtractorEnv  = "GOCLAW_BETA_DAILY_ICHING_EXTRACTOR"
	bookTessdataDirEnv    = "GOCLAW_BETA_DAILY_ICHING_TESSDATA_DIR"
	localTessdataDirName  = ".daily-iching-tessdata"
	tesseractLangsDefault = "vie+eng"
)

var (
	execLookPath = exec.LookPath
	execCommand  = exec.Command
)

type pdfToTextConfig struct {
	Path      string
	Signature string
}

type tesseractOCRConfig struct {
	TessdataDir string
	Langs       string
	Signature   string
}

func resolveBookTextExtractor(sourceRoot string) (string, bool, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(bookTextExtractorEnv)))
	switch mode {
	case "", bookTextExtractorAuto:
		if cfg, err := resolvePDFToTextConfig(); err == nil {
			return pdfToTextExtractorCacheKey(cfg), false, nil
		}
		if cfg, err := resolveTesseractOCRConfig(sourceRoot); err == nil {
			return tesseractExtractorCacheKey(cfg), false, nil
		}
		return bookTextExtractorPlain, false, nil
	case bookTextExtractorPlain:
		return bookTextExtractorPlain, true, nil
	case bookTextExtractorPDFToText:
		cfg, err := resolvePDFToTextConfig()
		if err != nil {
			return "", true, err
		}
		return pdfToTextExtractorCacheKey(cfg), true, nil
	case bookTextExtractorTesseract:
		cfg, err := resolveTesseractOCRConfig(sourceRoot)
		if err != nil {
			return "", true, err
		}
		return tesseractExtractorCacheKey(cfg), true, nil
	default:
		return "", true, fmt.Errorf("unsupported %s=%q (want auto, plain, pdftotext, or tesseract)", bookTextExtractorEnv, mode)
	}
}

func resolvePDFToTextConfig() (pdfToTextConfig, error) {
	path, err := execLookPath("pdftotext")
	if err != nil {
		return pdfToTextConfig{}, fmt.Errorf("%s backend unavailable: missing pdftotext", bookTextExtractorPDFToText)
	}

	version, err := pdfToTextVersion()
	if err != nil {
		return pdfToTextConfig{}, err
	}
	return pdfToTextConfig{
		Path:      path,
		Signature: hashSignature(path, version),
	}, nil
}

func pdfToTextVersion() (string, error) {
	cmd := execCommand("pdftotext", "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("query %s version: %w (%s)", bookTextExtractorPDFToText, err, cleanSnippet(string(out)))
	}
	version := cleanSnippet(string(out))
	if version == "" {
		version = "unknown"
	}
	return version, nil
}

func pdfToTextExtractorCacheKey(cfg pdfToTextConfig) string {
	signature := strings.TrimSpace(cfg.Signature)
	if signature == "" {
		signature = hashSignature(strings.TrimSpace(cfg.Path))
	}
	return fmt.Sprintf("%s:%s", bookTextExtractorPDFToText, signature)
}

func isPDFToTextExtractor(extractor string) bool {
	return extractor == bookTextExtractorPDFToText || strings.HasPrefix(extractor, bookTextExtractorPDFToText+":")
}

func tesseractOCREnabled(sourceRoot string) bool {
	return ensureTesseractOCRAvailable(sourceRoot) == nil
}

func ensureTesseractOCRAvailable(sourceRoot string) error {
	_, err := resolveTesseractOCRConfig(sourceRoot)
	return err
}

func resolveTesseractOCRConfig(sourceRoot string) (tesseractOCRConfig, error) {
	for _, name := range []string{"gs", "tesseract"} {
		if _, err := execLookPath(name); err != nil {
			return tesseractOCRConfig{}, fmt.Errorf("%s backend unavailable: missing %s", bookTextExtractorTesseract, name)
		}
	}

	tessdataDir, _, err := resolveTesseractDataDir(sourceRoot)
	if err != nil {
		return tesseractOCRConfig{}, err
	}

	if tessdataDir != "" {
		cfg := tesseractOCRConfig{
			TessdataDir: tessdataDir,
			Langs:       "vie",
			Signature:   tessdataDirSignature(tessdataDir),
		}
		if _, err := os.Stat(filepath.Join(tessdataDir, "eng.traineddata")); err == nil {
			cfg.Langs = tesseractLangsDefault
		}
		return cfg, nil
	}

	langs, err := tesseractAvailableLanguages()
	if err != nil {
		return tesseractOCRConfig{}, err
	}
	if _, ok := langs["vie"]; !ok {
		return tesseractOCRConfig{}, fmt.Errorf("%s backend unavailable: tesseract language 'vie' is not installed", bookTextExtractorTesseract)
	}

	cfg := tesseractOCRConfig{Langs: "vie", Signature: "system"}
	if _, ok := langs["eng"]; ok {
		cfg.Langs = tesseractLangsDefault
	}
	return cfg, nil
}

func tesseractExtractorCacheKey(cfg tesseractOCRConfig) string {
	signature := strings.TrimSpace(cfg.Signature)
	if signature == "" {
		signature = "system"
	}
	langs := strings.TrimSpace(cfg.Langs)
	if langs == "" {
		langs = tesseractLangsDefault
	}
	return fmt.Sprintf("%s:%s:%s", bookTextExtractorTesseract, langs, signature)
}

func isTesseractExtractor(extractor string) bool {
	return extractor == bookTextExtractorTesseract || strings.HasPrefix(extractor, bookTextExtractorTesseract+":")
}

func tessdataDirSignature(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return "system"
	}
	parts := []string{filepath.Clean(dir)}
	for _, name := range []string{"vie.traineddata", "eng.traineddata"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s|%d|%d", name, info.Size(), info.ModTime().UnixNano()))
	}
	return hashSignature(parts...)
}

func resolveTesseractDataDir(sourceRoot string) (string, bool, error) {
	explicit := strings.TrimSpace(os.Getenv(bookTessdataDirEnv))
	if explicit != "" {
		if err := validateTessdataDir(explicit); err != nil {
			return "", true, fmt.Errorf("%s=%q: %w", bookTessdataDirEnv, explicit, err)
		}
		return explicit, true, nil
	}

	if root := strings.TrimSpace(sourceRoot); root != "" {
		for _, candidate := range []string{
			filepath.Join(root, localTessdataDirName),
			filepath.Join(root, "tessdata"),
		} {
			if err := validateTessdataDir(candidate); err == nil {
				return candidate, false, nil
			}
		}
	}
	return "", false, nil
}

func validateTessdataDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "vie.traineddata")); err != nil {
		return fmt.Errorf("missing vie.traineddata")
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

func parsePDFSourceWithPDFToText(source bookSourceFile) (sourceDocument, error) {
	cmd := execCommand(
		"pdftotext",
		"-layout",
		"-enc",
		"UTF-8",
		"-nopgbrk",
		source.Path,
		"-",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return sourceDocument{}, fmt.Errorf("extract PDF text with %s %s: %w (%s)", bookTextExtractorPDFToText, source.DisplayPath, err, cleanSnippet(string(out)))
	}
	lines := extractedTextLines(string(out))
	if len(lines) == 0 {
		return sourceDocument{}, fmt.Errorf("extract PDF text with %s %s: no text produced", bookTextExtractorPDFToText, source.DisplayPath)
	}
	return sourceDocument{Source: source, Lines: lines}, nil
}

func parsePDFSourceWithTesseract(source bookSourceFile) (sourceDocument, error) {
	ocrConfig, err := resolveTesseractOCRConfig(filepath.Dir(source.Path))
	if err != nil {
		return sourceDocument{}, err
	}

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
		args := []string{
			page,
			"stdout",
		}
		if ocrConfig.TessdataDir != "" {
			args = append(args, "--tessdata-dir", ocrConfig.TessdataDir)
		}
		args = append(args,
			"-l",
			ocrConfig.Langs,
			"--psm",
			"6",
			"-c",
			"preserve_interword_spaces=1",
		)
		ocrCmd := execCommand(
			"tesseract",
			args...,
		)
		var stderr bytes.Buffer
		ocrCmd.Stderr = &stderr
		out, err := ocrCmd.Output()
		if err != nil {
			return sourceDocument{}, fmt.Errorf("OCR page %s in %s: %w (%s)", filepath.Base(page), source.DisplayPath, err, cleanSnippet(stderr.String()))
		}
		lines = append(lines, extractedTextLines(string(out))...)
		lines = append(lines, "")
	}

	return sourceDocument{Source: source, Lines: lines}, nil
}

func extractedTextLines(text string) []string {
	rawLines := strings.Split(canonicalUnicodeText(text), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		lines = append(lines, strings.TrimRight(line, " \t\r"))
	}
	return lines
}
