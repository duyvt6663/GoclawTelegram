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
	Version     int               `json:"version"`
	GeneratedAt time.Time         `json:"generated_at"`
	SourceRoot  string            `json:"source_root"`
	Extractor   string            `json:"extractor"`
	Sources     []bookSourceFile  `json:"sources"`
	Sections    []hexagramSection `json:"sections"`

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
		return filepath.Join(cacheDir, fmt.Sprintf("book_index_v%d.json", bookIndexVersion)), nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("cache root unavailable")
}

func loadOrBuildBookIndex(sourceRoot, cachePath string) (*bookIndex, error) {
	sources, err := listBookSourceFiles(sourceRoot)
	if err != nil {
		return nil, err
	}
	extractor, forced, err := resolveBookTextExtractor()
	if err != nil {
		return nil, err
	}
	if cached, err := loadCachedBookIndex(cachePath); err == nil && cacheMatchesSources(cached, sourceRoot, sources, extractor) {
		return prepareBookIndex(cached), nil
	}

	index, err := buildBookIndexWithExtractor(sourceRoot, sources, extractor)
	if err != nil && extractor == bookTextExtractorTesseract && !forced {
		index, err = buildBookIndexWithExtractor(sourceRoot, sources, bookTextExtractorPlain)
	}
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
	return &index, nil
}

func persistBookIndex(path string, index *bookIndex) error {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func cacheMatchesSources(index *bookIndex, sourceRoot string, sources []bookSourceFile, extractor string) bool {
	if index == nil || index.Version != bookIndexVersion || len(index.Sources) != len(sources) {
		return false
	}
	if filepath.Clean(index.SourceRoot) != filepath.Clean(sourceRoot) {
		return false
	}
	if strings.TrimSpace(index.Extractor) != strings.TrimSpace(extractor) {
		return false
	}
	for i := range sources {
		if index.Sources[i].Path != sources[i].Path || index.Sources[i].Signature != sources[i].Signature {
			return false
		}
	}
	return true
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
	extractor, forced, err := resolveBookTextExtractor()
	if err != nil {
		return nil, err
	}
	index, err := buildBookIndexWithExtractor(sourceRoot, sources, extractor)
	if err != nil && extractor == bookTextExtractorTesseract && !forced {
		return buildBookIndexWithExtractor(sourceRoot, sources, bookTextExtractorPlain)
	}
	return index, err
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
		Version:     bookIndexVersion,
		GeneratedAt: time.Now().UTC(),
		SourceRoot:  sourceRoot,
		Extractor:   extractor,
		Sources:     sources,
		Sections:    sections,
	}, nil
}

func parsePDFSource(source bookSourceFile, extractor string) (sourceDocument, error) {
	if extractor == bookTextExtractorTesseract {
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

	rawLines := strings.Split(string(textBytes), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		lines = append(lines, strings.TrimRight(line, " \t\r"))
	}

	return sourceDocument{Source: source, Lines: lines}, nil
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
		if shouldSkipSourceLine(candidate) {
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
	if shouldSkipSourceLine(line) {
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

func chunkHexagramText(text string) []bookChunk {
	lines := strings.Split(text, "\n")
	var (
		chunks    []bookChunk
		buffer    []string
		charCount int
	)

	flush := func() {
		if len(buffer) == 0 {
			return
		}
		raw := strings.TrimSpace(strings.Join(buffer, " "))
		raw = whitespaceCollapse.ReplaceAllString(raw, " ")
		raw = strings.TrimSpace(raw)
		if raw == "" {
			buffer = nil
			charCount = 0
			return
		}
		normalized := normalizeComparableText(raw)
		chunks = append(chunks, bookChunk{
			Order:      len(chunks),
			Text:       raw,
			Normalized: normalized,
			Tokens:     tokenizeComparableText(raw),
			HasHa:      strings.Contains(normalized, "hinh nhi ha"),
			HasThuong:  strings.Contains(normalized, "hinh nhi thuong"),
		})
		buffer = nil
		charCount = 0
	}

	for _, rawLine := range lines {
		line := cleanSourceLine(rawLine)
		switch {
		case line == "":
			if charCount >= 700 {
				flush()
			}
			continue
		case shouldSkipSourceLine(line):
			continue
		}

		buffer = append(buffer, line)
		charCount += len([]rune(line)) + 1
		if charCount >= 1100 {
			flush()
		}
	}
	flush()
	return chunks
}

func shouldSkipSourceLine(line string) bool {
	if line == "" {
		return true
	}
	normalized := normalizeComparableText(line)
	if normalized == "" {
		return true
	}
	switch normalized {
	case "dich kinh tuong giai", "thu giang", "nguyen duy can":
		return true
	}
	if strings.HasPrefix(normalized, "nguyen duy") {
		return true
	}
	if strings.HasPrefix(normalized, "thu giang nguyen duy") {
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

func prepareBookIndex(index *bookIndex) *bookIndex {
	if index == nil {
		return nil
	}
	index.byNumber = make(map[int]*hexagramSection, len(index.Sections))
	for i := range index.Sections {
		section := &index.Sections[i]
		if len(section.Chunks) == 0 {
			section.Chunks = chunkHexagramText(section.Text)
		}
		for j := range section.Chunks {
			chunk := &section.Chunks[j]
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
