package pdfurlautoreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/net/html"

	"github.com/nextlevelbuilder/goclaw/internal/config"
)

const (
	fetchUserAgent = "GoClaw PDF URL Auto Review/1.0"
)

var (
	urlRE       = regexp.MustCompile(`https?://[^\s<>"']+`)
	modeLineRE  = regexp.MustCompile(`(?im)\bmode\s*[:=]\s*(harsh|collaborative|strict|constructive|mentor)\b`)
	focusLineRE = regexp.MustCompile(`(?im)^\s*focus\s*[:=]\s*(.+?)\s*$`)
)

type fetchedDocument struct {
	Kind        string
	Title       string
	FinalURL    string
	ContentType string
	Data        []byte
	Text        string
}

func (f *PDFURLAutoReviewFeature) fetchDocument(ctx context.Context, rawURL string) (fetchedDocument, error) {
	sourceURL, err := normalizeHTTPURL(rawURL)
	if err != nil {
		return fetchedDocument{}, err
	}

	if arxivID := extractArxivID(sourceURL); arxivID != "" {
		pdfURL := "https://arxiv.org/pdf/" + strings.TrimSuffix(arxivID, ".pdf") + ".pdf"
		body, finalURL, contentType, err := f.downloadURL(ctx, pdfURL)
		if err != nil {
			return fetchedDocument{}, err
		}
		if !looksLikePDF(finalURL, contentType, body) {
			return fetchedDocument{}, fmt.Errorf("arXiv PDF URL did not return a PDF: %s", finalURL)
		}
		return fetchedDocument{
			Kind:        sourceKindArxivPDF,
			FinalURL:    finalURL,
			ContentType: contentType,
			Data:        body,
		}, nil
	}

	body, finalURL, contentType, err := f.downloadURL(ctx, sourceURL)
	if err != nil {
		return fetchedDocument{}, err
	}
	if looksLikePDF(finalURL, contentType, body) {
		return fetchedDocument{
			Kind:        sourceKindPDF,
			FinalURL:    finalURL,
			ContentType: contentType,
			Data:        body,
		}, nil
	}

	if isHTMLContent(contentType, body) {
		if pdfURL := extractPDFURLFromHTML(body, finalURL); pdfURL != "" {
			pdfBody, pdfFinalURL, pdfContentType, pdfErr := f.downloadURL(ctx, pdfURL)
			if pdfErr != nil {
				return fetchedDocument{}, pdfErr
			}
			if !looksLikePDF(pdfFinalURL, pdfContentType, pdfBody) {
				return fetchedDocument{}, fmt.Errorf("linked PDF URL did not return a PDF: %s", pdfFinalURL)
			}
			return fetchedDocument{
				Kind:        sourceKindPDF,
				FinalURL:    pdfFinalURL,
				ContentType: pdfContentType,
				Data:        pdfBody,
			}, nil
		}

		title, text := extractHTMLText(body)
		if strings.TrimSpace(text) == "" {
			return fetchedDocument{}, fmt.Errorf("no readable text extracted from %s", sourceURL)
		}
		return fetchedDocument{
			Kind:        sourceKindHTMLText,
			Title:       title,
			FinalURL:    finalURL,
			ContentType: contentType,
			Data:        []byte(text),
			Text:        text,
		}, nil
	}

	if isTextContent(contentType, body) {
		text := normalizeDocumentText(string(body))
		if text == "" {
			return fetchedDocument{}, fmt.Errorf("no readable text extracted from %s", sourceURL)
		}
		return fetchedDocument{
			Kind:        sourceKindPlainText,
			FinalURL:    finalURL,
			ContentType: contentType,
			Data:        []byte(text),
			Text:        text,
		}, nil
	}

	return fetchedDocument{}, fmt.Errorf("unsupported content type %q from %s", contentType, finalURL)
}

func (f *PDFURLAutoReviewFeature) downloadURL(ctx context.Context, rawURL string) ([]byte, string, string, error) {
	sourceURL, err := normalizeHTTPURL(rawURL)
	if err != nil {
		return nil, "", "", err
	}

	timeout := defaultFetchTimeout
	if f != nil {
		timeout = f.fetchTimeout(ctx)
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	req.Header.Set("Accept", "application/pdf,text/html,application/xhtml+xml,text/plain;q=0.8,*/*;q=0.5")

	client := http.DefaultClient
	if f != nil && f.httpClient != nil {
		client = f.httpClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()

	finalURL := sourceURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		snippet := strings.TrimSpace(string(body))
		if snippet != "" {
			return nil, finalURL, contentType, fmt.Errorf("fetch %s: unexpected status %d: %s", sourceURL, resp.StatusCode, snippet)
		}
		return nil, finalURL, contentType, fmt.Errorf("fetch %s: unexpected status %d", sourceURL, resp.StatusCode)
	}

	maxBytes := int64(defaultMaxFetchBytes)
	if f != nil {
		maxBytes = f.maxFetchBytes(ctx)
	}
	if resp.ContentLength > maxBytes && resp.ContentLength > 0 {
		return nil, finalURL, contentType, fmt.Errorf("fetch %s: response exceeded %d bytes", sourceURL, maxBytes)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, finalURL, contentType, err
	}
	if int64(len(body)) > maxBytes {
		return nil, finalURL, contentType, fmt.Errorf("fetch %s: response exceeded %d bytes", sourceURL, maxBytes)
	}
	return body, finalURL, contentType, nil
}

func normalizeHTTPURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := neturl.Parse(raw)
	if err != nil || parsed == nil || parsed.Host == "" {
		return "", fmt.Errorf("invalid URL: %s", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported URL scheme: %s", parsed.Scheme)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("URL credentials are not supported")
	}
	parsed.Fragment = ""
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String(), nil
}

func extractFirstURL(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	raw := urlRE.FindString(text)
	raw = strings.TrimRight(raw, ".,;:!?)]}")
	return raw
}

func parseURLReviewText(text string) FetchReviewRequest {
	mode, focus := parseTextOverrides(text)
	return FetchReviewRequest{
		URL:   extractFirstURL(text),
		Mode:  mode,
		Focus: focus,
	}
}

func parseTextOverrides(text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	mode := ""
	if matches := modeLineRE.FindStringSubmatch(text); len(matches) >= 2 {
		mode = strings.TrimSpace(matches[1])
	}
	focus := ""
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimLeft(rawLine, "-*"))
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "focus="):
			focus = strings.TrimSpace(line[len("focus="):])
		case strings.HasPrefix(lower, "focus:"):
			focus = strings.TrimSpace(line[len("focus:"):])
		}
		if focus != "" {
			break
		}
	}
	if focus == "" {
		if matches := focusLineRE.FindStringSubmatch(text); len(matches) >= 2 {
			focus = strings.TrimSpace(matches[1])
		}
	}
	return mode, focus
}

func extractArxivID(raw string) string {
	parsed, err := neturl.Parse(raw)
	if err != nil || parsed == nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if host != "arxiv.org" {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	switch strings.ToLower(parts[0]) {
	case "abs", "pdf", "html":
	default:
		return ""
	}
	idParts := parts[1:]
	for i := range idParts {
		if unescaped, err := neturl.PathUnescape(idParts[i]); err == nil {
			idParts[i] = unescaped
		}
	}
	id := strings.TrimSpace(strings.Join(idParts, "/"))
	id = strings.TrimSuffix(id, ".pdf")
	return strings.Trim(id, "/")
}

func looksLikePDF(finalURL, contentType string, body []byte) bool {
	_ = finalURL
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.EqualFold(mediaType, "application/pdf") {
		return true
	}
	if len(body) >= 5 && bytes.Equal(body[:5], []byte("%PDF-")) {
		return true
	}
	return false
}

func isHTMLContent(contentType string, body []byte) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return true
	}
	sniff := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 512)])))
	return strings.Contains(sniff, "<html") || strings.Contains(sniff, "<!doctype html")
}

func isTextContent(contentType string, body []byte) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	detected := http.DetectContentType(body[:min(len(body), 512)])
	return strings.HasPrefix(detected, "text/plain")
}

func extractPDFURLFromHTML(raw []byte, baseURL string) string {
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	base, _ := neturl.Parse(baseURL)
	var candidates []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			switch tag {
			case "meta":
				name := strings.ToLower(attrValue(n, "name"))
				property := strings.ToLower(attrValue(n, "property"))
				if name == "citation_pdf_url" || name == "dc.identifier" || property == "citation_pdf_url" {
					candidates = append(candidates, attrValue(n, "content"))
				}
			case "a", "link":
				href := attrValue(n, "href")
				rel := strings.ToLower(attrValue(n, "rel"))
				text := strings.ToLower(normalizeInlineText(textFromNode(n)))
				if strings.Contains(strings.ToLower(href), ".pdf") ||
					strings.Contains(rel, "alternate") && strings.Contains(strings.ToLower(attrValue(n, "type")), "pdf") ||
					text == "pdf" || strings.Contains(text, "download pdf") {
					candidates = append(candidates, href)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		parsed, err := neturl.Parse(candidate)
		if err != nil {
			continue
		}
		if base != nil {
			parsed = base.ResolveReference(parsed)
		}
		if parsed.Scheme == "http" || parsed.Scheme == "https" {
			return parsed.String()
		}
	}
	return ""
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

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeFileOnce(path string, data []byte) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path is required")
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp-" + uuid.NewString()
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func storageDirWritable(dir string) bool {
	dir = strings.TrimSpace(config.ExpandHome(dir))
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

func fileNameFromURL(rawURL, fallback string) string {
	parsed, err := neturl.Parse(rawURL)
	if err != nil || parsed == nil {
		return fallback
	}
	base := path.Base(parsed.Path)
	base = strings.TrimSpace(base)
	if base == "." || base == "/" || base == "" {
		return fallback
	}
	if !strings.EqualFold(path.Ext(base), ".pdf") {
		base += ".pdf"
	}
	return base
}

func normalizeReviewMode(value, fallback string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(fallback)
	}
	switch strings.ToLower(value) {
	case "", reviewModeCollaborative, "constructive", "mentor":
		return reviewModeCollaborative, nil
	case reviewModeHarsh, "strict", "harsh reviewer":
		return reviewModeHarsh, nil
	default:
		return "", fmt.Errorf("mode must be collaborative or harsh")
	}
}

func focusCacheKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func cleanUserFacingError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 280 {
		text = text[:280] + "..."
	}
	return text
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

func isInputError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "is required") ||
		strings.Contains(text, "invalid url") ||
		strings.Contains(text, "unsupported url") ||
		strings.Contains(text, "mode must")
}

func stringListContains(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func toolPolicyExplicitlyAllows(spec *config.ToolPolicySpec, toolName string) bool {
	if spec == nil {
		return false
	}
	return stringListContains(spec.Allow, toolName) || stringListContains(spec.AlsoAllow, toolName)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
