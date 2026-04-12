package dailyichingquery

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	dailyiching "github.com/nextlevelbuilder/goclaw/internal/beta/daily_iching"
	memembed "github.com/nextlevelbuilder/goclaw/internal/memory"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

var (
	queryStopwords = map[string]struct{}{
		"cach": {}, "co": {}, "cua": {}, "cho": {}, "duoc": {}, "giai": {}, "giai thich": {}, "gi": {}, "giup": {},
		"hay": {}, "hoi": {}, "huong": {}, "kinh": {}, "la": {}, "lien": {}, "mot": {}, "nao": {}, "nay": {}, "neu": {},
		"nghia": {}, "noi": {}, "nu": {}, "qua": {}, "que": {}, "ra": {}, "sao": {}, "so": {}, "su": {}, "the": {},
		"theo": {}, "thich": {}, "toi": {}, "trong": {}, "ve": {}, "voi": {}, "y": {},
	}
	linePhrases = []string{
		"so cuu", "so luc",
		"cuu nhi", "luc nhi",
		"cuu tam", "luc tam",
		"cuu tu", "luc tu",
		"cuu ngu", "luc ngu",
		"thuong cuu", "thuong luc",
		"dung cuu", "dung luc",
	}
)

type queryResponse struct {
	Question      string     `json:"question"`
	Answer        string     `json:"answer"`
	Confidence    float64    `json:"confidence"`
	NeedsRefine   bool       `json:"needs_refine"`
	RetrievalMode string     `json:"retrieval_mode"`
	Hits          []queryHit `json:"hits,omitempty"`
}

type queryHit struct {
	Ref           string  `json:"ref"`
	SectionNumber int     `json:"section_number"`
	SectionName   string  `json:"section_name"`
	SectionTitle  string  `json:"section_title"`
	Heading       string  `json:"heading,omitempty"`
	ChunkOrder    int     `json:"chunk_order"`
	Score         float64 `json:"score"`
	SemanticScore float64 `json:"semantic_score,omitempty"`
	KeywordScore  float64 `json:"keyword_score,omitempty"`
	Quote         string  `json:"quote"`
	Source        string  `json:"source,omitempty"`
	Role          string  `json:"role,omitempty"`
}

type rankedHit struct {
	chunk         indexedChunk
	score         float64
	semanticScore float64
	keywordScore  float64
	explicit      bool
	lineMatch     bool
	phraseMatches int
	tokenHits     int
	sectionHits   int
}

type resolvedEmbeddingProvider struct {
	provider store.EmbeddingProvider
	name     string
	model    string
}

type embeddingWarmState struct {
	Namespace   string
	LoadedCount int
	TotalCount  int
	Warming     bool
}

func (s embeddingWarmState) Ready() bool {
	return s.TotalCount > 0 && s.LoadedCount >= s.TotalCount
}

func (s embeddingWarmState) MissingCount() int {
	if s.TotalCount <= s.LoadedCount {
		return 0
	}
	return s.TotalCount - s.LoadedCount
}

type queryIntent struct {
	practical     bool
	philosophical bool
	advice        bool
	connection    bool
	line          bool
}

type questionAnalysis struct {
	raw               string
	normalized        string
	tokens            []string
	significantTokens []string
	phrases           []string
	explicitSections  map[int]struct{}
	linePhrases       []string
	lineOrdinal       int
	intent            queryIntent
}

func (f *DailyIChingQueryFeature) answerQuestion(ctx context.Context, tenantID, question string) (*queryResponse, error) {
	question = normalizeSpace(question)
	if question == "" {
		return nil, fmt.Errorf("question is required")
	}

	index, err := f.ensureIndex(false)
	if err != nil {
		return nil, err
	}

	analysis := analyzeQuestion(index, question)
	provider, err := f.resolveEmbeddingProvider(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	retrievalMode := "keyword"
	var queryEmbedding []float32
	if provider != nil {
		warmState, embErr := f.prepareChunkEmbeddings(index, provider, tenantID)
		if embErr != nil {
			slog.Warn("daily iching query chunk embedding prep failed", "error", embErr)
		} else if warmState.Ready() {
			if queryEmbedding, err = f.embedQuestion(ctx, provider, tenantID, analysis.normalized); err == nil && len(queryEmbedding) > 0 {
				retrievalMode = "semantic+keyword"
			} else if err != nil {
				slog.Warn("daily iching query embedding lookup failed", "error", err)
				err = nil
			}
		} else {
			slog.Info("daily iching query answering before embedding warm is complete",
				"loaded", warmState.LoadedCount,
				"total", warmState.TotalCount,
				"warming", warmState.Warming,
			)
		}
	}

	hits := f.rankHits(index, tenantID, provider, analysis, queryEmbedding)
	confidence := computeConfidence(hits, retrievalMode == "semantic+keyword")
	lowConfidence := isLowConfidence(analysis, hits, confidence, retrievalMode == "semantic+keyword")

	references := pickReferenceHits(analysis, hits)
	answerText := renderAnswerText(analysis, references, lowConfidence)
	response := &queryResponse{
		Question:      question,
		Answer:        answerText,
		Confidence:    confidence,
		NeedsRefine:   lowConfidence,
		RetrievalMode: retrievalMode,
		Hits:          buildResponseHits(references),
	}

	if responseJSON, marshalErr := json.Marshal(response); marshalErr == nil {
		if err := f.store.insertRun(&queryRunRecord{
			TenantID:           strings.TrimSpace(tenantID),
			SourceSignature:    index.SourceSignature,
			Question:           question,
			QuestionNormalized: analysis.normalized,
			Confidence:         confidence,
			LowConfidence:      lowConfidence,
			Response:           responseJSON,
			ProviderName:       providerName(provider),
			ProviderModel:      providerModel(provider),
		}); err != nil {
			slog.Warn("daily iching query run log failed", "error", err)
		}
	}

	return response, nil
}

func analyzeQuestion(index *compiledIndex, question string) questionAnalysis {
	normalized := dailyiching.NormalizeComparableText(question)
	tokens := dailyiching.TokenizeComparableText(question)
	significant := filterSignificantTokens(tokens)
	if len(significant) == 0 {
		significant = append([]string(nil), tokens...)
	}

	explicitSections := detectExplicitSections(index, normalized)
	lineMatches, ordinal := detectLineReferences(normalized)
	intent := detectIntent(tokens, len(explicitSections), len(lineMatches) > 0)
	phrases := buildQueryPhrases(significant)

	return questionAnalysis{
		raw:               question,
		normalized:        normalized,
		tokens:            tokens,
		significantTokens: significant,
		phrases:           phrases,
		explicitSections:  explicitSections,
		linePhrases:       lineMatches,
		lineOrdinal:       ordinal,
		intent:            intent,
	}
}

func filterSignificantTokens(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, ok := queryStopwords[token]; ok {
			continue
		}
		out = append(out, token)
	}
	return uniqueStrings(out)
}

func detectExplicitSections(index *compiledIndex, normalizedQuestion string) map[int]struct{} {
	matches := make(map[int]struct{})
	if index == nil || normalizedQuestion == "" {
		return matches
	}

	for _, groups := range hexagramNumberRe.FindAllStringSubmatch(normalizedQuestion, -1) {
		if len(groups) != 2 {
			continue
		}
		number, err := strconv.Atoi(groups[1])
		if err != nil {
			continue
		}
		if _, ok := index.Sections[number]; ok {
			matches[number] = struct{}{}
		}
	}

	for number, section := range index.Sections {
		title := dailyiching.NormalizeComparableText(section.Title)
		name := dailyiching.NormalizeComparableText(section.Name)
		if title != "" && (containsWholePhrase(normalizedQuestion, "que "+title) || containsWholePhrase(normalizedQuestion, title)) {
			matches[number] = struct{}{}
			continue
		}
		if name == "" {
			continue
		}
		if containsWholePhrase(normalizedQuestion, "que "+name) {
			matches[number] = struct{}{}
			continue
		}
		if len(strings.Fields(name)) > 1 || len([]rune(name)) >= 4 {
			if containsWholePhrase(normalizedQuestion, name) {
				matches[number] = struct{}{}
			}
		}
	}

	return matches
}

func detectLineReferences(normalizedQuestion string) ([]string, int) {
	var matches []string
	for _, phrase := range linePhrases {
		if containsWholePhrase(normalizedQuestion, phrase) {
			matches = append(matches, phrase)
		}
	}

	ordinal := 0
	if groups := lineOrdinalRe.FindStringSubmatch(normalizedQuestion); len(groups) == 2 {
		if parsed, err := strconv.Atoi(groups[1]); err == nil && parsed >= 1 && parsed <= 6 {
			ordinal = parsed
			switch parsed {
			case 1:
				matches = append(matches, "so cuu", "so luc")
			case 2:
				matches = append(matches, "cuu nhi", "luc nhi")
			case 3:
				matches = append(matches, "cuu tam", "luc tam")
			case 4:
				matches = append(matches, "cuu tu", "luc tu")
			case 5:
				matches = append(matches, "cuu ngu", "luc ngu")
			case 6:
				matches = append(matches, "thuong cuu", "thuong luc")
			}
		}
	}

	return uniqueStrings(matches), ordinal
}

func detectIntent(tokens []string, explicitSectionCount int, hasLineRef bool) queryIntent {
	intent := queryIntent{
		line: hasLineRef,
	}
	for _, token := range tokens {
		switch token {
		case "ha", "hanh", "lam", "loi", "khuyen", "thuc", "te", "ung", "xu", "doi", "song", "ap", "dung", "cong", "viec":
			intent.practical = true
		case "thuong", "dao", "ly", "triet", "tuong", "nguyen", "y", "nghia":
			intent.philosophical = true
		case "nen", "sao":
			intent.advice = true
		case "lien", "he", "ket", "noi", "sanh", "khac", "giong", "chuyen", "mach":
			intent.connection = true
		}
	}
	if explicitSectionCount >= 2 {
		intent.connection = true
	}
	if intent.advice {
		intent.practical = true
	}
	if !intent.practical && !intent.philosophical && !intent.connection && !intent.line {
		intent.philosophical = true
	}
	return intent
}

func buildQueryPhrases(tokens []string) []string {
	if len(tokens) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var phrases []string

	full := strings.Join(tokens, " ")
	if full != "" {
		seen[full] = struct{}{}
		phrases = append(phrases, full)
	}
	maxWidth := 4
	if len(tokens) < maxWidth {
		maxWidth = len(tokens)
	}
	for width := maxWidth; width >= 2; width-- {
		for start := 0; start+width <= len(tokens); start++ {
			phrase := strings.Join(tokens[start:start+width], " ")
			if phrase == "" {
				continue
			}
			if _, ok := seen[phrase]; ok {
				continue
			}
			seen[phrase] = struct{}{}
			phrases = append(phrases, phrase)
		}
	}
	return phrases
}

func (f *DailyIChingQueryFeature) rankHits(index *compiledIndex, tenantID string, provider *resolvedEmbeddingProvider, analysis questionAnalysis, queryEmbedding []float32) []rankedHit {
	if index == nil {
		return nil
	}

	namespace := ""
	hasSemantic := provider != nil && len(queryEmbedding) > 0
	if hasSemantic {
		namespace = f.chunkNamespace(tenantID, provider.name, provider.model, index.SourceSignature)
	}

	ranked := make([]rankedHit, 0, len(index.FlatChunks))
	for _, chunk := range index.FlatChunks {
		semanticScore := 0.0
		if hasSemantic {
			if embedding := f.lookupChunkEmbedding(namespace, chunk.Key); len(embedding) == len(queryEmbedding) && len(embedding) > 0 {
				semanticScore = clampSimilarity(memembed.CosineSimilarity(queryEmbedding, embedding))
			}
		}

		keywordScore, explicit, lineMatch, phraseMatches, tokenHits, sectionHits := computeKeywordScore(chunk, analysis)
		if semanticScore == 0 && keywordScore == 0 {
			continue
		}

		score := keywordScore
		if hasSemantic {
			score = semanticScore*0.8 + keywordScore*0.2
		}

		ranked = append(ranked, rankedHit{
			chunk:         chunk,
			score:         score,
			semanticScore: semanticScore,
			keywordScore:  keywordScore,
			explicit:      explicit,
			lineMatch:     lineMatch,
			phraseMatches: phraseMatches,
			tokenHits:     tokenHits,
			sectionHits:   sectionHits,
		})
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].explicit != ranked[j].explicit {
			return ranked[i].explicit
		}
		if ranked[i].lineMatch != ranked[j].lineMatch {
			return ranked[i].lineMatch
		}
		if ranked[i].semanticScore != ranked[j].semanticScore {
			return ranked[i].semanticScore > ranked[j].semanticScore
		}
		if ranked[i].chunk.SectionNumber != ranked[j].chunk.SectionNumber {
			return ranked[i].chunk.SectionNumber < ranked[j].chunk.SectionNumber
		}
		return ranked[i].chunk.Order < ranked[j].chunk.Order
	})

	return selectTopHits(ranked, analysis)
}

func computeKeywordScore(chunk indexedChunk, analysis questionAnalysis) (float64, bool, bool, int, int, int) {
	queryTokens := analysis.significantTokens
	if len(queryTokens) == 0 {
		queryTokens = analysis.tokens
	}
	tokenHits := countTokenOverlap(chunk.Tokens, queryTokens)
	sectionHits := countTokenOverlap(chunk.SectionTokens, queryTokens)
	phraseMatches := countPhraseMatches(chunk.Normalized, analysis.phrases)

	explicit := false
	if len(analysis.explicitSections) > 0 {
		_, explicit = analysis.explicitSections[chunk.SectionNumber]
	}

	lineMatch := false
	if len(analysis.linePhrases) > 0 {
		for _, phrase := range analysis.linePhrases {
			if containsWholePhrase(chunk.Normalized, phrase) {
				lineMatch = true
				break
			}
		}
	}

	score := 0.0
	if len(queryTokens) > 0 {
		score += minFloat(0.30, float64(tokenHits)/float64(len(queryTokens))*0.30)
		score += minFloat(0.20, float64(sectionHits)/float64(len(queryTokens))*0.20)
	}
	if phraseMatches > 0 {
		score += minFloat(0.20, 0.12+float64(phraseMatches-1)*0.04)
	}
	if explicit {
		score += 0.30
	}
	if analysis.intent.line && lineMatch {
		score += 0.30
	}
	if analysis.intent.practical {
		if chunk.HasHa {
			score += 0.12
		}
		if strings.Contains(chunk.Normalized, "hinh nhi ha") {
			score += 0.06
		}
	}
	if analysis.intent.philosophical {
		if chunk.HasThuong {
			score += 0.12
		}
		if strings.Contains(chunk.Normalized, "hinh nhi thuong") {
			score += 0.06
		}
		if strings.Contains(chunk.Normalized, "dai tuong") {
			score += 0.05
		}
	}
	if !analysis.intent.practical && !analysis.intent.philosophical && chunk.Order == 0 && !dailyiching.IsLikelyNoisyOCRText(chunk.Text) {
		score += 0.04
	}

	score -= minFloat(0.25, float64(dailyiching.OCRTextNoisePenalty(chunk.Text))/100.0*0.25)
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return score, explicit, lineMatch, phraseMatches, tokenHits, sectionHits
}

func selectTopHits(ranked []rankedHit, analysis questionAnalysis) []rankedHit {
	if len(ranked) == 0 {
		return nil
	}

	maxPerSection := 3
	if analysis.intent.connection || len(analysis.explicitSections) >= 2 {
		maxPerSection = 2
	}

	selected := make([]rankedHit, 0, defaultTopK)
	seen := make(map[string]struct{})
	sectionCounts := make(map[int]int)

	if analysis.intent.connection && len(analysis.explicitSections) >= 2 {
		explicitNumbers := make([]int, 0, len(analysis.explicitSections))
		for number := range analysis.explicitSections {
			explicitNumbers = append(explicitNumbers, number)
		}
		sort.Ints(explicitNumbers)
		for _, number := range explicitNumbers {
			for _, hit := range ranked {
				if hit.chunk.SectionNumber != number {
					continue
				}
				if _, ok := seen[hit.chunk.Key]; ok {
					continue
				}
				selected = append(selected, hit)
				seen[hit.chunk.Key] = struct{}{}
				sectionCounts[number]++
				break
			}
		}
	}

	for _, hit := range ranked {
		if len(selected) >= defaultTopK {
			break
		}
		if _, ok := seen[hit.chunk.Key]; ok {
			continue
		}
		if sectionCounts[hit.chunk.SectionNumber] >= maxPerSection {
			continue
		}
		selected = append(selected, hit)
		seen[hit.chunk.Key] = struct{}{}
		sectionCounts[hit.chunk.SectionNumber]++
	}

	sort.Slice(selected, func(i, j int) bool {
		if selected[i].score != selected[j].score {
			return selected[i].score > selected[j].score
		}
		if selected[i].chunk.SectionNumber != selected[j].chunk.SectionNumber {
			return selected[i].chunk.SectionNumber < selected[j].chunk.SectionNumber
		}
		return selected[i].chunk.Order < selected[j].chunk.Order
	})
	return selected
}

func computeConfidence(hits []rankedHit, hasSemantic bool) float64 {
	if len(hits) == 0 {
		return 0
	}
	confidence := hits[0].score
	if len(hits) > 1 {
		confidence += minFloat(0.10, hits[1].score*0.10)
	}
	if hasSemantic && hits[0].semanticScore >= semanticOnlyConfidenceMin {
		confidence += 0.05
	}
	if len(hits) > 1 && hits[0].chunk.SectionNumber == hits[1].chunk.SectionNumber {
		confidence += 0.05
	}
	if confidence > 1 {
		confidence = 1
	}
	return confidence
}

func isLowConfidence(analysis questionAnalysis, hits []rankedHit, confidence float64, hasSemantic bool) bool {
	if len(hits) == 0 {
		return true
	}
	if confidence < lowConfidenceThreshold {
		return true
	}
	if hasSemantic && hits[0].semanticScore < semanticOnlyConfidenceMin && hits[0].keywordScore < 0.20 {
		return true
	}
	if len(analysis.explicitSections) >= 2 {
		covered := make(map[int]struct{})
		for _, hit := range hits {
			covered[hit.chunk.SectionNumber] = struct{}{}
		}
		for section := range analysis.explicitSections {
			if _, ok := covered[section]; !ok {
				return true
			}
		}
	}
	return false
}

func pickReferenceHits(analysis questionAnalysis, hits []rankedHit) []rankedHit {
	if len(hits) == 0 {
		return nil
	}

	selected := make([]rankedHit, 0, answerReferenceCount)
	seen := make(map[string]struct{})
	add := func(hit rankedHit) {
		if len(selected) >= answerReferenceCount {
			return
		}
		if _, ok := seen[hit.chunk.Key]; ok {
			return
		}
		seen[hit.chunk.Key] = struct{}{}
		selected = append(selected, hit)
	}

	if analysis.intent.connection && len(analysis.explicitSections) >= 2 {
		explicitNumbers := make([]int, 0, len(analysis.explicitSections))
		for number := range analysis.explicitSections {
			explicitNumbers = append(explicitNumbers, number)
		}
		sort.Ints(explicitNumbers)
		for _, number := range explicitNumbers {
			for _, hit := range hits {
				if hit.chunk.SectionNumber == number {
					add(hit)
					break
				}
			}
		}
	}

	if len(selected) == 0 {
		add(hits[0])
	}

	if practical := firstMatchingHit(hits, func(hit rankedHit) bool {
		return hit.chunk.HasHa || containsWholePhrase(hit.chunk.Normalized, "hinh nhi ha")
	}); practical != nil {
		add(*practical)
	}
	if philosophical := firstMatchingHit(hits, func(hit rankedHit) bool {
		return hit.chunk.HasThuong || containsWholePhrase(hit.chunk.Normalized, "hinh nhi thuong") || containsWholePhrase(hit.chunk.Normalized, "dai tuong")
	}); philosophical != nil {
		add(*philosophical)
	}

	for _, hit := range hits {
		add(hit)
	}

	return selected
}

func renderAnswerText(analysis questionAnalysis, refs []rankedHit, lowConfidence bool) string {
	if lowConfidence {
		return renderLowConfidenceAnswer(refs)
	}
	if analysis.intent.connection && len(refs) >= 2 {
		return renderConnectionAnswer(refs)
	}
	return renderStandardAnswer(analysis, refs)
}

func renderLowConfidenceAnswer(refs []rankedHit) string {
	var b strings.Builder
	b.WriteString("Tôi chưa đủ chắc để trả lời gọn từ corpus hiện có. Bạn thử nói rõ hơn quẻ, hào, hoặc tình huống muốn hỏi để tôi truy đoạn sát hơn.")
	if len(refs) == 0 {
		return b.String()
	}
	b.WriteString("\n\nĐiểm gần nhất tôi tìm được là: ")
	fmt.Fprintf(&b, "\"%s\" [1].", shortQuote(refs[0].chunk.Text))
	b.WriteString("\n\nTrích dẫn ngắn\n")
	fmt.Fprintf(&b, "[1] Quẻ %d %s, đoạn %d", refs[0].chunk.SectionNumber, refs[0].chunk.SectionName, refs[0].chunk.Order)
	if refs[0].chunk.DisplaySource != "" {
		fmt.Fprintf(&b, " (%s)", refs[0].chunk.DisplaySource)
	}
	return b.String()
}

func renderStandardAnswer(analysis questionAnalysis, refs []rankedHit) string {
	if len(refs) == 0 {
		return renderLowConfidenceAnswer(nil)
	}

	var b strings.Builder
	primary := refs[0]
	briefQuote := shortQuote(primary.chunk.Text)

	switch {
	case analysis.intent.line:
		fmt.Fprintf(&b, "Trả lời ngắn\nTheo corpus hiện có, đoạn gần nhất cho hào này nhấn: \"%s\" [1].", briefQuote)
	case analysis.intent.advice || analysis.intent.practical:
		fmt.Fprintf(&b, "Trả lời ngắn\nNếu đọc theo hướng thực hành, corpus nghiêng về: \"%s\" [1].", briefQuote)
	case len(analysis.explicitSections) == 1:
		fmt.Fprintf(&b, "Trả lời ngắn\nTheo corpus hiện có, quẻ %d %s được nhấn ở ý: \"%s\" [1].", primary.chunk.SectionNumber, primary.chunk.SectionName, briefQuote)
	default:
		fmt.Fprintf(&b, "Trả lời ngắn\nĐoạn gần nhất trong corpus gợi ý: \"%s\" [1].", briefQuote)
	}

	practicalIdx := referenceIndex(refs, func(hit rankedHit) bool {
		return hit.chunk.HasHa || containsWholePhrase(hit.chunk.Normalized, "hinh nhi ha")
	})
	philosophicalIdx := referenceIndex(refs, func(hit rankedHit) bool {
		return hit.chunk.HasThuong || containsWholePhrase(hit.chunk.Normalized, "hinh nhi thuong") || containsWholePhrase(hit.chunk.Normalized, "dai tuong")
	})

	if practicalIdx >= 0 || philosophicalIdx >= 0 {
		b.WriteString("\n\nGiải thích")
		if practicalIdx >= 0 {
			fmt.Fprintf(&b, "\nHình nhi hạ: Ở mặt xử trí cụ thể, đoạn gần nhất nghiêng về \"%s\" [%d].", shortQuote(refs[practicalIdx].chunk.Text), practicalIdx+1)
		}
		if philosophicalIdx >= 0 && philosophicalIdx != practicalIdx {
			fmt.Fprintf(&b, "\nHình nhi thượng: Ở mặt nguyên lý, corpus nhấn vào \"%s\" [%d].", shortQuote(refs[philosophicalIdx].chunk.Text), philosophicalIdx+1)
		}
	}

	b.WriteString("\n\nTrích dẫn ngắn")
	for i, hit := range refs {
		fmt.Fprintf(&b, "\n[%d] Quẻ %d %s, đoạn %d", i+1, hit.chunk.SectionNumber, hit.chunk.SectionName, hit.chunk.Order)
		if hit.chunk.DisplaySource != "" {
			fmt.Fprintf(&b, " (%s)", hit.chunk.DisplaySource)
		}
		fmt.Fprintf(&b, ": \"%s\"", shortQuote(hit.chunk.Text))
	}
	return b.String()
}

func renderConnectionAnswer(refs []rankedHit) string {
	if len(refs) < 2 {
		return renderStandardAnswer(questionAnalysis{}, refs)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Trả lời ngắn\nCorpus không cho thấy một câu nối trực tiếp rất mạnh giữa hai quẻ này. Điểm gần nhất là quẻ %d %s nhấn \"%s\" [1], còn quẻ %d %s nhấn \"%s\" [2].",
		refs[0].chunk.SectionNumber, refs[0].chunk.SectionName, shortQuote(refs[0].chunk.Text),
		refs[1].chunk.SectionNumber, refs[1].chunk.SectionName, shortQuote(refs[1].chunk.Text),
	)

	b.WriteString("\n\nGiải thích")
	fmt.Fprintf(&b, "\nTrục thứ nhất: Quẻ %d %s được dẫn bằng đoạn \"%s\" [1].", refs[0].chunk.SectionNumber, refs[0].chunk.SectionName, shortQuote(refs[0].chunk.Text))
	fmt.Fprintf(&b, "\nTrục thứ hai: Quẻ %d %s được dẫn bằng đoạn \"%s\" [2].", refs[1].chunk.SectionNumber, refs[1].chunk.SectionName, shortQuote(refs[1].chunk.Text))
	b.WriteString("\nMạch nối an toàn nhất là đặt hai trục này cạnh nhau để đối chiếu, thay vì khẳng định một quan hệ vượt quá những gì corpus đang có.")

	b.WriteString("\n\nTrích dẫn ngắn")
	for i, hit := range refs {
		fmt.Fprintf(&b, "\n[%d] Quẻ %d %s, đoạn %d", i+1, hit.chunk.SectionNumber, hit.chunk.SectionName, hit.chunk.Order)
		if hit.chunk.DisplaySource != "" {
			fmt.Fprintf(&b, " (%s)", hit.chunk.DisplaySource)
		}
		fmt.Fprintf(&b, ": \"%s\"", shortQuote(hit.chunk.Text))
	}
	return b.String()
}

func buildResponseHits(hits []rankedHit) []queryHit {
	if len(hits) == 0 {
		return nil
	}
	out := make([]queryHit, 0, len(hits))
	for i, hit := range hits {
		role := ""
		switch {
		case hit.chunk.HasHa:
			role = "practical"
		case hit.chunk.HasThuong:
			role = "philosophical"
		case i == 0:
			role = "primary"
		}
		out = append(out, queryHit{
			Ref:           fmt.Sprintf("[%d]", i+1),
			SectionNumber: hit.chunk.SectionNumber,
			SectionName:   hit.chunk.SectionName,
			SectionTitle:  hit.chunk.SectionTitle,
			Heading:       trimHeading(hit.chunk.SectionHeading),
			ChunkOrder:    hit.chunk.Order,
			Score:         hit.score,
			SemanticScore: hit.semanticScore,
			KeywordScore:  hit.keywordScore,
			Quote:         shortQuote(hit.chunk.Text),
			Source:        hit.chunk.DisplaySource,
			Role:          role,
		})
	}
	return out
}

func firstMatchingHit(hits []rankedHit, predicate func(rankedHit) bool) *rankedHit {
	for i := range hits {
		if predicate(hits[i]) {
			return &hits[i]
		}
	}
	return nil
}

func referenceIndex(hits []rankedHit, predicate func(rankedHit) bool) int {
	for i := range hits {
		if predicate(hits[i]) {
			return i
		}
	}
	return -1
}

func shortQuote(text string) string {
	text = normalizeSpace(text)
	if text == "" {
		return ""
	}
	for _, sentence := range splitSentences(text) {
		sentence = normalizeSpace(sentence)
		if sentence == "" || dailyiching.IsLikelyNoisyOCRText(sentence) {
			continue
		}
		if len([]rune(sentence)) <= 120 {
			return sentence
		}
		return trimRunes(sentence, 117) + "..."
	}
	if len([]rune(text)) <= 120 {
		return text
	}
	return trimRunes(text, 117) + "..."
}

func splitSentences(value string) []string {
	value = normalizeSpace(value)
	if value == "" {
		return nil
	}
	var (
		out []string
		b   strings.Builder
	)
	for _, r := range value {
		b.WriteRune(r)
		switch r {
		case '.', '!', '?', ';':
			out = append(out, b.String())
			b.Reset()
		}
	}
	if tail := strings.TrimSpace(b.String()); tail != "" {
		out = append(out, tail)
	}
	return out
}

func trimHeading(value string) string {
	value = normalizeSpace(value)
	if len([]rune(value)) <= 96 {
		return value
	}
	return trimRunes(value, 93) + "..."
}

func countTokenOverlap(haystackTokens, queryTokens []string) int {
	if len(haystackTokens) == 0 || len(queryTokens) == 0 {
		return 0
	}
	haystack := make(map[string]struct{}, len(haystackTokens))
	for _, token := range haystackTokens {
		haystack[token] = struct{}{}
	}
	score := 0
	for _, token := range queryTokens {
		if _, ok := haystack[token]; ok {
			score++
		}
	}
	return score
}

func countPhraseMatches(normalized string, phrases []string) int {
	if normalized == "" || len(phrases) == 0 {
		return 0
	}
	hits := 0
	for _, phrase := range phrases {
		if phrase == "" {
			continue
		}
		if containsWholePhrase(normalized, phrase) {
			hits++
		}
	}
	if hits > 3 {
		return 3
	}
	return hits
}

func clampSimilarity(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func providerName(provider *resolvedEmbeddingProvider) string {
	if provider == nil {
		return ""
	}
	return provider.name
}

func providerModel(provider *resolvedEmbeddingProvider) string {
	if provider == nil {
		return ""
	}
	return provider.model
}

func (f *DailyIChingQueryFeature) resolveEmbeddingProvider(ctx context.Context, tenantID string) (*resolvedEmbeddingProvider, error) {
	if f == nil {
		return nil, nil
	}
	tenantCtx := tenantScopedContext(ctx, tenantID)
	overrideModel := ""

	if f.stores != nil && f.stores.SystemConfigs != nil {
		overrideModel, _ = f.stores.SystemConfigs.Get(tenantCtx, "embedding.model")
		if providerName, _ := f.stores.SystemConfigs.Get(tenantCtx, "embedding.provider"); strings.TrimSpace(providerName) != "" {
			if provider := f.resolveNamedEmbeddingProvider(tenantCtx, providerName, overrideModel); provider != nil {
				return provider, nil
			}
		}
	}

	if f.stores != nil && f.stores.Providers != nil {
		providers, err := f.stores.Providers.ListProviders(tenantCtx)
		if err != nil {
			return nil, err
		}
		for _, providerData := range providers {
			if !providerData.Enabled || store.NoEmbeddingTypes[providerData.ProviderType] {
				continue
			}
			settings := store.ParseEmbeddingSettings(providerData.Settings)
			if settings == nil || !settings.Enabled {
				continue
			}
			if provider := f.buildEmbeddingProviderFromData(&providerData, overrideModel); provider != nil {
				return provider, nil
			}
		}
	}

	if f.config != nil && strings.TrimSpace(f.config.Providers.OpenAI.APIKey) != "" {
		model := strings.TrimSpace(overrideModel)
		if model == "" {
			model = defaultEmbeddingModel
		}
		ep := memembed.NewOpenAIEmbeddingProvider("openai", f.config.Providers.OpenAI.APIKey, f.config.Providers.OpenAI.APIBase, model)
		return &resolvedEmbeddingProvider{provider: ep, name: ep.Name(), model: ep.Model()}, nil
	}

	return nil, nil
}

func (f *DailyIChingQueryFeature) resolveNamedEmbeddingProvider(ctx context.Context, name, overrideModel string) *resolvedEmbeddingProvider {
	if f == nil || f.stores == nil || f.stores.Providers == nil {
		return nil
	}
	providerData, err := f.stores.Providers.GetProviderByName(ctx, strings.TrimSpace(name))
	if err != nil || providerData == nil || !providerData.Enabled || store.NoEmbeddingTypes[providerData.ProviderType] {
		return nil
	}
	return f.buildEmbeddingProviderFromData(providerData, overrideModel)
}

func (f *DailyIChingQueryFeature) buildEmbeddingProviderFromData(providerData *store.LLMProviderData, overrideModel string) *resolvedEmbeddingProvider {
	if providerData == nil || strings.TrimSpace(providerData.APIKey) == "" {
		return nil
	}

	settings := store.ParseEmbeddingSettings(providerData.Settings)
	model := strings.TrimSpace(overrideModel)
	if model == "" && settings != nil && settings.Model != "" {
		model = settings.Model
	}
	if model == "" {
		model = defaultEmbeddingModel
	}

	apiBase := ""
	if settings != nil && settings.APIBase != "" {
		apiBase = settings.APIBase
	}
	if apiBase == "" {
		apiBase = strings.TrimSpace(providerData.APIBase)
	}
	if apiBase == "" && f.config != nil {
		apiBase = f.config.Providers.APIBaseForType(providerData.ProviderType)
	}

	ep := memembed.NewOpenAIEmbeddingProvider(providerData.Name, providerData.APIKey, apiBase, model)
	if settings != nil && settings.Dimensions > 0 {
		ep.WithDimensions(settings.Dimensions)
	}
	return &resolvedEmbeddingProvider{provider: ep, name: ep.Name(), model: ep.Model()}
}

func (f *DailyIChingQueryFeature) embedQuestion(ctx context.Context, provider *resolvedEmbeddingProvider, tenantID, normalizedQuestion string) ([]float32, error) {
	if provider == nil || provider.provider == nil || normalizedQuestion == "" {
		return nil, nil
	}

	cacheKey := f.queryEmbeddingKey(tenantID, provider.name, provider.model, normalizedQuestion)
	f.cacheMu.RLock()
	if embedding, ok := f.queryEmbeddings[cacheKey]; ok {
		f.cacheMu.RUnlock()
		return embedding, nil
	}
	f.cacheMu.RUnlock()

	embedCtx, cancel := context.WithTimeout(ctx, embeddingRequestTimeout)
	defer cancel()

	embeddings, err := provider.provider.Embed(embedCtx, []string{normalizedQuestion})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 || len(embeddings[0]) == 0 {
		return nil, fmt.Errorf("embedding provider returned no query embedding")
	}
	embedding := append([]float32(nil), embeddings[0]...)

	f.cacheMu.Lock()
	f.queryEmbeddings[cacheKey] = embedding
	f.cacheMu.Unlock()
	return embedding, nil
}

func (f *DailyIChingQueryFeature) prepareChunkEmbeddings(index *compiledIndex, provider *resolvedEmbeddingProvider, tenantID string) (embeddingWarmState, error) {
	state := embeddingWarmState{}
	if index == nil || provider == nil || provider.provider == nil {
		return state, nil
	}

	namespace := f.chunkNamespace(tenantID, provider.name, provider.model, index.SourceSignature)
	state.Namespace = namespace
	state.TotalCount = len(index.FlatChunks)

	f.backfillMu.Lock()
	defer f.backfillMu.Unlock()

	f.cacheMu.RLock()
	loaded := f.loadedNamespaces[namespace]
	f.cacheMu.RUnlock()

	if !loaded {
		records, err := f.store.listChunkEmbeddings(strings.TrimSpace(tenantID), index.SourceSignature, provider.name, provider.model)
		if err != nil {
			return state, err
		}
		f.cacheMu.Lock()
		for _, record := range records {
			f.chunkEmbeddings[f.chunkEmbeddingKey(namespace, fmt.Sprintf("%d:%d", record.SectionNumber, record.ChunkOrder))] = append([]float32(nil), record.Embedding...)
		}
		f.loadedNamespaces[namespace] = true
		f.cacheMu.Unlock()
	}

	state.LoadedCount = f.loadedChunkEmbeddingCount(namespace, index)
	state.Warming = f.warmingNamespaces[namespace]
	if state.Ready() || state.Warming {
		return state, nil
	}

	f.warmingNamespaces[namespace] = true
	state.Warming = true
	slog.Info("daily iching query embedding warm started",
		"loaded", state.LoadedCount,
		"total", state.TotalCount,
		"missing", state.MissingCount(),
	)
	go f.backfillChunkEmbeddings(index, provider, tenantID, namespace)
	return state, nil
}

func (f *DailyIChingQueryFeature) backfillChunkEmbeddings(index *compiledIndex, provider *resolvedEmbeddingProvider, tenantID, namespace string) {
	err := f.populateMissingChunkEmbeddings(tenantScopedContext(context.Background(), tenantID), index, provider, tenantID, namespace)
	loaded := f.loadedChunkEmbeddingCount(namespace, index)

	f.backfillMu.Lock()
	delete(f.warmingNamespaces, namespace)
	f.backfillMu.Unlock()

	if err != nil {
		slog.Warn("daily iching query embedding warm failed",
			"loaded", loaded,
			"total", len(index.FlatChunks),
			"error", err,
		)
		return
	}
	slog.Info("daily iching query embedding warm completed",
		"loaded", loaded,
		"total", len(index.FlatChunks),
	)
}

func (f *DailyIChingQueryFeature) populateMissingChunkEmbeddings(ctx context.Context, index *compiledIndex, provider *resolvedEmbeddingProvider, tenantID, namespace string) error {
	if index == nil || provider == nil || provider.provider == nil {
		return nil
	}

	f.cacheMu.RLock()
	missing := make([]indexedChunk, 0)
	for _, chunk := range index.FlatChunks {
		if _, ok := f.chunkEmbeddings[f.chunkEmbeddingKey(namespace, chunk.Key)]; !ok {
			missing = append(missing, chunk)
		}
	}
	f.cacheMu.RUnlock()
	if len(missing) == 0 {
		return nil
	}

	for start := 0; start < len(missing); start += embeddingBatchSize {
		end := start + embeddingBatchSize
		if end > len(missing) {
			end = len(missing)
		}
		batch := missing[start:end]
		texts := make([]string, 0, len(batch))
		for _, chunk := range batch {
			texts = append(texts, chunk.Text)
		}

		embedCtx, cancel := context.WithTimeout(ctx, embeddingRequestTimeout)
		embeddings, err := provider.provider.Embed(embedCtx, texts)
		cancel()
		if err != nil {
			return err
		}
		if len(embeddings) != len(batch) {
			return fmt.Errorf("embedding provider returned %d chunk embeddings for %d texts", len(embeddings), len(batch))
		}

		records := make([]chunkEmbeddingRecord, 0, len(batch))
		for i, chunk := range batch {
			if len(embeddings[i]) == 0 {
				continue
			}
			records = append(records, chunkEmbeddingRecord{
				TenantID:        strings.TrimSpace(tenantID),
				SourceSignature: index.SourceSignature,
				ProviderName:    provider.name,
				ProviderModel:   provider.model,
				SectionNumber:   chunk.SectionNumber,
				ChunkOrder:      chunk.Order,
				ChunkTextHash:   memembed.ContentHash(chunk.Normalized),
				Embedding:       append([]float32(nil), embeddings[i]...),
			})
		}
		if err := f.store.upsertChunkEmbeddings(records); err != nil {
			return err
		}

		f.cacheMu.Lock()
		for _, record := range records {
			f.chunkEmbeddings[f.chunkEmbeddingKey(namespace, fmt.Sprintf("%d:%d", record.SectionNumber, record.ChunkOrder))] = append([]float32(nil), record.Embedding...)
		}
		f.cacheMu.Unlock()
	}

	return nil
}

func (f *DailyIChingQueryFeature) loadedChunkEmbeddingCount(namespace string, index *compiledIndex) int {
	if namespace == "" || index == nil {
		return 0
	}
	f.cacheMu.RLock()
	defer f.cacheMu.RUnlock()

	count := 0
	for _, chunk := range index.FlatChunks {
		if _, ok := f.chunkEmbeddings[f.chunkEmbeddingKey(namespace, chunk.Key)]; ok {
			count++
		}
	}
	return count
}

func (f *DailyIChingQueryFeature) lookupChunkEmbedding(namespace, chunkKey string) []float32 {
	f.cacheMu.RLock()
	defer f.cacheMu.RUnlock()
	return f.chunkEmbeddings[f.chunkEmbeddingKey(namespace, chunkKey)]
}

func (f *DailyIChingQueryFeature) chunkNamespace(tenantID, providerName, providerModel, sourceSignature string) string {
	return strings.Join([]string{
		strings.TrimSpace(tenantID),
		strings.TrimSpace(providerName),
		strings.TrimSpace(providerModel),
		strings.TrimSpace(sourceSignature),
	}, "|")
}

func (f *DailyIChingQueryFeature) chunkEmbeddingKey(namespace, chunkKey string) string {
	return namespace + "|" + strings.TrimSpace(chunkKey)
}

func (f *DailyIChingQueryFeature) queryEmbeddingKey(tenantID, providerName, providerModel, question string) string {
	return strings.Join([]string{
		strings.TrimSpace(tenantID),
		strings.TrimSpace(providerName),
		strings.TrimSpace(providerModel),
		memembed.ContentHash(question),
	}, "|")
}
