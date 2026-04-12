package dailyiching

import (
	"os"
	"sort"
	"strings"
	"time"
)

type IndexInspectionReport struct {
	SourceRoot       string                 `json:"source_root"`
	SourceSignature  string                 `json:"source_signature,omitempty"`
	RequestedQueries []string               `json:"requested_queries,omitempty"`
	Versions         []IndexVersionSummary  `json:"versions"`
	Comparisons      []IndexQueryComparison `json:"comparisons,omitempty"`
}

type IndexVersionSummary struct {
	Label           string             `json:"label"`
	Available       bool               `json:"available"`
	Error           string             `json:"error,omitempty"`
	CachePath       string             `json:"cache_path,omitempty"`
	IndexVersion    int                `json:"index_version"`
	SourceSignature string             `json:"source_signature,omitempty"`
	Extractor       string             `json:"extractor,omitempty"`
	GeneratedAt     *time.Time         `json:"generated_at,omitempty"`
	SourceCount     int                `json:"source_count"`
	HexagramCount   int                `json:"hexagram_count"`
	ChunkCount      int                `json:"chunk_count"`
	SampleChunks    []IndexChunkSample `json:"sample_chunks,omitempty"`
}

type IndexChunkSample struct {
	SectionNumber int    `json:"section_number"`
	SectionName   string `json:"section_name"`
	SectionTitle  string `json:"section_title"`
	Heading       string `json:"heading,omitempty"`
	ChunkOrder    int    `json:"chunk_order"`
	Text          string `json:"text"`
}

type IndexQueryComparison struct {
	Query    string             `json:"query"`
	Versions []IndexQueryResult `json:"versions"`
}

type IndexQueryResult struct {
	Label           string              `json:"label"`
	IndexVersion    int                 `json:"index_version"`
	SourceSignature string              `json:"source_signature,omitempty"`
	Hits            []IndexRetrievalHit `json:"hits,omitempty"`
}

type IndexRetrievalHit struct {
	SectionNumber int     `json:"section_number"`
	SectionName   string  `json:"section_name"`
	SectionTitle  string  `json:"section_title"`
	Heading       string  `json:"heading,omitempty"`
	DisplaySource string  `json:"display_source,omitempty"`
	ChunkOrder    int     `json:"chunk_order"`
	Score         int     `json:"score"`
	MatchCount    int     `json:"match_count"`
	Coverage      float64 `json:"coverage"`
	Text          string  `json:"text"`
}

type chunkSearchHit struct {
	section    *hexagramSection
	chunk      *bookChunk
	score      int
	matchCount int
	coverage   float64
}

type retrievalIntent struct {
	practical     bool
	philosophical bool
	overview      bool
}

func DefaultIndexInspectionQueries() []string {
	return []string{
		"quân tử tự cường bất tức",
		"hiện long tại điền lợi kiến đại nhân",
		"quần long vô thủ",
		"vị tế hanh tiểu hồ ly",
		"hình nhi hạ",
	}
}

func BuildIndexInspectionReport(workspace, dataDir string, queries []string, rebuild bool) (*IndexInspectionReport, error) {
	sourceRoot, err := resolveBookSourceRoot(workspace)
	if err != nil {
		return nil, err
	}

	report := &IndexInspectionReport{
		SourceRoot:       sourceRoot,
		RequestedQueries: normalizeInspectionQueries(queries),
		Versions:         make([]IndexVersionSummary, 0, 3),
	}

	versionPlan := []struct {
		label   string
		version int
	}{
		{label: "v2", version: 2},
		{label: "v3", version: 3},
		{label: "v4", version: bookIndexVersion},
	}

	loaded := make(map[string]*bookIndex, len(versionPlan))
	for _, plan := range versionPlan {
		index, cachePath, err := loadIndexForInspection(sourceRoot, workspace, dataDir, plan.version, rebuild && plan.version == bookIndexVersion)
		summary := summarizeIndexVersion(plan.label, cachePath, index, err)
		report.Versions = append(report.Versions, summary)
		if index != nil {
			loaded[plan.label] = index
			if report.SourceSignature == "" {
				report.SourceSignature = index.effectiveSourceSignature()
			}
		}
	}
	if v4 := findInspectionVersionSummary(report.Versions, "v4"); v4 != nil && v4.Available && v4.SourceSignature != "" {
		report.SourceSignature = v4.SourceSignature
	}

	if len(report.RequestedQueries) == 0 {
		return report, nil
	}

	report.Comparisons = make([]IndexQueryComparison, 0, len(report.RequestedQueries))
	for _, query := range report.RequestedQueries {
		comparison := IndexQueryComparison{
			Query:    query,
			Versions: make([]IndexQueryResult, 0, len(report.Versions)),
		}
		for _, version := range report.Versions {
			index := loaded[version.Label]
			if index == nil {
				continue
			}
			comparison.Versions = append(comparison.Versions, IndexQueryResult{
				Label:           version.Label,
				IndexVersion:    version.IndexVersion,
				SourceSignature: version.SourceSignature,
				Hits:            searchIndexHits(index, query, 3),
			})
		}
		report.Comparisons = append(report.Comparisons, comparison)
	}

	return report, nil
}

func loadIndexForInspection(sourceRoot, workspace, dataDir string, version int, rebuild bool) (*bookIndex, string, error) {
	if version == bookIndexVersion {
		cachePath, err := resolveBookCachePathForVersion(workspace, dataDir, version)
		if err != nil {
			return nil, "", err
		}
		index, err := loadOrBuildBookIndexWithOptions(sourceRoot, cachePath, rebuild)
		return index, cachePath, err
	}
	return loadCachedBookIndexForVersion(workspace, dataDir, version)
}

func summarizeIndexVersion(label, cachePath string, index *bookIndex, err error) IndexVersionSummary {
	summary := IndexVersionSummary{
		Label:     label,
		CachePath: strings.TrimSpace(cachePath),
	}
	if err != nil {
		if os.IsNotExist(err) {
			summary.Error = "cache not found"
		} else {
			summary.Error = err.Error()
		}
	}
	if index == nil {
		return summary
	}

	generatedAt := index.GeneratedAt.UTC()
	summary.Available = true
	summary.IndexVersion = index.effectiveVersion()
	summary.SourceSignature = index.effectiveSourceSignature()
	summary.Extractor = index.Extractor
	summary.GeneratedAt = &generatedAt
	summary.SourceCount = len(index.Sources)
	summary.HexagramCount = len(index.Sections)
	summary.ChunkCount = totalChunkCount(index)
	summary.SampleChunks = sampleChunksForIndex(index)
	return summary
}

func totalChunkCount(index *bookIndex) int {
	if index == nil {
		return 0
	}
	total := 0
	for _, section := range index.Sections {
		total += len(section.Chunks)
	}
	return total
}

func sampleChunksForIndex(index *bookIndex) []IndexChunkSample {
	if index == nil {
		return nil
	}

	var samples []IndexChunkSample
	for _, number := range []int{1, 31, 64} {
		section := index.sectionByNumber(number)
		if section == nil {
			continue
		}
		maxChunks := 2
		if len(section.Chunks) < maxChunks {
			maxChunks = len(section.Chunks)
		}
		for i := 0; i < maxChunks; i++ {
			chunk := section.Chunks[i]
			samples = append(samples, IndexChunkSample{
				SectionNumber: section.Number,
				SectionName:   section.Name,
				SectionTitle:  section.Title,
				Heading:       trimInspectionText(section.Heading, 96),
				ChunkOrder:    chunk.Order,
				Text:          trimInspectionText(chunk.Text, 240),
			})
		}
	}
	return samples
}

func searchIndexHits(index *bookIndex, query string, limit int) []IndexRetrievalHit {
	if index == nil {
		return nil
	}

	var ranked []chunkSearchHit
	for i := range index.Sections {
		section := &index.Sections[i]
		ranked = append(ranked, rankSectionChunks(section, query, 0)...)
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].coverage != ranked[j].coverage {
			return ranked[i].coverage > ranked[j].coverage
		}
		if ranked[i].section.Number != ranked[j].section.Number {
			return ranked[i].section.Number < ranked[j].section.Number
		}
		return ranked[i].chunk.Order < ranked[j].chunk.Order
	})

	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}

	out := make([]IndexRetrievalHit, 0, len(ranked))
	for _, hit := range ranked {
		out = append(out, IndexRetrievalHit{
			SectionNumber: hit.section.Number,
			SectionName:   hit.section.Name,
			SectionTitle:  hit.section.Title,
			Heading:       trimInspectionText(hit.section.Heading, 96),
			DisplaySource: hit.section.DisplaySource,
			ChunkOrder:    hit.chunk.Order,
			Score:         hit.score,
			MatchCount:    hit.matchCount,
			Coverage:      hit.coverage,
			Text:          trimInspectionText(hit.chunk.Text, 240),
		})
	}
	return out
}

func rankSectionChunks(section *hexagramSection, query string, limit int) []chunkSearchHit {
	if section == nil || len(section.Chunks) == 0 {
		return nil
	}

	queryTokens := tokenizeComparableText(query)
	if len(queryTokens) == 0 {
		return nil
	}
	phrases := buildComparableQueryPhrases(queryTokens)
	intent := detectRetrievalIntent(queryTokens)
	sectionTokens := tokenizeComparableText(section.Name + " " + section.Title + " " + section.Heading)
	requiredHits := requiredQueryTokenHits(queryTokens)

	ranked := make([]chunkSearchHit, 0, len(section.Chunks))
	for i := range section.Chunks {
		chunk := &section.Chunks[i]
		chunkTokenHits := countTokenOverlap(chunk.Tokens, queryTokens)
		sectionTokenHits := countTokenOverlap(sectionTokens, queryTokens)
		phraseHits := countPhraseHits(chunk.Normalized, phrases)
		matchCount := chunkTokenHits + sectionTokenHits + phraseHits

		switch {
		case chunkTokenHits == 0 && phraseHits == 0:
			if sectionTokenHits < requiredHits || chunk.Order > 1 {
				continue
			}
		case chunkTokenHits+sectionTokenHits < requiredHits && phraseHits == 0:
			continue
		}

		score := chunkTokenHits*20 + phraseHits*35 + sectionTokenHits*8
		noisePenalty := ocrTextNoisePenalty(chunk.Text)
		if chunkTokenHits == len(queryTokens) && len(queryTokens) > 0 {
			score += 25
		}
		if phraseHits > 0 {
			score += 10
		}
		if sectionTokenHits > 0 && chunk.Order == 0 {
			score += 4
		}
		if intent.practical {
			if chunk.HasHa {
				score += 24
			}
			if strings.Contains(chunk.Normalized, "hinh nhi ha") {
				score += 12
			}
		}
		if intent.philosophical {
			if chunk.HasThuong {
				score += 24
			}
			if strings.Contains(chunk.Normalized, "hinh nhi thuong") {
				score += 12
			}
			if strings.Contains(chunk.Normalized, "dai tuong") {
				score += 12
			}
		}
		if intent.overview && chunk.Order == 0 && noisePenalty < 20 {
			score += 8
		}
		score -= noisePenalty

		coverage := 0.0
		if len(queryTokens) > 0 {
			coverage = float64(chunkTokenHits+sectionTokenHits) / float64(len(queryTokens))
			if coverage > 1 {
				coverage = 1
			}
		}

		ranked = append(ranked, chunkSearchHit{
			section:    section,
			chunk:      chunk,
			score:      score,
			matchCount: matchCount,
			coverage:   coverage,
		})
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].coverage != ranked[j].coverage {
			return ranked[i].coverage > ranked[j].coverage
		}
		return ranked[i].chunk.Order < ranked[j].chunk.Order
	})

	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

func buildComparableQueryPhrases(tokens []string) []string {
	if len(tokens) < 2 {
		return nil
	}

	seen := make(map[string]struct{})
	var phrases []string
	fullQuery := strings.Join(tokens, " ")
	seen[fullQuery] = struct{}{}
	phrases = append(phrases, fullQuery)

	maxWindow := 3
	if len(tokens) < maxWindow {
		maxWindow = len(tokens)
	}
	for width := maxWindow; width >= 2; width-- {
		for start := 0; start+width <= len(tokens); start++ {
			phrase := strings.Join(tokens[start:start+width], " ")
			if _, ok := seen[phrase]; ok {
				continue
			}
			seen[phrase] = struct{}{}
			phrases = append(phrases, phrase)
		}
	}
	return phrases
}

func countPhraseHits(normalized string, phrases []string) int {
	if normalized == "" || len(phrases) == 0 {
		return 0
	}
	hits := 0
	for _, phrase := range phrases {
		if phrase == "" {
			continue
		}
		if strings.Contains(normalized, phrase) {
			hits++
		}
	}
	if hits > 2 {
		return 2
	}
	return hits
}

func detectRetrievalIntent(tokens []string) retrievalIntent {
	intent := retrievalIntent{}
	for _, token := range tokens {
		switch token {
		case "ha", "hinh", "thuc", "hanh", "ung", "xu", "doi", "song":
			intent.practical = true
		case "thuong", "dao", "ly", "triet", "tuong", "nguyen":
			intent.philosophical = true
		}
	}
	intent.overview = !intent.practical && !intent.philosophical
	return intent
}

func requiredQueryTokenHits(tokens []string) int {
	switch {
	case len(tokens) == 0:
		return 0
	case len(tokens) <= 2:
		return 1
	default:
		return 2
	}
}

func normalizeInspectionQueries(queries []string) []string {
	if len(queries) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(queries))
	out := make([]string, 0, len(queries))
	for _, query := range queries {
		query = cleanSnippet(query)
		if query == "" {
			continue
		}
		if _, ok := seen[query]; ok {
			continue
		}
		seen[query] = struct{}{}
		out = append(out, query)
	}
	return out
}

func trimInspectionText(value string, limit int) string {
	value = cleanSnippet(value)
	if value == "" {
		return ""
	}
	if runeLen(value) <= limit {
		return value
	}
	return strings.TrimSpace(trimRunes(value, limit)) + "..."
}

func findInspectionVersionSummary(summaries []IndexVersionSummary, label string) *IndexVersionSummary {
	for i := range summaries {
		if summaries[i].Label == label {
			return &summaries[i]
		}
	}
	return nil
}
