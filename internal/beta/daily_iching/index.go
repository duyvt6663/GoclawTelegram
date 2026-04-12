package dailyiching

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	pdf "github.com/ledongthuc/pdf"
)

type bookIndex struct {
	Version         int               `json:"version"`
	IndexVersion    int               `json:"index_version,omitempty"`
	GeneratedAt     time.Time         `json:"generated_at"`
	SourceRoot      string            `json:"source_root"`
	SourceSignature string            `json:"source_signature,omitempty"`
	Extractor       string            `json:"extractor"`
	Sources         []bookSourceFile  `json:"sources"`
	Sections        []hexagramSection `json:"sections"`

	byNumber map[int]*hexagramSection `json:"-"`
}

type bookSourceFile struct {
	Path        string `json:"path"`
	DisplayPath string `json:"display_path"`
	Signature   string `json:"signature"`
	VolumeOrder int    `json:"volume_order"`
}

type hexagramSection struct {
	Number        int         `json:"number"`
	Name          string      `json:"name"`
	Title         string      `json:"title"`
	SourcePath    string      `json:"source_path"`
	DisplaySource string      `json:"display_source"`
	Heading       string      `json:"heading"`
	Text          string      `json:"text"`
	Chunks        []bookChunk `json:"chunks"`
}

type bookChunk struct {
	Order      int      `json:"order"`
	Text       string   `json:"text"`
	Normalized string   `json:"normalized"`
	Tokens     []string `json:"tokens"`
	HasHa      bool     `json:"has_ha"`
	HasThuong  bool     `json:"has_thuong"`
}

type sourceDocument struct {
	Source bookSourceFile
	Lines  []string
}

func resolveBookSourceRoot(workspace string) (string, error) {
	var candidates []string
	if workspace != "" {
		candidates = append(candidates, filepath.Join(workspace, "builder-bot", "data"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "builder-bot", "data"))
	}
	candidates = append(candidates, filepath.Join("builder-bot", "data"))

	for _, candidate := range uniqueStrings(candidates) {
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		files, err := listBookSourceFiles(candidate)
		if err == nil && len(files) >= 2 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("builder-bot/data with the two local PDF books was not found")
}

func resolveBookCachePath(workspace, dataDir string) (string, error) {
	return resolveBookCachePathForVersion(workspace, dataDir, bookIndexVersion)
}

func resolveBookCachePathForVersion(workspace, dataDir string, version int) (string, error) {
	candidates := make([]string, 0, 4)
	if root := strings.TrimSpace(dataDir); root != "" {
		candidates = append(candidates, root)
	}
	if root := strings.TrimSpace(workspace); root != "" {
		candidates = append(candidates, root)
	}
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		candidates = append(candidates, wd)
	}
	candidates = append(candidates, os.TempDir())

	var lastErr error
	for _, root := range uniqueStrings(candidates) {
		cacheDir := filepath.Join(root, "beta_cache", featureName)
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			lastErr = err
			continue
		}
		return filepath.Join(cacheDir, fmt.Sprintf("book_index_v%d.json", version)), nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("cache root unavailable")
}

func loadOrBuildBookIndex(sourceRoot, cachePath string) (*bookIndex, error) {
	return loadOrBuildBookIndexWithOptions(sourceRoot, cachePath, false)
}

func loadOrBuildBookIndexWithOptions(sourceRoot, cachePath string, rebuild bool) (*bookIndex, error) {
	sources, err := listBookSourceFiles(sourceRoot)
	if err != nil {
		return nil, err
	}
	extractor, forced, err := resolveBookTextExtractor(sourceRoot)
	if err != nil {
		return nil, err
	}
	if !rebuild {
		if cached, err := loadCachedBookIndex(cachePath); err == nil && cacheMatchesSources(cached, sourceRoot, sources, extractor) {
			return prepareBookIndex(cached), nil
		}
	}

	index, err := buildBookIndexUsingExtractor(sourceRoot, sources, extractor, forced)
	if err != nil {
		return nil, err
	}
	if err := persistBookIndex(cachePath, index); err != nil {
		return nil, err
	}
	return prepareBookIndex(index), nil
}

func loadCachedBookIndex(path string) (*bookIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var index bookIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	return prepareBookIndex(&index), nil
}

func persistBookIndex(path string, index *bookIndex) error {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func cacheMatchesSources(index *bookIndex, sourceRoot string, sources []bookSourceFile, extractor string) bool {
	if index == nil || index.effectiveVersion() != bookIndexVersion || len(index.Sources) != len(sources) {
		return false
	}
	if index.SourceSignature != "" && index.SourceSignature != computeBookSourceSignature(sources) {
		return false
	}
	if index.SourceSignature == "" && filepath.Clean(index.SourceRoot) != filepath.Clean(sourceRoot) {
		return false
	}
	if strings.TrimSpace(index.Extractor) != strings.TrimSpace(extractor) {
		return false
	}
	for i := range sources {
		if index.Sources[i].DisplayPath != sources[i].DisplayPath || index.Sources[i].Signature != sources[i].Signature {
			return false
		}
	}
	return true
}

func (idx *bookIndex) effectiveVersion() int {
	if idx == nil {
		return 0
	}
	if idx.IndexVersion > 0 {
		return idx.IndexVersion
	}
	return idx.Version
}

func (idx *bookIndex) effectiveSourceSignature() string {
	if idx == nil {
		return ""
	}
	if strings.TrimSpace(idx.SourceSignature) != "" {
		return strings.TrimSpace(idx.SourceSignature)
	}
	return computeBookSourceSignature(idx.Sources)
}

func computeBookSourceSignature(sources []bookSourceFile) string {
	parts := make([]string, 0, len(sources))
	for _, source := range sources {
		parts = append(parts, fmt.Sprintf("%s|%s|%d", source.DisplayPath, source.Signature, source.VolumeOrder))
	}
	return hashSignature(parts...)
}

func loadCachedBookIndexForVersion(workspace, dataDir string, version int) (*bookIndex, string, error) {
	cachePath, err := resolveBookCachePathForVersion(workspace, dataDir, version)
	if err != nil {
		return nil, "", err
	}
	index, err := loadCachedBookIndex(cachePath)
	if err != nil {
		return nil, cachePath, err
	}
	return index, cachePath, nil
}

func listBookSourceFiles(root string) ([]bookSourceFile, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	var out []bookSourceFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.ToLower(filepath.Ext(entry.Name())) != ".pdf" {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, bookSourceFile{
			Path:        path,
			DisplayPath: relativeOrBase(root, path),
			Signature:   hashSignature(entry.Name(), fmt.Sprintf("%d", info.Size()), fmt.Sprintf("%d", info.ModTime().UnixNano())),
			VolumeOrder: volumeOrderForSource(entry.Name()),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].VolumeOrder != out[j].VolumeOrder {
			return out[i].VolumeOrder < out[j].VolumeOrder
		}
		return out[i].Path < out[j].Path
	})
	if len(out) == 0 {
		return nil, fmt.Errorf("no PDF sources found in %s", root)
	}
	return out, nil
}

func volumeOrderForSource(name string) int {
	normalized := normalizeComparableText(name)
	switch {
	case strings.Contains(normalized, "thuong"):
		return 0
	case strings.Contains(normalized, "ha"):
		return 1
	default:
		return 2
	}
}

func buildBookIndex(sourceRoot string, sources []bookSourceFile) (*bookIndex, error) {
	extractor, forced, err := resolveBookTextExtractor(sourceRoot)
	if err != nil {
		return nil, err
	}
	return buildBookIndexUsingExtractor(sourceRoot, sources, extractor, forced)
}

func buildBookIndexUsingExtractor(sourceRoot string, sources []bookSourceFile, extractor string, forced bool) (*bookIndex, error) {
	index, err := buildBookIndexWithExtractor(sourceRoot, sources, extractor)
	if err == nil || forced {
		return index, err
	}
	for _, fallback := range fallbackBookTextExtractors(sourceRoot, extractor) {
		index, err = buildBookIndexWithExtractor(sourceRoot, sources, fallback)
		if err == nil {
			return index, nil
		}
	}
	return nil, err
}

func fallbackBookTextExtractors(sourceRoot, extractor string) []string {
	switch {
	case isPDFToTextExtractor(extractor):
		fallbacks := make([]string, 0, 2)
		if cfg, err := resolveTesseractOCRConfig(sourceRoot); err == nil {
			fallbacks = append(fallbacks, tesseractExtractorCacheKey(cfg))
		}
		fallbacks = append(fallbacks, bookTextExtractorPlain)
		return uniqueStrings(fallbacks)
	case isTesseractExtractor(extractor):
		return []string{bookTextExtractorPlain}
	default:
		return nil
	}
}

func buildBookIndexWithExtractor(sourceRoot string, sources []bookSourceFile, extractor string) (*bookIndex, error) {
	docs := make([]sourceDocument, 0, len(sources))
	for _, source := range sources {
		doc, err := parsePDFSource(source, extractor)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].Source.VolumeOrder != docs[j].Source.VolumeOrder {
			return docs[i].Source.VolumeOrder < docs[j].Source.VolumeOrder
		}
		return docs[i].Source.Path < docs[j].Source.Path
	})

	var sections []hexagramSection
	for _, doc := range docs {
		startNumber, endNumber := 1, len(kingWenSequence)
		switch doc.Source.VolumeOrder {
		case 0:
			endNumber = 30
		case 1:
			startNumber = 31
		}
		docSections, err := extractSectionsFromDocument(sourceRoot, doc, startNumber, endNumber)
		if err != nil {
			return nil, err
		}
		sections = append(sections, docSections...)
	}
	sort.Slice(sections, func(i, j int) bool { return sections[i].Number < sections[j].Number })
	if len(sections) != len(kingWenSequence) {
		return nil, fmt.Errorf("expected %d hexagram sections, found %d", len(kingWenSequence), len(sections))
	}

	return &bookIndex{
		Version:         bookIndexVersion,
		IndexVersion:    bookIndexVersion,
		GeneratedAt:     time.Now().UTC(),
		SourceRoot:      sourceRoot,
		SourceSignature: computeBookSourceSignature(sources),
		Extractor:       extractor,
		Sources:         sources,
		Sections:        sections,
	}, nil
}

func parsePDFSource(source bookSourceFile, extractor string) (sourceDocument, error) {
	if isPDFToTextExtractor(extractor) {
		return parsePDFSourceWithPDFToText(source)
	}
	if isTesseractExtractor(extractor) {
		return parsePDFSourceWithTesseract(source)
	}
	return parsePDFSourceWithPlainText(source)
}

func parsePDFSourceWithPlainText(source bookSourceFile) (sourceDocument, error) {
	file, reader, err := pdf.Open(source.Path)
	if err != nil {
		return sourceDocument{}, fmt.Errorf("open PDF %s: %w", source.DisplayPath, err)
	}
	defer file.Close()

	rc, err := reader.GetPlainText()
	if err != nil {
		return sourceDocument{}, fmt.Errorf("extract PDF text %s: %w", source.DisplayPath, err)
	}
	textBytes, err := io.ReadAll(rc)
	if err != nil {
		return sourceDocument{}, fmt.Errorf("read PDF text %s: %w", source.DisplayPath, err)
	}

	return sourceDocument{Source: source, Lines: extractedTextLines(string(textBytes))}, nil
}

func extractSectionsFromDocument(sourceRoot string, doc sourceDocument, startNumber, endNumber int) ([]hexagramSection, error) {
	metas := kingWenSequence[startNumber-1 : endNumber]
	starts := make([]int, 0, len(metas))
	linePos := 0
	for _, meta := range metas {
		idx := findHexagramStart(doc.Lines, linePos, meta)
		if idx < 0 {
			return nil, fmt.Errorf("could not find quẻ %d %s in %s", meta.Number, meta.Name, doc.Source.DisplayPath)
		}
		starts = append(starts, idx)
		linePos = idx + 1
	}

	sections := make([]hexagramSection, 0, len(metas))
	for i, meta := range metas {
		start := starts[i]
		end := len(doc.Lines)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		heading := sectionHeading(doc.Lines, start)
		text := strings.TrimSpace(strings.Join(doc.Lines[start:end], "\n"))
		sections = append(sections, hexagramSection{
			Number:        meta.Number,
			Name:          meta.Name,
			Title:         meta.Title,
			SourcePath:    doc.Source.Path,
			DisplaySource: relativeOrBase(sourceRoot, doc.Source.Path),
			Heading:       heading,
			Text:          text,
			Chunks:        chunkHexagramText(text),
		})
	}
	return sections, nil
}

func findHexagramStart(lines []string, from int, meta hexagramMeta) int {
	if from < 0 {
		from = 0
	}

	if idx := findHexagramStartWithPrefix(lines, from, meta); idx >= 0 {
		return idx
	}
	return findHexagramStartWithoutPrefix(lines, from, meta)
}

func findHexagramStartWithPrefix(lines []string, from int, meta hexagramMeta) int {
	prefix := fmt.Sprintf("%d.", meta.Number)
	titleTokens := tokenizeComparableText(meta.Title)
	headingTitleTokens := tokenizeComparableText("Bát " + meta.Title)
	headingSkeletons := expectedHeadingSkeletons(meta)
	requiredTitleHits := requiredMatchThreshold(titleTokens, 2)
	requiredHeadingTitleHits := requiredMatchThreshold(headingTitleTokens, 2)
	fallback := -1

	for i := from; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		lineTail, ok := prefixedHeadingTail(lines, i)
		if !ok {
			continue
		}
		if fallback < 0 {
			fallback = i
		}
		lineNorm := normalizeComparableText(lineTail)
		lineTitleHits := countTokenHits(lineNorm, headingTitleTokens)
		lineSkeleton := consonantSkeletonComparableText(lineTail)
		hasNameContext, titleHits := scoreHexagramWindow(lines, i, meta, titleTokens)
		if hasStrongSkeletonMatch(lineSkeleton, headingSkeletons) ||
			hasNameContext ||
			titleHits >= requiredTitleHits ||
			lineTitleHits >= requiredHeadingTitleHits {
			return i
		}
	}
	return fallback
}

func findHexagramStartWithoutPrefix(lines []string, from int, meta hexagramMeta) int {
	titleTokens := tokenizeComparableText(meta.Title)
	headingTitleTokens := tokenizeComparableText("Bát " + meta.Title)
	requiredTitleHits := requiredMatchThreshold(titleTokens, 2)
	requiredHeadingTitleHits := requiredMatchThreshold(headingTitleTokens, 2)
	headingSkeletons := expectedHeadingSkeletons(meta)

	for i := from; i < len(lines); i++ {
		lineTail, ok := standaloneHeadingTail(lines[i])
		if !ok {
			continue
		}
		lineNorm := normalizeComparableText(lineTail)
		lineTitleHits := countTokenHits(lineNorm, headingTitleTokens)
		lineSkeleton := consonantSkeletonComparableText(lineTail)
		hasNameContext, titleHits := scoreHexagramWindow(lines, i, meta, titleTokens)
		if hasStrongSkeletonMatch(lineSkeleton, headingSkeletons) ||
			lineTitleHits >= requiredHeadingTitleHits ||
			hasNameContext ||
			titleHits >= requiredTitleHits {
			return i
		}
	}
	return -1
}

func scoreHexagramWindow(lines []string, start int, meta hexagramMeta, titleTokens []string) (bool, int) {
	nameNorm := normalizeComparableText(meta.Name)
	titleNorm := normalizeComparableText(meta.Title)
	hasNameContext := false
	titleHits := 0
	nonEmpty := 0
	for j := start; j < len(lines) && nonEmpty < 64; j++ {
		candidate := cleanSourceLine(lines[j])
		if candidate == "" {
			continue
		}
		nonEmpty++
		candidateNorm := normalizeComparableText(candidate)
		if candidateNorm == nameNorm ||
			strings.HasPrefix(candidateNorm, "que "+nameNorm) ||
			strings.Contains(candidateNorm, " que "+nameNorm) {
			hasNameContext = true
		}
		if strings.Contains(candidateNorm, titleNorm) {
			titleHits = len(titleTokens)
			continue
		}
		hits := countTokenHits(candidateNorm, titleTokens)
		if hits > titleHits {
			titleHits = hits
		}
	}
	return hasNameContext, titleHits
}

func isLikelyHexagramHeadingLine(line string) bool {
	return isLikelyHeadingText(headingTail(line))
}

func prefixedHeadingTail(lines []string, start int) (string, bool) {
	if start < 0 || start >= len(lines) {
		return "", false
	}
	tail := headingTail(lines[start])
	if isLikelyHeadingText(tail) {
		return tail, true
	}
	if strings.TrimSpace(tail) != "" {
		return "", false
	}
	for i := start + 1; i < len(lines) && i <= start+3; i++ {
		candidate := cleanSourceLine(lines[i])
		if candidate == "" {
			continue
		}
		if shouldSkipHeadingScanLine(candidate) {
			continue
		}
		if isLikelyHeadingText(candidate) {
			return candidate, true
		}
		break
	}
	return "", false
}

func headingTail(line string) string {
	line = cleanSourceLine(line)
	matches := headingPrefixRe.FindStringIndex(line)
	if len(matches) != 2 {
		return ""
	}
	return strings.TrimSpace(line[matches[1]:])
}

func standaloneHeadingTail(line string) (string, bool) {
	line = cleanSourceLine(line)
	if line == "" || len([]rune(line)) > 120 {
		return "", false
	}
	if shouldSkipHeadingScanLine(line) {
		return "", false
	}
	if headingPrefixRe.MatchString(line) {
		tail := headingTail(line)
		if !isLikelyHeadingText(tail) {
			return "", false
		}
		return tail, true
	}
	tail := strings.TrimLeftFunc(line, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsDigit(r) || strings.ContainsRune(".:-_()[]{}", r)
	})
	if !isLikelyHeadingText(tail) {
		return "", false
	}
	return tail, true
}

func isLikelyHeadingText(tail string) bool {
	tail = cleanSourceLine(tail)
	if tail == "" {
		return false
	}
	if len(strings.Fields(normalizeComparableText(tail))) < 2 {
		return false
	}

	letters := 0
	uppercase := 0
	for _, r := range tail {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.IsUpper(r) || r == '�' {
			uppercase++
		}
	}
	if letters == 0 {
		return false
	}
	return float64(uppercase)/float64(letters) >= 0.6
}

func sectionHeading(lines []string, start int) string {
	if start < 0 || start >= len(lines) {
		return ""
	}
	heading := cleanSourceLine(lines[start])
	if heading == "" {
		return ""
	}
	if strings.TrimSpace(headingTail(heading)) != "" {
		return heading
	}
	if nextTail, ok := prefixedHeadingTail(lines, start); ok {
		return strings.TrimSpace(heading + " " + nextTail)
	}
	return heading
}

func expectedHeadingSkeletons(meta hexagramMeta) []string {
	return uniqueStrings([]string{
		consonantSkeletonComparableText("Bát " + meta.Title),
		consonantSkeletonComparableText(meta.Title),
	})
}

func requiredMatchThreshold(tokens []string, max int) int {
	required := max
	if len(tokens) < required {
		required = len(tokens)
	}
	if required == 0 {
		return 1
	}
	return required
}

func hasStrongSkeletonMatch(candidate string, expected []string) bool {
	if len(candidate) < 3 {
		return false
	}
	for _, want := range expected {
		if len(want) < 3 {
			continue
		}
		if candidate == want || strings.Contains(candidate, want) || strings.Contains(want, candidate) {
			return true
		}
		shorter, longer := candidate, want
		if len(shorter) > len(longer) {
			shorter, longer = longer, shorter
		}
		if len(shorter) >= 3 && float64(len(shorter))/float64(len(longer)) >= 0.75 && isSubsequence(shorter, longer) {
			return true
		}
	}
	return false
}

func isSubsequence(needle, haystack string) bool {
	if needle == "" {
		return true
	}
	j := 0
	for _, r := range haystack {
		if j >= len(needle) {
			break
		}
		if byte(r) == needle[j] {
			j++
		}
	}
	return j == len(needle)
}

func normalizedWindow(lines []string, start, nonEmptyLimit int) string {
	var parts []string
	for i := start; i < len(lines) && len(parts) < nonEmptyLimit; i++ {
		line := cleanSourceLine(lines[i])
		if line == "" {
			continue
		}
		parts = append(parts, line)
	}
	return normalizeComparableText(strings.Join(parts, " "))
}

const (
	bookChunkParagraphFloor = 140
	bookChunkMinRunes       = 96
	bookChunkTargetRunes    = 360
	bookChunkMaxRunes       = 540
)

func chunkHexagramText(text string) []bookChunk {
	paragraphs := semanticParagraphs(text)
	if len(paragraphs) == 0 {
		return nil
	}

	chunks := make([]bookChunk, 0, len(paragraphs))
	appendChunk := func(raw string) {
		raw = cleanSnippet(raw)
		raw = whitespaceCollapse.ReplaceAllString(raw, " ")
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		normalized := normalizeComparableText(raw)
		if normalized == "" {
			return
		}
		if len(chunks) > 0 && chunks[len(chunks)-1].Normalized == normalized {
			return
		}
		chunks = append(chunks, bookChunk{
			Order:      len(chunks),
			Text:       raw,
			Normalized: normalized,
			Tokens:     tokenizeComparableText(raw),
			HasHa:      strings.Contains(normalized, "hinh nhi ha"),
			HasThuong:  strings.Contains(normalized, "hinh nhi thuong"),
		})
	}

	for _, paragraph := range paragraphs {
		for _, chunkText := range splitParagraphIntoChunkTexts(paragraph) {
			appendChunk(chunkText)
		}
	}
	return chunks
}

func semanticParagraphs(text string) []string {
	lines := strings.Split(canonicalUnicodeText(text), "\n")
	lines = trimLeadingSectionNoiseLines(lines)
	var (
		paragraphs []string
		buffer     []string
		bufferSize int
	)

	flush := func() {
		if len(buffer) == 0 {
			return
		}
		raw := cleanSnippet(strings.Join(buffer, " "))
		raw = whitespaceCollapse.ReplaceAllString(raw, " ")
		raw = strings.TrimSpace(raw)
		if raw != "" {
			paragraphs = append(paragraphs, raw)
		}
		buffer = nil
		bufferSize = 0
	}

	for _, rawLine := range lines {
		line := cleanSourceLine(rawLine)
		switch {
		case line == "":
			if bufferSize >= bookChunkParagraphFloor {
				flush()
			}
			continue
		case shouldSkipSourceLine(line):
			if bufferSize >= bookChunkParagraphFloor {
				flush()
			}
			continue
		}

		if len(buffer) > 0 && isSemanticBoundaryLine(line) && (bufferSize >= bookChunkParagraphFloor || endsWithSentencePunctuation(buffer[len(buffer)-1])) {
			flush()
		}

		buffer = append(buffer, line)
		bufferSize += runeLen(line) + 1
		if bufferSize >= bookChunkMaxRunes && sentenceBoundaryLikeLine(line) {
			flush()
		}
	}
	flush()
	return paragraphs
}

func trimLeadingSectionNoiseLines(lines []string) []string {
	start := 0
	for trimmed := 0; start < len(lines) && trimmed < 16; trimmed++ {
		line := cleanSourceLine(lines[start])
		switch {
		case line == "":
			start++
			continue
		case shouldSkipSourceLine(line), isLikelyHexagramHeadingLine(line), isLikelyShortOCRArtifactLine(line):
			start++
			continue
		case isLikelyReadableOpeningLine(line):
			return lines[start:]
		case runeLen(line) < 24 && !endsWithSentencePunctuation(line):
			start++
			continue
		default:
			return lines[start:]
		}
	}
	if start >= len(lines) {
		return nil
	}
	return lines[start:]
}

func isLikelyReadableOpeningLine(line string) bool {
	if isLikelyNoisyOCRText(line) {
		return false
	}
	tokens := strings.Fields(normalizeComparableText(line))
	switch {
	case len(tokens) >= 4 && runeLen(line) >= 16:
		return true
	case len(tokens) >= 3 && runeLen(line) >= 24:
		return true
	default:
		return false
	}
}

func splitParagraphIntoChunkTexts(paragraph string) []string {
	paragraph = cleanSnippet(paragraph)
	if paragraph == "" {
		return nil
	}
	if runeLen(paragraph) <= bookChunkMaxRunes {
		return []string{paragraph}
	}

	sentences := splitSentences(paragraph)
	if len(sentences) <= 1 {
		return splitChunkTextByWords(paragraph, bookChunkTargetRunes)
	}

	var (
		chunks     []string
		buffer     []string
		bufferSize int
	)
	flush := func() {
		if len(buffer) == 0 {
			return
		}
		raw := cleanSnippet(strings.Join(buffer, " "))
		raw = whitespaceCollapse.ReplaceAllString(raw, " ")
		raw = strings.TrimSpace(raw)
		if raw != "" {
			chunks = append(chunks, raw)
		}
		buffer = nil
		bufferSize = 0
	}

	for _, sentence := range sentences {
		sentence = cleanSnippet(sentence)
		if sentence == "" {
			continue
		}
		sentenceSize := runeLen(sentence)
		if sentenceSize > bookChunkMaxRunes {
			flush()
			chunks = append(chunks, splitChunkTextByWords(sentence, bookChunkTargetRunes)...)
			continue
		}
		if bufferSize > 0 && bufferSize+1+sentenceSize > bookChunkMaxRunes {
			flush()
		}
		buffer = append(buffer, sentence)
		bufferSize += sentenceSize + 1
		if bufferSize >= bookChunkTargetRunes && sentenceBoundaryLikeLine(sentence) {
			flush()
		}
	}
	flush()
	if len(chunks) == 0 {
		return []string{paragraph}
	}
	return mergeShortChunkTexts(chunks)
}

func splitChunkTextByWords(value string, targetRunes int) []string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return nil
	}

	var (
		out         []string
		b           strings.Builder
		currentSize int
	)
	flush := func() {
		raw := strings.TrimSpace(b.String())
		if raw != "" {
			out = append(out, raw)
		}
		b.Reset()
		currentSize = 0
	}

	for _, field := range fields {
		fieldSize := runeLen(field)
		extra := fieldSize
		if currentSize > 0 {
			extra++
		}
		if currentSize > 0 && currentSize+extra > bookChunkMaxRunes {
			flush()
		}
		if currentSize > 0 {
			b.WriteByte(' ')
			currentSize++
		}
		b.WriteString(field)
		currentSize += fieldSize
		if currentSize >= targetRunes && endsWithSentencePunctuation(field) {
			flush()
		}
	}
	flush()
	return mergeShortChunkTexts(out)
}

func mergeShortChunkTexts(chunks []string) []string {
	if len(chunks) <= 1 {
		return chunks
	}
	out := make([]string, 0, len(chunks))
	for i := 0; i < len(chunks); i++ {
		current := cleanSnippet(chunks[i])
		if current == "" {
			continue
		}
		if runeLen(current) < bookChunkMinRunes {
			switch {
			case len(out) > 0 && runeLen(out[len(out)-1])+1+runeLen(current) <= bookChunkMaxRunes:
				out[len(out)-1] = cleanSnippet(out[len(out)-1] + " " + current)
				continue
			case i+1 < len(chunks):
				merged := cleanSnippet(current + " " + chunks[i+1])
				if merged != "" && runeLen(merged) <= bookChunkMaxRunes {
					out = append(out, merged)
					i++
					continue
				}
			}
		}
		out = append(out, current)
	}
	return out
}

func isSemanticBoundaryLine(line string) bool {
	normalized := normalizeComparableText(line)
	if normalized == "" {
		return false
	}
	if isLikelyHexagramHeadingLine(line) {
		return true
	}
	for _, prefix := range []string{
		"hinh nhi ha",
		"hinh nhi thuong",
		"dai tuong",
		"tieu tuong",
		"thoan",
		"tuong viet",
		"que viet",
		"van ngon",
		"dung cuu",
		"dung luc",
	} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	tokens := strings.Fields(normalized)
	if len(tokens) < 2 {
		return false
	}
	switch tokens[0] {
	case "so":
		return tokens[1] == "cuu" || tokens[1] == "luc"
	case "cuu", "luc":
		return isLineOrdinalComparableToken(tokens[1])
	case "thuong", "dung":
		return tokens[1] == "cuu" || tokens[1] == "luc"
	}
	return false
}

func isLineOrdinalComparableToken(token string) bool {
	switch token {
	case "1", "2", "3", "4", "5", "6", "nhat", "nhi", "tam", "tu", "ngu", "luc":
		return true
	default:
		return false
	}
}

func sentenceBoundaryLikeLine(line string) bool {
	if line == "" {
		return false
	}
	if isSemanticBoundaryLine(line) {
		return true
	}
	return endsWithSentencePunctuation(line)
}

func endsWithSentencePunctuation(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	runes := []rune(value)
	switch runes[len(runes)-1] {
	case '.', '!', '?', ';':
		return true
	default:
		return false
	}
}

func shouldSkipSourceLine(line string) bool {
	if shouldSkipHeadingScanLine(line) {
		return true
	}
	normalized := normalizeComparableText(line)
	if strings.HasPrefix(normalized, "quyen thuong") || strings.HasPrefix(normalized, "quyen ha") {
		return true
	}
	if isLikelyShortOCRArtifactLine(line) {
		return true
	}
	if len(normalized) <= 3 {
		allDigits := true
		for _, r := range normalized {
			if !unicode.IsDigit(r) {
				allDigits = false
				break
			}
		}
		if allDigits {
			return true
		}
	}
	return false
}

func shouldSkipHeadingScanLine(line string) bool {
	if line == "" {
		return true
	}
	normalized := normalizeComparableText(line)
	if normalized == "" {
		return true
	}
	switch normalized {
	case "dich kinh tuong giai", "thu giang", "nguyen duy can", "nha xuat ban tre", "muc luc":
		return true
	}
	if strings.HasPrefix(normalized, "nguyen duy") {
		return true
	}
	if strings.HasPrefix(normalized, "thu giang nguyen duy") {
		return true
	}
	for _, phrase := range []string{
		"dich kinh tuong giai",
		"thu giang nguyen duy can",
		"nha xuat ban tre",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	if len(normalized) <= 3 {
		allDigits := true
		for _, r := range normalized {
			if !unicode.IsDigit(r) {
				allDigits = false
				break
			}
		}
		if allDigits {
			return true
		}
	}
	return false
}

func isLikelyShortOCRArtifactLine(line string) bool {
	normalized := normalizeComparableText(line)
	if normalized == "" {
		return true
	}
	tokens := strings.Fields(normalized)
	if len(tokens) == 0 {
		return true
	}
	if len(tokens) > 4 {
		return false
	}

	shortTokens := 0
	hasDigit := false
	letters := 0
	other := 0
	for _, token := range tokens {
		if runeLen(token) <= 2 {
			shortTokens++
		}
		if strings.IndexFunc(token, unicode.IsDigit) >= 0 {
			hasDigit = true
		}
	}
	for _, r := range line {
		switch {
		case unicode.IsLetter(r):
			letters++
		case unicode.IsDigit(r), unicode.IsSpace(r):
		default:
			other++
		}
	}
	if shortTokens == len(tokens) && (hasDigit || other > 0 || letters <= 8) {
		return true
	}
	return other > letters && len(tokens) <= 3
}

func prepareBookIndex(index *bookIndex) *bookIndex {
	if index == nil {
		return nil
	}
	if index.Version == 0 {
		index.Version = index.IndexVersion
	}
	if index.IndexVersion == 0 {
		index.IndexVersion = index.Version
	}
	if index.SourceSignature == "" {
		index.SourceSignature = index.effectiveSourceSignature()
	}
	index.byNumber = make(map[int]*hexagramSection, len(index.Sections))
	for i := range index.Sections {
		section := &index.Sections[i]
		if len(section.Chunks) == 0 {
			section.Chunks = chunkHexagramText(section.Text)
		}
		for j := range section.Chunks {
			chunk := &section.Chunks[j]
			chunk.Order = j
			if chunk.Normalized == "" {
				chunk.Normalized = normalizeComparableText(chunk.Text)
			}
			if len(chunk.Tokens) == 0 {
				chunk.Tokens = tokenizeComparableText(chunk.Text)
			}
			chunk.HasHa = chunk.HasHa || strings.Contains(chunk.Normalized, "hinh nhi ha")
			chunk.HasThuong = chunk.HasThuong || strings.Contains(chunk.Normalized, "hinh nhi thuong")
		}
		index.byNumber[section.Number] = section
	}
	return index
}

func (idx *bookIndex) sectionByNumber(number int) *hexagramSection {
	if idx == nil {
		return nil
	}
	if idx.byNumber == nil {
		prepareBookIndex(idx)
	}
	return idx.byNumber[number]
}
