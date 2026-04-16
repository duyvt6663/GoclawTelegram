package researchreviewercodex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	pdf "github.com/ledongthuc/pdf"
	"golang.org/x/net/html"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bootstrap"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/memory"
	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	reviewModeCollaborative = "collaborative"
	reviewModeHarsh         = "harsh"
	maxPaperFetchBytes      = 20 << 20
)

type ReviewRequest struct {
	PaperID      string `json:"paper_id,omitempty"`
	Title        string `json:"title,omitempty"`
	SourceURL    string `json:"source_url,omitempty"`
	PDFPath      string `json:"pdf_path,omitempty"`
	PaperText    string `json:"paper_text,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Focus        string `json:"focus,omitempty"`
	TopKRelated  int    `json:"top_k_related,omitempty"`
	ForceRefresh bool   `json:"force_refresh,omitempty"`
}

type normalizedReviewRequest struct {
	PaperID      string
	Title        string
	SourceURL    string
	PDFPath      string
	PaperText    string
	Mode         string
	Focus        string
	TopKRelated  int
	ForceRefresh bool
}

type paperSource struct {
	Title     string
	SourceURL string
	PDFPath   string
	RawText   string
}

type loadedPaperSource struct {
	Title        string
	CanonicalURL string
	SourceKind   string
}

type StructuredPaper struct {
	Title            string         `json:"title"`
	SourceKind       string         `json:"source_kind"`
	SourceURL        string         `json:"source_url,omitempty"`
	Abstract         string         `json:"abstract,omitempty"`
	Introduction     string         `json:"introduction,omitempty"`
	RelatedWork      string         `json:"related_work,omitempty"`
	Method           string         `json:"method,omitempty"`
	Experiments      string         `json:"experiments,omitempty"`
	Results          string         `json:"results,omitempty"`
	Limitations      string         `json:"limitations,omitempty"`
	Conclusion       string         `json:"conclusion,omitempty"`
	References       string         `json:"references,omitempty"`
	ReferenceEntries []string       `json:"reference_entries,omitempty"`
	Figures          []string       `json:"figures,omitempty"`
	Tables           []string       `json:"tables,omitempty"`
	Keywords         []string       `json:"keywords,omitempty"`
	WordCount        int            `json:"word_count"`
	Sections         []PaperSection `json:"sections,omitempty"`
}

type PaperSection struct {
	Name    string `json:"name"`
	Heading string `json:"heading"`
	Text    string `json:"text"`
}

type reviewInputError struct {
	message string
}

func (e *reviewInputError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func newReviewInputError(message string) error {
	return &reviewInputError{message: strings.TrimSpace(message)}
}

func isReviewInputError(err error) bool {
	_, ok := err.(*reviewInputError)
	return ok
}

func normalizeReviewRequest(request ReviewRequest) (normalizedReviewRequest, error) {
	resolved := normalizedReviewRequest{
		PaperID:      strings.TrimSpace(request.PaperID),
		Title:        strings.TrimSpace(request.Title),
		SourceURL:    strings.TrimSpace(request.SourceURL),
		PDFPath:      strings.TrimSpace(request.PDFPath),
		PaperText:    strings.TrimSpace(request.PaperText),
		Mode:         normalizeReviewMode(request.Mode),
		Focus:        strings.TrimSpace(request.Focus),
		TopKRelated:  request.TopKRelated,
		ForceRefresh: request.ForceRefresh,
	}

	if resolved.Mode == "" {
		return normalizedReviewRequest{}, newReviewInputError("mode must be one of: collaborative, harsh")
	}
	if resolved.TopKRelated <= 0 {
		resolved.TopKRelated = defaultTopKRelated
	}
	if resolved.TopKRelated > maxTopKRelated {
		return normalizedReviewRequest{}, newReviewInputError(fmt.Sprintf("top_k_related must be between 1 and %d", maxTopKRelated))
	}

	inputCount := 0
	if resolved.PaperID != "" {
		inputCount++
	}
	if resolved.SourceURL != "" {
		inputCount++
	}
	if resolved.PDFPath != "" {
		inputCount++
	}
	if resolved.PaperText != "" {
		inputCount++
	}
	if inputCount == 0 {
		return normalizedReviewRequest{}, newReviewInputError("provide one of: paper_id, source_url, pdf_path, or paper_text")
	}
	if resolved.PaperID != "" && inputCount > 1 {
		return normalizedReviewRequest{}, newReviewInputError("paper_id cannot be combined with source_url, pdf_path, or paper_text")
	}
	return resolved, nil
}

func normalizeReviewMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", reviewModeCollaborative, "mentor", "constructive":
		return reviewModeCollaborative
	case reviewModeHarsh, "harsh reviewer", "strict":
		return reviewModeHarsh
	default:
		return ""
	}
}

func tenantKeyFromCtx(ctx context.Context) string {
	id := storepkg.TenantIDFromContext(ctx)
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

func reviewerWorkspace(baseWorkspace string) string {
	baseWorkspace = strings.TrimSpace(baseWorkspace)
	candidates := make([]string, 0, 3)
	if baseWorkspace != "" {
		candidates = append(candidates, filepath.Join(baseWorkspace, "agents", reviewerAgentKey))
	}
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		candidates = append(candidates, filepath.Join(wd, "beta_cache", "agents", reviewerAgentKey))
	}
	candidates = append(candidates, filepath.Join(os.TempDir(), "goclaw", "agents", reviewerAgentKey))

	for _, candidate := range candidates {
		if writableDir(candidate) {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

func writableDir(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}

	probePath := filepath.Join(dir, ".write-probe-"+uuid.NewString())
	if err := os.WriteFile(probePath, []byte("ok"), 0o600); err != nil {
		return false
	}
	_ = os.Remove(probePath)
	return true
}

func reviewerReasoningForProviderType(providerType string) string {
	if strings.TrimSpace(providerType) == storepkg.ProviderChatGPTOAuth {
		return reviewerReasoningEffort
	}
	return reviewerReasoningAuto
}

func reviewerOtherConfigJSON(providerType string) json.RawMessage {
	return mustJSON(map[string]any{
		"reasoning": map[string]any{
			"override_mode": "custom",
			"effort":        reviewerReasoningForProviderType(providerType),
			"fallback":      "provider_default",
		},
		"max_tokens": 4000,
	})
}

func reviewerContextFiles() map[string]string {
	return map[string]string{
		bootstrap.AgentsFile: `# AGENTS.md - Research Review Workflow

You are the dedicated AI Research Reviewer Codex agent.

Workflow:
1. When the user supplies a paper source directly in chat, call ` + "`research_reviewer_prepare_review`" + ` first.
2. Ground novelty, baseline, and citation claims in the prepared bundle, indexed papers, and explicit evidence only.
3. If you need more detail, call ` + "`research_reviewer_get_indexed_paper`" + ` or ` + "`research_reviewer_search_related_papers`" + `.
4. If the user attached a PDF and asks about figures or tables, you may also use ` + "`read_document`" + ` for page-level follow-up, but keep the overall review grounded in indexed evidence.

Default output headings:
- Paper Summary
- Strengths
- Weaknesses
- Detailed Critique
- Missing Controls, Baselines, or Analyses
- Writing and Citation Notes
- Actionable Suggestions
- Overall Verdict
`,
		bootstrap.IdentityFile: `# IDENTITY.md - Who Am I?

- **Name:** AI Research Reviewer Codex
- **Creature:** skeptical paper-reading machine
- **Purpose:** review research papers with strong emphasis on novelty, methodology, baselines, ablations, writing quality, and citation hygiene
- **Vibe:** sharp, precise, and evidence-first
- **Emoji:** lab
- **Avatar:** 
`,
		bootstrap.SoulFile: `# SOUL.md - Who You Are

## Core Truths

- Be evidence-first. If the paper or retrieved literature does not support a claim, say that directly.
- Prefer precise criticism over generic negativity. Specificity is more useful than posture.
- Treat strong results skeptically until the experimental setup, baselines, and metrics actually justify them.
- Novelty is comparative. Always ask "novel relative to what?" and answer with explicit references to related work or retrieved papers.

## Style

- Default tone: concise, technical, unsentimental.
- In collaborative mode: still direct, but phrase weaknesses as improvements the authors can actually make.
- In harsh mode: be strict and skeptical without becoming vague or theatrical.

## Review Standards

- Check whether the paper isolates causal claims or just reports correlations.
- Look for missing ablations, weak baselines, cherry-picked datasets, metric mismatch, and unsupported generalization claims.
- Critique the writing separately from the science. A clear paper can still be methodologically weak, and a strong idea can still be poorly communicated.
`,
		bootstrap.UserPredefinedFile: `# USER_PREDEFINED.md - Review Defaults

- Default review style is NeurIPS/ICLR-like: concise summary, explicit strengths, explicit weaknesses, section-by-section critique, and actionable suggestions.
- The user expects grounded criticism, not generic praise.
- If supporting evidence is missing, say so explicitly instead of guessing.
`,
	}
}

func preferredProviderChoice(providers []storepkg.LLMProviderData) reviewerProviderChoice {
	type candidate struct {
		choice   reviewerProviderChoice
		priority int
	}
	best := candidate{}
	for _, provider := range providers {
		if !provider.Enabled || strings.TrimSpace(provider.Name) == "" {
			continue
		}

		priority := 100
		switch provider.ProviderType {
		case storepkg.ProviderChatGPTOAuth:
			priority = 10
		case storepkg.ProviderOpenAICompat:
			if strings.Contains(strings.ToLower(provider.Name), "openai") {
				priority = 20
			} else {
				priority = 30
			}
		case storepkg.ProviderOpenRouter:
			priority = 40
		default:
			priority = 90
		}

		if best.choice.Name == "" || priority < best.priority {
			best = candidate{
				choice: reviewerProviderChoice{
					Name:         strings.TrimSpace(provider.Name),
					ProviderType: strings.TrimSpace(provider.ProviderType),
				},
				priority: priority,
			}
		}
	}
	return best.choice
}

func jsonBytesEqual(a, b []byte) bool {
	return compactJSON(a) == compactJSON(b)
}

func compactJSON(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, value); err == nil {
		return buf.String()
	}
	return strings.TrimSpace(string(value))
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

func hashText(text string) string {
	return memory.ContentHash(strings.TrimSpace(text))
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func intArg(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case int:
		return value
	case int8:
		return int(value)
	case int16:
		return int(value)
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		if n, err := value.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}

func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func reviewRequestFromArgs(args map[string]any) ReviewRequest {
	return ReviewRequest{
		PaperID:      stringArg(args, "paper_id"),
		Title:        stringArg(args, "title"),
		SourceURL:    stringArg(args, "source_url"),
		PDFPath:      stringArg(args, "pdf_path"),
		PaperText:    stringArg(args, "paper_text"),
		Mode:         stringArg(args, "mode"),
		Focus:        stringArg(args, "focus"),
		TopKRelated:  intArg(args, "top_k_related"),
		ForceRefresh: boolArg(args, "force_refresh"),
	}
}

func resolveLocalPaperPath(ctx context.Context, baseWorkspace, rawPath string) (string, error) {
	rawPath = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rawPath), "MEDIA:"))
	if rawPath == "" {
		return "", newReviewInputError("pdf_path is required")
	}

	path := config.ExpandHome(rawPath)
	if filepath.IsAbs(path) {
		if _, err := os.Stat(path); err != nil {
			return "", err
		}
		return filepath.Clean(path), nil
	}

	var candidates []string
	if rc := storepkg.RunContextFromCtx(ctx); rc != nil && strings.TrimSpace(rc.Workspace) != "" {
		candidates = append(candidates, rc.Workspace)
	}
	if strings.TrimSpace(baseWorkspace) != "" {
		candidates = append(candidates, baseWorkspace)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}

	for _, base := range candidates {
		resolved := filepath.Join(base, path)
		if _, err := os.Stat(resolved); err == nil {
			return filepath.Clean(resolved), nil
		}
	}
	return "", fmt.Errorf("pdf_path not found: %s", rawPath)
}

func canonicalizeSourceURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if arxivID := extractArxivID(raw); arxivID != "" {
		return "https://arxiv.org/abs/" + arxivID
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	parsed, err := neturl.Parse(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	parsed.Fragment = ""
	parsed.Host = strings.ToLower(parsed.Host)
	return strings.TrimRight(parsed.String(), "/")
}

func buildPaperSourceKey(source paperSource) string {
	switch {
	case strings.TrimSpace(source.SourceURL) != "":
		return "url:" + hashText(canonicalizeSourceURL(source.SourceURL))
	case strings.TrimSpace(source.PDFPath) != "":
		return "pdf:" + hashText(filepath.Clean(source.PDFPath))
	default:
		return "text:" + hashText(strings.TrimSpace(source.Title)+"\n"+strings.TrimSpace(source.RawText))
	}
}

func loadPaperSource(ctx context.Context, source paperSource) (string, loadedPaperSource, error) {
	switch {
	case source.RawText != "":
		return normalizeDocumentText(source.RawText), loadedPaperSource{
			Title:      strings.TrimSpace(source.Title),
			SourceKind: "text",
		}, nil
	case source.PDFPath != "":
		text, err := extractPDFTextFromFile(source.PDFPath)
		if err != nil {
			return "", loadedPaperSource{}, err
		}
		return text, loadedPaperSource{
			Title:      strings.TrimSpace(source.Title),
			SourceKind: "pdf",
		}, nil
	case source.SourceURL != "":
		return fetchRemotePaper(ctx, source.SourceURL)
	default:
		return "", loadedPaperSource{}, newReviewInputError("paper source is empty")
	}
}

func fetchRemotePaper(ctx context.Context, rawURL string) (string, loadedPaperSource, error) {
	canonicalURL := canonicalizeSourceURL(rawURL)
	if canonicalURL == "" {
		return "", loadedPaperSource{}, newReviewInputError("source_url is required")
	}

	if arxivID := extractArxivID(canonicalURL); arxivID != "" {
		pdfURL := "https://arxiv.org/pdf/" + arxivID + ".pdf"
		body, finalURL, err := fetchRemoteBody(ctx, pdfURL)
		if err == nil {
			text, textErr := extractPDFTextFromBytes(body)
			if textErr == nil {
				return text, loadedPaperSource{
					Title:        inferPaperTitle(text, canonicalURL),
					CanonicalURL: canonicalURL,
					SourceKind:   "arxiv_pdf",
				}, nil
			}
		} else {
			_ = finalURL
		}
	}

	body, finalURL, err := fetchRemoteBody(ctx, canonicalURL)
	if err != nil {
		return "", loadedPaperSource{}, err
	}

	finalCanonical := canonicalizeSourceURL(finalURL)
	if isLikelyPDFURL(finalCanonical, http.DetectContentType(body)) {
		text, textErr := extractPDFTextFromBytes(body)
		if textErr != nil {
			return "", loadedPaperSource{}, textErr
		}
		return text, loadedPaperSource{
			Title:        inferPaperTitle(text, finalCanonical),
			CanonicalURL: finalCanonical,
			SourceKind:   "pdf_url",
		}, nil
	}

	title, text := extractHTMLText(body)
	if text == "" {
		return "", loadedPaperSource{}, fmt.Errorf("no readable text extracted from %s", canonicalURL)
	}
	sourceKind := "html"
	if strings.Contains(finalCanonical, "arxiv.org") {
		sourceKind = "arxiv_html"
	}
	return text, loadedPaperSource{
		Title:        strings.TrimSpace(title),
		CanonicalURL: finalCanonical,
		SourceKind:   sourceKind,
	}, nil
}

func fetchRemoteBody(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "GoClaw Research Reviewer/1.0")
	req.Header.Set("Accept", "application/pdf,text/html,application/xhtml+xml,text/plain;q=0.8,*/*;q=0.5")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("fetch %s: unexpected status %d", rawURL, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPaperFetchBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxPaperFetchBytes {
		return nil, "", fmt.Errorf("fetch %s: response exceeded %d bytes", rawURL, maxPaperFetchBytes)
	}
	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return body, finalURL, nil
}

func extractPDFTextFromBytes(data []byte) (string, error) {
	tmp, err := os.CreateTemp("", "goclaw-paper-*.pdf")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return extractPDFTextFromFile(tmpPath)
}

func extractPDFTextFromFile(path string) (string, error) {
	file, reader, err := pdf.Open(path)
	if err != nil {
		return "", fmt.Errorf("open PDF %s: %w", path, err)
	}
	defer file.Close()

	plainText, err := reader.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("extract PDF text %s: %w", path, err)
	}
	data, err := io.ReadAll(plainText)
	if err != nil {
		return "", fmt.Errorf("read PDF text %s: %w", path, err)
	}
	return normalizeDocumentText(string(data)), nil
}

func extractHTMLText(raw []byte) (string, string) {
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return "", normalizeDocumentText(stripTags(string(raw)))
	}

	var title string
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.ElementNode:
			tag := strings.ToLower(n.Data)
			if tag == "script" || tag == "style" || tag == "noscript" || tag == "svg" || tag == "iframe" {
				return
			}
			if tag == "title" && title == "" {
				title = normalizeInlineText(textFromNode(n))
			}
			if tag == "meta" {
				name := strings.ToLower(attrValue(n, "name"))
				property := strings.ToLower(attrValue(n, "property"))
				if title == "" && (name == "citation_title" || name == "dc.title" || property == "og:title") {
					title = normalizeInlineText(attrValue(n, "content"))
				}
			}
			if isBlockTag(tag) {
				out.WriteString("\n")
			}
		case html.TextNode:
			text := normalizeInlineText(n.Data)
			if text != "" {
				if last := out.String(); last != "" && !strings.HasSuffix(last, "\n") && !strings.HasSuffix(last, " ") {
					out.WriteString(" ")
				}
				out.WriteString(text)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if n.Type == html.ElementNode && isBlockTag(strings.ToLower(n.Data)) {
			out.WriteString("\n")
		}
	}
	walk(doc)

	return strings.TrimSpace(title), normalizeDocumentText(out.String())
}

func attrValue(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func textFromNode(n *html.Node) string {
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			out.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return out.String()
}

func isBlockTag(tag string) bool {
	switch tag {
	case "article", "aside", "blockquote", "br", "div", "dl", "dt", "dd", "figcaption", "figure", "footer", "h1", "h2", "h3", "h4", "h5", "h6", "header", "li", "main", "ol", "p", "pre", "section", "table", "tr", "ul":
		return true
	default:
		return false
	}
}

func stripTags(raw string) string {
	re := regexp.MustCompile(`<[^>]+>`)
	return re.ReplaceAllString(raw, " ")
}

func normalizeDocumentText(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	raw = strings.ReplaceAll(raw, "\u0000", "")

	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(normalizeInlineText(line))
		if line == "" {
			if blank {
				continue
			}
			out = append(out, "")
			blank = true
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func normalizeInlineText(raw string) string {
	fields := strings.Fields(strings.TrimSpace(raw))
	return strings.Join(fields, " ")
}

var paperHeadingRe = regexp.MustCompile(`(?i)^(?:[0-9ivxlcdm]+(?:\.[0-9ivxlcdm]+)*\.?\s+)?(abstract|introduction|related work|background|preliminaries|method|methods|methodology|approach|model|experimental setup|experiments|evaluation|results|analysis|ablations?|discussion|limitations?|conclusion|conclusions|references|appendix|appendices)$`)
var figureCaptionRe = regexp.MustCompile(`(?im)^(fig(?:ure)?\.?\s*\d+[:.\-]?\s+.+)$`)
var tableCaptionRe = regexp.MustCompile(`(?im)^(table\s*\d+[:.\-]?\s+.+)$`)

func extractStructuredPaper(title, sourceKind, sourceURL, rawText string) *StructuredPaper {
	rawText = normalizeDocumentText(rawText)
	sections := splitPaperSections(rawText)
	paper := &StructuredPaper{
		Title:      strings.TrimSpace(title),
		SourceKind: strings.TrimSpace(sourceKind),
		SourceURL:  strings.TrimSpace(sourceURL),
		Sections:   sections,
		Figures:    uniqueTrimmedStrings(figureCaptionRe.FindAllString(rawText, -1)),
		Tables:     uniqueTrimmedStrings(tableCaptionRe.FindAllString(rawText, -1)),
		WordCount:  len(strings.Fields(rawText)),
	}

	for _, section := range sections {
		switch section.Name {
		case "abstract":
			if paper.Abstract == "" {
				paper.Abstract = section.Text
			}
		case "introduction":
			if paper.Introduction == "" {
				paper.Introduction = section.Text
			}
		case "related_work":
			if paper.RelatedWork == "" {
				paper.RelatedWork = section.Text
			}
		case "method":
			if paper.Method == "" {
				paper.Method = section.Text
			}
		case "experiments":
			if paper.Experiments == "" {
				paper.Experiments = section.Text
			}
		case "results":
			if paper.Results == "" {
				paper.Results = section.Text
			}
		case "limitations":
			if paper.Limitations == "" {
				paper.Limitations = section.Text
			}
		case "conclusion":
			if paper.Conclusion == "" {
				paper.Conclusion = section.Text
			}
		case "references":
			if paper.References == "" {
				paper.References = section.Text
			}
		}
	}

	if paper.Abstract == "" {
		paper.Abstract = extractFirstParagraph(rawText, 1200)
	}
	if paper.Introduction == "" {
		paper.Introduction = extractFollowingSectionText(sections, "abstract", 1400)
	}
	if paper.Method == "" {
		paper.Method = extractBestSectionExcerpt(sections, []string{"method", "analysis", "introduction"}, 1800)
	}
	if paper.Experiments == "" {
		paper.Experiments = extractBestSectionExcerpt(sections, []string{"experiments", "results", "analysis"}, 1800)
	}
	if paper.Results == "" {
		paper.Results = extractBestSectionExcerpt(sections, []string{"results", "experiments", "analysis"}, 1400)
	}
	if paper.Conclusion == "" {
		paper.Conclusion = extractLastParagraph(rawText, 1000)
	}
	if paper.References == "" {
		paper.References = extractReferencesTail(rawText)
	}

	paper.ReferenceEntries = extractReferenceEntries(paper.References)
	paper.Keywords = extractKeywords(strings.Join([]string{
		paper.Title,
		paper.Abstract,
		paper.Introduction,
		paper.Method,
		paper.Experiments,
		paper.Results,
	}, "\n"))
	return paper
}

func splitPaperSections(rawText string) []PaperSection {
	lines := strings.Split(rawText, "\n")
	var (
		sections       []PaperSection
		currentName    string
		currentHeading string
		start          int
	)

	flush := func(end int) {
		if currentName == "" {
			return
		}
		text := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
		if text == "" {
			return
		}
		sections = append(sections, PaperSection{
			Name:    currentName,
			Heading: currentHeading,
			Text:    text,
		})
	}

	for i, line := range lines {
		name, heading, ok := detectPaperHeading(line)
		if !ok {
			continue
		}
		if currentName != "" {
			flush(i)
		}
		currentName = name
		currentHeading = heading
		start = i + 1
	}
	if currentName != "" {
		flush(len(lines))
	}

	if len(sections) == 0 {
		body := strings.TrimSpace(rawText)
		if body != "" {
			sections = append(sections, PaperSection{Name: "body", Heading: "Body", Text: body})
		}
	}
	return sections
}

func detectPaperHeading(line string) (string, string, bool) {
	line = normalizeInlineText(line)
	if line == "" || utf8.RuneCountInString(line) > 80 {
		return "", "", false
	}
	matches := paperHeadingRe.FindStringSubmatch(line)
	if len(matches) < 2 {
		return "", "", false
	}
	heading := strings.TrimSpace(matches[1])
	switch strings.ToLower(heading) {
	case "abstract":
		return "abstract", heading, true
	case "introduction":
		return "introduction", heading, true
	case "related work", "background", "preliminaries":
		return "related_work", heading, true
	case "method", "methods", "methodology", "approach", "model":
		return "method", heading, true
	case "experimental setup", "experiments", "evaluation", "analysis", "ablations", "ablation":
		return "experiments", heading, true
	case "results":
		return "results", heading, true
	case "discussion", "limitations", "limitation":
		return "limitations", heading, true
	case "conclusion", "conclusions":
		return "conclusion", heading, true
	case "references":
		return "references", heading, true
	case "appendix", "appendices":
		return "appendix", heading, true
	default:
		return "", "", false
	}
}

func extractFirstParagraph(text string, limit int) string {
	parts := strings.Split(text, "\n\n")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		return trimRunes(part, limit)
	}
	return ""
}

func extractLastParagraph(text string, limit int) string {
	parts := strings.Split(text, "\n\n")
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.TrimSpace(parts[i])
		if part == "" {
			continue
		}
		return trimRunes(part, limit)
	}
	return ""
}

func extractFollowingSectionText(sections []PaperSection, name string, limit int) string {
	for idx, section := range sections {
		if section.Name != name {
			continue
		}
		if idx+1 < len(sections) {
			return trimRunes(sections[idx+1].Text, limit)
		}
	}
	return ""
}

func extractBestSectionExcerpt(sections []PaperSection, names []string, limit int) string {
	for _, wanted := range names {
		for _, section := range sections {
			if section.Name == wanted {
				return trimRunes(section.Text, limit)
			}
		}
	}
	return ""
}

func extractReferencesTail(text string) string {
	lower := strings.ToLower(text)
	idx := strings.LastIndex(lower, "\nreferences")
	if idx < 0 {
		idx = strings.LastIndex(lower, "references")
	}
	if idx < 0 {
		return ""
	}
	return trimRunes(strings.TrimSpace(text[idx:]), 4000)
}

func extractReferenceEntries(refText string) []string {
	refText = strings.TrimSpace(refText)
	if refText == "" {
		return nil
	}

	lines := strings.Split(refText, "\n")
	var (
		entries []string
		current strings.Builder
	)
	startsNewEntry := func(line string) bool {
		line = strings.TrimSpace(line)
		if line == "" {
			return false
		}
		if regexp.MustCompile(`^\[?\d+\]?`).MatchString(line) {
			return true
		}
		if regexp.MustCompile(`^[A-Z][A-Za-z\-]+,\s+[A-Z]`).MatchString(line) {
			return true
		}
		return false
	}

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if startsNewEntry(line) && current.Len() > 0 {
			entries = append(entries, trimRunes(current.String(), 320))
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteByte(' ')
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		entries = append(entries, trimRunes(current.String(), 320))
	}

	if len(entries) == 0 {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			entries = append(entries, trimRunes(line, 320))
			if len(entries) >= 10 {
				break
			}
		}
	}
	if len(entries) > 12 {
		entries = entries[:12]
	}
	return uniqueTrimmedStrings(entries)
}

func extractKeywords(text string) []string {
	tokens := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	if len(tokens) == 0 {
		return nil
	}

	stopwords := map[string]bool{
		"the": true, "and": true, "with": true, "from": true, "that": true, "this": true,
		"into": true, "their": true, "there": true, "these": true, "those": true,
		"for": true, "are": true, "was": true, "were": true, "have": true, "has": true,
		"our": true, "using": true, "use": true, "via": true, "also": true, "than": true,
		"show": true, "shows": true, "paper": true, "method": true, "results": true,
	}

	counts := map[string]int{}
	for _, token := range tokens {
		if len(token) < 4 || stopwords[token] {
			continue
		}
		counts[token]++
	}

	type keywordCount struct {
		Token string
		Count int
	}
	var ordered []keywordCount
	for token, count := range counts {
		ordered = append(ordered, keywordCount{Token: token, Count: count})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Count == ordered[j].Count {
			return ordered[i].Token < ordered[j].Token
		}
		return ordered[i].Count > ordered[j].Count
	})

	var keywords []string
	for _, item := range ordered {
		keywords = append(keywords, item.Token)
		if len(keywords) >= 10 {
			break
		}
	}
	return keywords
}

func buildRetrievalQuery(paper *StructuredPaper) string {
	if paper == nil {
		return ""
	}
	parts := []string{strings.TrimSpace(paper.Title)}
	if paper.Abstract != "" {
		parts = append(parts, trimWords(paper.Abstract, 50))
	}
	if len(paper.Keywords) > 0 {
		parts = append(parts, strings.Join(paper.Keywords[:min(len(paper.Keywords), 8)], " "))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func fallbackSearchTerm(query string) string {
	keywords := extractKeywords(query)
	if len(keywords) == 0 {
		return strings.ToLower(trimRunes(query, 80))
	}
	if len(keywords) > 4 {
		keywords = keywords[:4]
	}
	return strings.Join(keywords, " ")
}

func inferPaperTitle(rawText, fallbackURL string) string {
	lines := strings.Split(rawText, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || utf8.RuneCountInString(line) > 180 {
			continue
		}
		lower := strings.ToLower(line)
		if lower == "abstract" || strings.HasPrefix(lower, "figure ") || strings.HasPrefix(lower, "table ") {
			continue
		}
		return line
	}
	if arxivID := extractArxivID(fallbackURL); arxivID != "" {
		return "arXiv " + arxivID
	}
	return ""
}

func extractArxivID(raw string) string {
	re := regexp.MustCompile(`(?i)(?:arxiv\.org/(?:abs|pdf)/)?([0-9]{4}\.[0-9]{4,5}(?:v[0-9]+)?)`)
	matches := re.FindStringSubmatch(strings.TrimSpace(raw))
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSuffix(matches[1], ".pdf")
}

func isLikelyPDFURL(rawURL, contentType string) bool {
	contentType = strings.ToLower(contentType)
	if strings.Contains(contentType, "application/pdf") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(rawURL)), ".pdf")
}

func buildMemoryDocumentContent(paper *StructuredPaper, rawText string) string {
	if paper == nil {
		return strings.TrimSpace(rawText)
	}
	var out strings.Builder
	if strings.TrimSpace(paper.Title) != "" {
		out.WriteString("# ")
		out.WriteString(strings.TrimSpace(paper.Title))
		out.WriteString("\n\n")
	}
	if paper.SourceURL != "" {
		out.WriteString("Source URL: ")
		out.WriteString(paper.SourceURL)
		out.WriteString("\n")
	}
	if len(paper.Keywords) > 0 {
		out.WriteString("Keywords: ")
		out.WriteString(strings.Join(paper.Keywords, ", "))
		out.WriteString("\n")
	}
	if out.Len() > 0 {
		out.WriteString("\n")
	}
	for _, section := range paper.Sections {
		if section.Text == "" {
			continue
		}
		out.WriteString("## ")
		if section.Heading != "" {
			out.WriteString(section.Heading)
		} else {
			out.WriteString(section.Name)
		}
		out.WriteString("\n")
		out.WriteString(section.Text)
		out.WriteString("\n\n")
	}
	if len(paper.Sections) == 0 && strings.TrimSpace(rawText) != "" {
		out.WriteString(strings.TrimSpace(rawText))
	}
	return strings.TrimSpace(out.String())
}

func preparedPaperFromRecord(record *paperRecord) (PreparedPaper, error) {
	if record == nil {
		return PreparedPaper{}, fmt.Errorf("paper record is nil")
	}
	structured, err := record.structured()
	if err != nil {
		return PreparedPaper{}, err
	}
	sections := make([]PaperSectionExcerpt, 0, len(structured.Sections))
	for _, section := range structured.Sections {
		if section.Text == "" {
			continue
		}
		sections = append(sections, PaperSectionExcerpt{
			Name:    section.Name,
			Heading: section.Heading,
			Excerpt: trimRunes(section.Text, sectionExcerptLimit(section.Name)),
		})
		if len(sections) >= 8 {
			break
		}
	}
	return PreparedPaper{
		PaperID:          record.ID,
		Title:            structured.Title,
		SourceKind:       structured.SourceKind,
		SourceURL:        structured.SourceURL,
		MemoryPath:       record.MemoryPath,
		ContentHash:      record.ContentHash,
		WordCount:        structured.WordCount,
		Keywords:         append([]string(nil), structured.Keywords...),
		Figures:          append([]string(nil), structured.Figures...),
		Tables:           append([]string(nil), structured.Tables...),
		ReferenceEntries: append([]string(nil), structured.ReferenceEntries...),
		Sections:         sections,
	}, nil
}

func relatedPaperFromRecord(record *paperRecord, snippet string, score float64) (RelatedPaper, error) {
	structured, err := record.structured()
	if err != nil {
		return RelatedPaper{}, err
	}
	return RelatedPaper{
		PaperID:    record.ID,
		Title:      structured.Title,
		SourceKind: structured.SourceKind,
		SourceURL:  structured.SourceURL,
		Abstract:   trimRunes(structured.Abstract, 700),
		Snippet:    trimRunes(strings.TrimSpace(snippet), 420),
		Score:      score,
	}, nil
}

func sectionExcerptLimit(name string) int {
	switch name {
	case "abstract":
		return 900
	case "method", "experiments":
		return 1200
	case "references":
		return 800
	default:
		return 900
	}
}

func trimRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "..."
}

func trimWords(value string, limit int) string {
	parts := strings.Fields(strings.TrimSpace(value))
	if limit <= 0 || len(parts) <= limit {
		return strings.Join(parts, " ")
	}
	return strings.Join(parts[:limit], " ") + "..."
}

func formatPreparedBundle(bundle *PreparedReviewBundle) string {
	if bundle == nil {
		return tools.WrapExternalContent("No review bundle was prepared.", "Research Reviewer", false)
	}

	var out strings.Builder
	out.WriteString("Prepared research review bundle\n\n")
	out.WriteString("Mode: ")
	out.WriteString(bundle.Mode)
	if bundle.Focus != "" {
		out.WriteString("\nFocus: ")
		out.WriteString(bundle.Focus)
	}
	if bundle.RetrievalQuery != "" {
		out.WriteString("\nRetrieval query: ")
		out.WriteString(bundle.RetrievalQuery)
	}
	out.WriteString("\n\nPaper\n")
	out.WriteString("-----\n")
	out.WriteString("paper_id: ")
	out.WriteString(bundle.Paper.PaperID)
	out.WriteString("\nTitle: ")
	out.WriteString(bundle.Paper.Title)
	if bundle.Paper.SourceURL != "" {
		out.WriteString("\nSource URL: ")
		out.WriteString(bundle.Paper.SourceURL)
	}
	out.WriteString("\nSource kind: ")
	out.WriteString(bundle.Paper.SourceKind)
	out.WriteString("\nWord count: ")
	out.WriteString(fmt.Sprintf("%d", bundle.Paper.WordCount))
	if len(bundle.Paper.Keywords) > 0 {
		out.WriteString("\nKeywords: ")
		out.WriteString(strings.Join(bundle.Paper.Keywords, ", "))
	}
	out.WriteString("\n")

	if len(bundle.Paper.Sections) > 0 {
		out.WriteString("\nSection excerpts\n")
		out.WriteString("---------------\n")
		for _, section := range bundle.Paper.Sections {
			out.WriteString(section.Heading)
			out.WriteString(":\n")
			out.WriteString(section.Excerpt)
			out.WriteString("\n\n")
		}
	}

	if len(bundle.Paper.Figures) > 0 {
		out.WriteString("Figure captions:\n")
		for i, caption := range bundle.Paper.Figures {
			out.WriteString(fmt.Sprintf("%d. %s\n", i+1, trimRunes(caption, 220)))
			if i >= 4 {
				break
			}
		}
		out.WriteString("\n")
	}
	if len(bundle.Paper.Tables) > 0 {
		out.WriteString("Table captions:\n")
		for i, caption := range bundle.Paper.Tables {
			out.WriteString(fmt.Sprintf("%d. %s\n", i+1, trimRunes(caption, 220)))
			if i >= 4 {
				break
			}
		}
		out.WriteString("\n")
	}
	if len(bundle.Paper.ReferenceEntries) > 0 {
		out.WriteString("Reference candidates:\n")
		for i, ref := range bundle.Paper.ReferenceEntries {
			out.WriteString(fmt.Sprintf("%d. %s\n", i+1, trimRunes(ref, 240)))
			if i >= 7 {
				break
			}
		}
		out.WriteString("\n")
	}

	out.WriteString("Local related work\n")
	out.WriteString("------------------\n")
	if len(bundle.Related) == 0 {
		out.WriteString("No indexed related papers matched the retrieval query.\n\n")
	} else {
		for i, item := range bundle.Related {
			out.WriteString(fmt.Sprintf("%d. %s", i+1, item.Title))
			if item.SourceURL != "" {
				out.WriteString(" - ")
				out.WriteString(item.SourceURL)
			}
			if item.Snippet != "" {
				out.WriteString("\n   Matched snippet: ")
				out.WriteString(item.Snippet)
			}
			if item.Abstract != "" {
				out.WriteString("\n   Abstract: ")
				out.WriteString(item.Abstract)
			}
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}

	out.WriteString("Use this bundle as evidence for the review. If you need more detail, call research_reviewer_get_indexed_paper or research_reviewer_search_related_papers.\n")
	return tools.WrapExternalContent(out.String(), "Research Reviewer Prepare", false)
}

func formatRelatedSearchResults(query string, related []RelatedPaper) string {
	var out strings.Builder
	out.WriteString("Local related-paper search\n\n")
	if query != "" {
		out.WriteString("Query: ")
		out.WriteString(query)
		out.WriteString("\n\n")
	}
	if len(related) == 0 {
		out.WriteString("No indexed papers matched.\n")
		return out.String()
	}
	for i, item := range related {
		out.WriteString(fmt.Sprintf("%d. %s", i+1, item.Title))
		if item.SourceURL != "" {
			out.WriteString(" - ")
			out.WriteString(item.SourceURL)
		}
		if item.Snippet != "" {
			out.WriteString("\n   ")
			out.WriteString(item.Snippet)
		}
		if item.Abstract != "" {
			out.WriteString("\n   Abstract: ")
			out.WriteString(item.Abstract)
		}
		out.WriteString("\n")
	}
	return tools.WrapExternalContent(out.String(), "Research Reviewer Related Work", false)
}

func formatIndexedPaperDetail(record *paperRecord, includeFullText bool, maxChars int) (string, error) {
	structured, err := record.structured()
	if err != nil {
		return "", err
	}

	if maxChars <= 0 {
		maxChars = 6000
	}
	var out strings.Builder
	out.WriteString("Indexed paper detail\n\n")
	out.WriteString("paper_id: ")
	out.WriteString(record.ID)
	out.WriteString("\nTitle: ")
	out.WriteString(structured.Title)
	if structured.SourceURL != "" {
		out.WriteString("\nSource URL: ")
		out.WriteString(structured.SourceURL)
	}
	out.WriteString("\nSource kind: ")
	out.WriteString(structured.SourceKind)
	out.WriteString("\nKeywords: ")
	out.WriteString(strings.Join(structured.Keywords, ", "))
	out.WriteString("\n\n")

	for _, section := range structured.Sections {
		if section.Text == "" {
			continue
		}
		out.WriteString(section.Heading)
		out.WriteString(":\n")
		out.WriteString(trimRunes(section.Text, maxChars/3))
		out.WriteString("\n\n")
	}

	if includeFullText && strings.TrimSpace(record.RawText) != "" {
		out.WriteString("Full text excerpt:\n")
		out.WriteString(trimRunes(record.RawText, maxChars))
		out.WriteString("\n")
	}
	return tools.WrapExternalContent(out.String(), "Research Reviewer Indexed Paper", false), nil
}

func buildReviewPrompt(bundle *PreparedReviewBundle) string {
	var out strings.Builder
	out.WriteString("You are reviewing a research paper in a NeurIPS/ICLR style.\n\n")
	out.WriteString("Constraints:\n")
	out.WriteString("- Ground every claim in the prepared paper bundle and indexed related work below.\n")
	out.WriteString("- If evidence is missing, say so explicitly instead of guessing.\n")
	out.WriteString("- You may call research_reviewer_get_indexed_paper or research_reviewer_search_related_papers if you need more evidence.\n")
	out.WriteString("- Do not waste space on generic compliments.\n\n")

	out.WriteString("Mode: ")
	out.WriteString(bundle.Mode)
	out.WriteString("\n")
	if bundle.Mode == reviewModeHarsh {
		out.WriteString("Tone: strict, skeptical, concise.\n")
	} else {
		out.WriteString("Tone: constructive, concrete, still critical when needed.\n")
	}
	if bundle.Focus != "" {
		out.WriteString("Requested focus: ")
		out.WriteString(bundle.Focus)
		out.WriteString("\n")
	}
	out.WriteString("\nPrepared bundle:\n")
	out.WriteString(formatPreparedBundle(bundle))
	out.WriteString("\n\nOutput format:\n")
	out.WriteString("## Paper Summary\n")
	out.WriteString("## Strengths\n")
	out.WriteString("## Weaknesses\n")
	out.WriteString("## Detailed Critique\n")
	out.WriteString("Cover: introduction, novelty, method, experiments, results, overclaiming, and limitations.\n")
	out.WriteString("## Missing Controls, Baselines, or Analyses\n")
	out.WriteString("## Writing and Citation Notes\n")
	out.WriteString("## Actionable Suggestions\n")
	out.WriteString("## Overall Verdict\n")
	out.WriteString("Include an explicit recommendation label such as reject, weak reject, borderline, weak accept, or accept.\n")
	return out.String()
}

func uniqueTrimmedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
