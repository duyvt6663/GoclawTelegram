package dailyiching

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveBookCachePathFallsBackWhenDataDirUnavailable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	blockedDataDir := filepath.Join(root, "blocked")
	if err := os.WriteFile(blockedDataDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocked data dir sentinel: %v", err)
	}

	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	got, err := resolveBookCachePath(workspace, blockedDataDir)
	if err != nil {
		t.Fatalf("resolveBookCachePath returned error: %v", err)
	}

	wantPrefix := filepath.Join(workspace, "beta_cache", featureName)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("cache path = %q, want prefix %q", got, wantPrefix)
	}
	if _, err := os.Stat(filepath.Dir(got)); err != nil {
		t.Fatalf("cache dir stat: %v", err)
	}
}

func TestFindHexagramStartAcceptsBatThuanHeadingVariant(t *testing.T) {
	t.Parallel()

	lines := []string{
		"DỊCH KINH TƯỜNG GIẢI",
		"111",
		"1. BAT THUAN KIEN",
		"[1]",
		"Kiền: Sức sáng tạo",
		"1. có 2 giọng đọc:",
		"Kiền",
		"hoặc",
		"Càn",
	}

	got := findHexagramStart(lines, 0, kingWenSequence[0])
	if got != 2 {
		t.Fatalf("findHexagramStart() = %d, want 2", got)
	}
}

func TestFindHexagramStartAcceptsCorruptedHeadingWithDelayedNameContext(t *testing.T) {
	t.Parallel()

	lines := []string{
		"DỊCH KINH TƯỜNG GIẢI",
		"111",
		"1. B T THU N KI N",
		"[1]",
		"Ki n: S c s ng t o",
		"ho c Dang Tao hoa",
		"Ki n gi, Ki n d",
		"T ng: con R ng",
		"Que co 6 hao du ng.",
		"s c s ng t o c a Tao hoa",
		"1. có 2 giọng đọc:",
		"Kiền",
		"hoặc",
		"Càn",
	}

	got := findHexagramStart(lines, 0, kingWenSequence[0])
	if got != 2 {
		t.Fatalf("findHexagramStart() = %d, want 2", got)
	}
}

func TestFindHexagramStartRejectsBareEnumerationWithDelayedNameContext(t *testing.T) {
	t.Parallel()

	lines := []string{
		"DỊCH KINH TƯỜNG GIẢI",
		"111",
		"2.",
		"Vui vẻ, sung sướng",
		"Lôi trên, Địa dưới, tượng",
		"quẻ Khôn trong một đoạn bình giải khác",
		"2. B T THU N KH N",
		"Khôn, nguyên hanh",
	}

	got := findHexagramStart(lines, 0, kingWenSequence[1])
	if got != 6 {
		t.Fatalf("findHexagramStart() = %d, want 6", got)
	}
}

func TestFindHexagramStartAcceptsConsonantSkeletonHeadingVariant(t *testing.T) {
	t.Parallel()

	lines := []string{
		"DỊCH KINH TƯỜNG GIẢI",
		"111",
		"2. B T THU N KH N",
		"Khôn, nguyên, hanh",
	}

	got := findHexagramStart(lines, 0, kingWenSequence[1])
	if got != 2 {
		t.Fatalf("findHexagramStart() = %d, want 2", got)
	}
}

func TestFindHexagramStartAcceptsMissingNumberWhenHeadingTextMatches(t *testing.T) {
	t.Parallel()

	lines := []string{
		"DỊCH KINH TƯỜNG GIẢI",
		"111",
		". THI N TH Y T NG",
		"Tụng, hữu phu",
	}

	got := findHexagramStart(lines, 0, kingWenSequence[5])
	if got != 2 {
		t.Fatalf("findHexagramStart() = %d, want 2", got)
	}
}

func TestFindHexagramStartAcceptsNumberOnlyHeadingLineWithTitleOnNextLine(t *testing.T) {
	t.Parallel()

	lines := []string{
		"DỊCH KINH TƯỜNG GIẢI",
		"326",
		"15.",
		"SN KHIM",
		"Khim gi, n d",
		"Quẻ",
	}

	got := findHexagramStart(lines, 0, kingWenSequence[14])
	if got != 2 {
		t.Fatalf("findHexagramStart() = %d, want 2", got)
	}
}

func TestFindHexagramStartFallsBackToFirstHeadingLikeExactNumberLine(t *testing.T) {
	t.Parallel()

	lines := []string{
		"DỊCH KINH TƯỜNG GIẢI",
		"111",
		"8. TH Y T",
		"T gi, than d, hoa d",
	}

	got := findHexagramStart(lines, 0, kingWenSequence[7])
	if got != 2 {
		t.Fatalf("findHexagramStart() = %d, want 2", got)
	}
}

func TestFindHexagramStartAcceptsShortSkeletonHeadingWithoutNumber(t *testing.T) {
	t.Parallel()

	lines := []string{
		"DỊCH KINH TƯỜNG GIẢI",
		"221",
		". TH Y S",
		"S gi, ch ng d",
	}

	got := findHexagramStart(lines, 0, kingWenSequence[6])
	if got != 2 {
		t.Fatalf("findHexagramStart() = %d, want 2", got)
	}
}

func TestFindHexagramStartUsesWindowContextForCorruptedHeading(t *testing.T) {
	t.Parallel()

	lines := []string{
		"DỊCH KINH TƯỜNG GIẢI",
		"339",
		"1. L I D",
		"D gi, duyet d, nhac d",
		"Loi tren, Dia duoi, tuong",
		"Thai cua que D, nghia ay lon vay",
	}

	got := findHexagramStart(lines, 0, kingWenSequence[15])
	if got != 2 {
		t.Fatalf("findHexagramStart() = %d, want 2", got)
	}
}

func TestShouldSkipSourceLineSkipsHeaderAndArtifactNoise(t *testing.T) {
	t.Parallel()

	cases := []struct {
		line string
		want bool
	}{
		{line: "112 Thu Giang NGUYÊN DUY CẦN", want: true},
		{line: "DỊCH KINH TƯỜNG GIẢI 113", want: true},
		{line: "D0, FE", want: true},
		{line: "—aa.ea", want: true},
		{line: "LỜI NÓI ĐẦU", want: false},
		{line: "Quẻ có 6 hào dương.", want: false},
	}

	for _, tc := range cases {
		if got := shouldSkipSourceLine(tc.line); got != tc.want {
			t.Fatalf("shouldSkipSourceLine(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestChunkHexagramTextTrimsLeadingOCRNoise(t *testing.T) {
	t.Parallel()

	text := strings.Join([]string{
		"1. BAT THUAN KIEN",
		"LL",
		"D0, FE",
		"—aa.ea",
		"Kiền: Sức sáng tạo hoặc Đấng Tạo hóa",
		"Quẻ có 6 hào dương. Hào dương là nguyên lực rất mạnh và rất hoạt động.",
	}, "\n")

	chunks := chunkHexagramText(text)
	if len(chunks) == 0 {
		t.Fatal("chunkHexagramText() returned no chunks")
	}
	first := normalizeComparableText(chunks[0].Text)
	if strings.Contains(first, "bat thuan kien") {
		t.Fatalf("first chunk still contains OCR heading noise: %q", chunks[0].Text)
	}
	if !strings.Contains(first, "kien suc sang tao") {
		t.Fatalf("first chunk = %q, want cleaned opening content", chunks[0].Text)
	}
}

func TestBuildBookIndexFromLocalBooks(t *testing.T) {
	sourceRoot := findLocalBookSourceRoot(t)

	sources, err := listBookSourceFiles(sourceRoot)
	if err != nil {
		t.Fatalf("listBookSourceFiles() error = %v", err)
	}

	index, err := buildBookIndexWithExtractor(sourceRoot, sources, bookTextExtractorPlain)
	if err != nil {
		t.Fatalf("buildBookIndexWithExtractor() error = %v", err)
	}
	if got, want := len(index.Sections), len(kingWenSequence); got != want {
		t.Fatalf("len(index.Sections) = %d, want %d", got, want)
	}

	q16 := index.sectionByNumber(16)
	if q16 == nil {
		t.Fatal("section 16 missing from built index")
	}
	if q16.DisplaySource == "" || q16.Heading == "" || len(q16.Chunks) == 0 {
		t.Fatalf("section 16 incomplete: source=%q heading=%q chunks=%d", q16.DisplaySource, q16.Heading, len(q16.Chunks))
	}
	if shouldSkipHeadingScanLine(q16.Heading) {
		t.Fatalf("section 16 heading should not be a page header: %q", q16.Heading)
	}

	q64 := index.sectionByNumber(64)
	if q64 == nil {
		t.Fatal("section 64 missing from built index")
	}
	if q64.DisplaySource == "" || q64.Heading == "" || len(q64.Chunks) == 0 {
		t.Fatalf("section 64 incomplete: source=%q heading=%q chunks=%d", q64.DisplaySource, q64.Heading, len(q64.Chunks))
	}
}

func TestBuildBookIndexFromLocalBooksWithPDFToText(t *testing.T) {
	sourceRoot := findLocalBookSourceRoot(t)

	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}

	sources, err := listBookSourceFiles(sourceRoot)
	if err != nil {
		t.Fatalf("listBookSourceFiles() error = %v", err)
	}

	index, err := buildBookIndexWithExtractor(sourceRoot, sources, bookTextExtractorPDFToText)
	if err != nil {
		t.Fatalf("buildBookIndexWithExtractor() error = %v", err)
	}
	if got, want := len(index.Sections), len(kingWenSequence); got != want {
		t.Fatalf("len(index.Sections) = %d, want %d", got, want)
	}

	q16 := index.sectionByNumber(16)
	if q16 == nil {
		t.Fatal("section 16 missing from built index")
	}
	if !strings.Contains(normalizeComparableText(q16.Heading), normalizeComparableText("16. LÔI ĐỊA DỰ")) {
		t.Fatalf("section 16 heading = %q, want LÔI ĐỊA DỰ", q16.Heading)
	}

	q17 := index.sectionByNumber(17)
	if q17 == nil {
		t.Fatal("section 17 missing from built index")
	}
	if !strings.Contains(normalizeComparableText(q17.Heading), normalizeComparableText("17. TRẠCH LÔI TÙY")) {
		t.Fatalf("section 17 heading = %q, want TRẠCH LÔI TÙY", q17.Heading)
	}
}

func TestFindHexagramStartFromLocalUpperVolumeAroundQ15Q17(t *testing.T) {
	sourceRoot := findLocalBookSourceRoot(t)

	sources, err := listBookSourceFiles(sourceRoot)
	if err != nil {
		t.Fatalf("listBookSourceFiles() error = %v", err)
	}

	var upper bookSourceFile
	for _, source := range sources {
		if source.VolumeOrder == 0 {
			upper = source
			break
		}
	}
	if upper.Path == "" {
		t.Fatal("upper volume source missing")
	}

	doc, err := parsePDFSource(upper, bookTextExtractorPlain)
	if err != nil {
		t.Fatalf("parsePDFSource() error = %v", err)
	}

	linePos := 0
	cases := []struct {
		number      int
		wantHeading string
	}{
		{number: 15, wantHeading: "15."},
		{number: 16, wantHeading: "LI D"},
		{number: 17, wantHeading: "TRCH LI TY"},
	}

	for _, tc := range cases {
		meta := kingWenSequence[tc.number-1]
		idx := findHexagramStart(doc.Lines, linePos, meta)
		if idx < 0 {
			t.Fatalf("findHexagramStart(%d) returned %d", tc.number, idx)
		}
		got := cleanSourceLine(doc.Lines[idx])
		if !strings.Contains(normalizeComparableText(got), normalizeComparableText(tc.wantHeading)) {
			t.Fatalf("findHexagramStart(%d) heading = %q, want it to contain %q", tc.number, got, tc.wantHeading)
		}
		linePos = idx + 1
	}
}

func TestFindHexagramStartFromLocalUpperVolumeAroundQ15Q17WithPDFToText(t *testing.T) {
	sourceRoot := findLocalBookSourceRoot(t)

	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}

	sources, err := listBookSourceFiles(sourceRoot)
	if err != nil {
		t.Fatalf("listBookSourceFiles() error = %v", err)
	}

	var upper bookSourceFile
	for _, source := range sources {
		if source.VolumeOrder == 0 {
			upper = source
			break
		}
	}
	if upper.Path == "" {
		t.Fatal("upper volume source missing")
	}

	doc, err := parsePDFSource(upper, bookTextExtractorPDFToText)
	if err != nil {
		t.Fatalf("parsePDFSource() error = %v", err)
	}

	linePos := 0
	cases := []struct {
		number      int
		wantHeading string
	}{
		{number: 15, wantHeading: "15. ĐỊA SƠN KHIÊM"},
		{number: 16, wantHeading: "16. LÔI ĐỊA DỰ"},
		{number: 17, wantHeading: "17. TRẠCH LÔI TÙY"},
	}

	for _, tc := range cases {
		meta := kingWenSequence[tc.number-1]
		idx := findHexagramStart(doc.Lines, linePos, meta)
		if idx < 0 {
			t.Fatalf("findHexagramStart(%d) returned %d", tc.number, idx)
		}
		got := cleanSourceLine(doc.Lines[idx])
		if !strings.Contains(normalizeComparableText(got), normalizeComparableText(tc.wantHeading)) {
			t.Fatalf("findHexagramStart(%d) heading = %q, want it to contain %q", tc.number, got, tc.wantHeading)
		}
		linePos = idx + 1
	}
}

func findLocalBookSourceRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}

	for {
		candidate := filepath.Join(dir, "builder-bot", "data")
		if files, err := listBookSourceFiles(candidate); err == nil && len(files) >= 2 {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Skip("local Daily I Ching PDFs not available")
	return ""
}
