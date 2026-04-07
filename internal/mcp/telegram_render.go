package mcp

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	mcpWrappedStart = "<<<EXTERNAL_UNTRUSTED_CONTENT>>>"
	mcpWrappedEnd   = "<<<END_EXTERNAL_UNTRUSTED_CONTENT>>>"
)

type telegramTradingRender struct {
	Media       bus.MediaFile
	Note        string
	Deliverable string
}

type tradingTheme struct {
	Background color.RGBA
	Panel      color.RGBA
	Border     color.RGBA
	Text       color.RGBA
	Muted      color.RGBA
	Grid       color.RGBA
	Good       color.RGBA
	Bad        color.RGBA
	Warn       color.RGBA
}

type marketDataPayload struct {
	Ticker          string                    `json:"ticker"`
	Date            string                    `json:"date"`
	PriceData       string                    `json:"price_data"`
	PriceRows       []marketPriceRow          `json:"price_rows"`
	Indicators      map[string]string         `json:"indicators"`
	IndicatorLatest map[string]indicatorPoint `json:"indicator_latest"`
}

type marketPriceRow struct {
	Date        string  `json:"date"`
	Open        float64 `json:"open"`
	High        float64 `json:"high"`
	Low         float64 `json:"low"`
	Close       float64 `json:"close"`
	Volume      float64 `json:"volume"`
	Dividends   float64 `json:"dividends"`
	StockSplits float64 `json:"stock_splits"`
}

type indicatorPoint struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"`
}

type fundamentalsPayload struct {
	Ticker          string         `json:"ticker"`
	Date            string         `json:"date"`
	Fundamentals    string         `json:"fundamentals"`
	FundamentalsMap map[string]any `json:"fundamentals_map"`
}

type analysisPayload struct {
	Signal             string `json:"signal"`
	Ticker             string `json:"ticker"`
	Date               string `json:"date"`
	FinalDecision      string `json:"final_decision"`
	MarketReport       string `json:"market_report"`
	SentimentReport    string `json:"sentiment_report"`
	NewsReport         string `json:"news_report"`
	FundamentalsReport string `json:"fundamentals_report"`
}

type tradingFontSet struct {
	Title    font.Face
	Heading  font.Face
	Body     font.Face
	BodyBold font.Face
	Badge    font.Face
	Small    font.Face
}

var (
	tradingThemeDefault = tradingTheme{
		Background: color.RGBA{0xF6, 0xF3, 0xEC, 0xFF},
		Panel:      color.RGBA{0xFF, 0xFF, 0xFF, 0xFF},
		Border:     color.RGBA{0xE2, 0xDA, 0xD0, 0xFF},
		Text:       color.RGBA{0x1E, 0x1F, 0x24, 0xFF},
		Muted:      color.RGBA{0x6A, 0x6F, 0x78, 0xFF},
		Grid:       color.RGBA{0xE9, 0xE2, 0xD7, 0xFF},
		Good:       color.RGBA{0x10, 0x7A, 0x5A, 0xFF},
		Bad:        color.RGBA{0xC0, 0x38, 0x39, 0xFF},
		Warn:       color.RGBA{0xD0, 0x7A, 0x20, 0xFF},
	}
	tradingFontLoadOnce sync.Once
	tradingRegularFont  *sfnt.Font
	tradingBoldFont     *sfnt.Font
)

func maybeBuildTradingTelegramRender(ctx context.Context, serverName, toolName, wrapped string) (*telegramTradingRender, error) {
	if !strings.EqualFold(serverName, "trading") {
		return nil, nil
	}
	if !strings.EqualFold(tools.ToolChannelTypeFromCtx(ctx), "telegram") {
		return nil, nil
	}

	payload := strings.TrimSpace(extractWrappedMCPBody(wrapped))
	if payload == "" || strings.HasPrefix(payload, "Error:") {
		return nil, nil
	}

	switch toolName {
	case "get_market_data":
		render, err := buildMarketDataTelegramRender(ctx, payload)
		if render != nil || err == nil {
			return render, err
		}
	case "get_fundamentals":
		render, err := buildFundamentalsTelegramRender(ctx, payload)
		if render != nil || err == nil {
			return render, err
		}
	case "analyze_stock":
		render, err := buildAnalysisTelegramRender(ctx, payload)
		if render != nil || err == nil {
			return render, err
		}
	}

	return buildGenericTradingTelegramRender(ctx, toolName, payload)
}

func buildMarketDataTelegramRender(ctx context.Context, payload string) (*telegramTradingRender, error) {
	var parsed marketDataPayload
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return nil, err
	}

	rows := parsed.PriceRows
	if len(rows) == 0 {
		rows = parseLegacyPriceRows(parsed.PriceData)
	}
	if len(rows) == 0 {
		return buildGenericTradingTelegramRender(ctx, "market-data", payload)
	}

	latest := parsed.IndicatorLatest
	if len(latest) == 0 && len(parsed.Indicators) > 0 {
		latest = parseLegacyIndicatorLatest(parsed.Indicators)
	}

	data, err := renderMarketDataCard(parsed.Ticker, parsed.Date, rows, latest)
	if err != nil {
		return nil, err
	}

	path, err := persistTradingRenderPNG(ctx, parsed.Ticker, "market-data", data)
	if err != nil {
		return nil, err
	}

	return &telegramTradingRender{
		Media:       bus.MediaFile{Path: path, MimeType: "image/png"},
		Note:        "\n\n[System note: A Telegram-friendly market chart image is already attached. Reply with 1-3 short bullets or a short caption. Do not paste raw JSON, CSV, or wide tables.]",
		Deliverable: fmt.Sprintf("[Rendered market chart: %s]", strings.TrimSpace(parsed.Ticker)),
	}, nil
}

func buildFundamentalsTelegramRender(ctx context.Context, payload string) (*telegramTradingRender, error) {
	var parsed fundamentalsPayload
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return nil, err
	}

	metrics := normalizeFundamentalsMap(parsed.FundamentalsMap)
	if len(metrics) == 0 {
		metrics = parseLegacyMetricsMap(parsed.Fundamentals)
	}
	if len(metrics) == 0 {
		return buildGenericTradingTelegramRender(ctx, "fundamentals", payload)
	}

	data, err := renderFundamentalsCard(parsed.Ticker, parsed.Date, metrics)
	if err != nil {
		return nil, err
	}
	path, err := persistTradingRenderPNG(ctx, parsed.Ticker, "fundamentals", data)
	if err != nil {
		return nil, err
	}

	return &telegramTradingRender{
		Media:       bus.MediaFile{Path: path, MimeType: "image/png"},
		Note:        "\n\n[System note: A Telegram-friendly fundamentals card image is already attached. Reply with a concise valuation/quality/risk read instead of dumping the raw data block.]",
		Deliverable: fmt.Sprintf("[Rendered fundamentals card: %s]", strings.TrimSpace(parsed.Ticker)),
	}, nil
}

func buildAnalysisTelegramRender(ctx context.Context, payload string) (*telegramTradingRender, error) {
	var parsed analysisPayload
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return nil, err
	}
	if strings.TrimSpace(parsed.Signal) == "" && strings.TrimSpace(parsed.FinalDecision) == "" {
		return buildGenericTradingTelegramRender(ctx, "analysis", payload)
	}

	data, err := renderAnalysisCard(parsed)
	if err != nil {
		return nil, err
	}
	path, err := persistTradingRenderPNG(ctx, parsed.Ticker, "analysis", data)
	if err != nil {
		return nil, err
	}

	return &telegramTradingRender{
		Media:       bus.MediaFile{Path: path, MimeType: "image/png"},
		Note:        "\n\n[System note: A Telegram-friendly trading decision card is already attached. Reply with a short decision summary and the main risk, not the full raw report.]",
		Deliverable: fmt.Sprintf("[Rendered trading decision card: %s]", strings.TrimSpace(parsed.Ticker)),
	}, nil
}

func buildGenericTradingTelegramRender(ctx context.Context, toolName, payload string) (*telegramTradingRender, error) {
	fields := genericTradingFields(payload)
	if len(fields) == 0 {
		return nil, nil
	}
	data, ticker, err := renderGenericTradingCard(toolName, fields)
	if err != nil {
		return nil, err
	}
	path, err := persistTradingRenderPNG(ctx, ticker, toolName, data)
	if err != nil {
		return nil, err
	}
	return &telegramTradingRender{
		Media:       bus.MediaFile{Path: path, MimeType: "image/png"},
		Note:        "\n\n[System note: A Telegram-friendly summary card is already attached. Keep the text reply brief and interpretive.]",
		Deliverable: fmt.Sprintf("[Rendered trading summary card: %s]", strings.TrimSpace(ticker)),
	}, nil
}

func renderMarketDataCard(ticker, date string, rows []marketPriceRow, latest map[string]indicatorPoint) ([]byte, error) {
	const (
		cardW = 1200
		cardH = 860
	)
	img := image.NewRGBA(image.Rect(0, 0, cardW, cardH))
	theme := tradingThemeDefault
	fonts := newTradingFonts()
	fillRect(img, img.Bounds(), theme.Background)

	lastRow := rows[len(rows)-1]
	firstRow := rows[0]
	changePct := 0.0
	if firstRow.Close != 0 {
		changePct = (lastRow.Close - firstRow.Close) / firstRow.Close * 100
	}
	accent := theme.Good
	if changePct < 0 {
		accent = theme.Bad
	}

	fillRect(img, image.Rect(0, 0, cardW, 118), accent)
	drawText(img, fonts.Title, color.White, 56, 72, strings.TrimSpace(defaultTicker(ticker))+" Market Snapshot")
	subtitle := fmt.Sprintf("Recent price action through %s", friendlyTradingDate(lastNonEmpty(lastRow.Date, date)))
	drawText(img, fonts.Body, color.RGBA{0xF6, 0xF8, 0xFB, 0xFF}, 56, 102, subtitle)

	badgeText := formatSignedPercent(changePct)
	drawPill(
		img,
		fonts.Badge,
		badgeText,
		image.Rect(cardW-270, 24, cardW-40, 96),
		color.RGBA{0xFF, 0xFC, 0xF6, 0xFF},
		accent,
		color.RGBA{0xD7, 0xCF, 0xC2, 0xFF},
	)

	chartPanel := image.Rect(42, 146, 812, 744)
	sidePanel := image.Rect(840, 146, 1158, 744)
	drawPanel(img, chartPanel, theme)
	drawPanel(img, sidePanel, theme)

	drawText(img, fonts.Heading, theme.Text, chartPanel.Min.X+26, chartPanel.Min.Y+34, "Close Price")
	drawText(img, fonts.Small, theme.Muted, chartPanel.Min.X+26, chartPanel.Min.Y+58, fmt.Sprintf("%d trading days", len(rows)))

	priceRect := image.Rect(chartPanel.Min.X+24, chartPanel.Min.Y+86, chartPanel.Max.X-26, chartPanel.Min.Y+382)
	volumeRect := image.Rect(chartPanel.Min.X+24, chartPanel.Min.Y+410, chartPanel.Max.X-26, chartPanel.Max.Y-28)
	drawPriceChart(img, priceRect, rows, fonts.Small, theme, accent)
	drawVolumeBars(img, volumeRect, rows, fonts.Small, theme, accent)

	drawText(img, fonts.Heading, theme.Text, sidePanel.Min.X+22, sidePanel.Min.Y+34, "Signal Check")

	sideY := sidePanel.Min.Y + 72
	metrics := []struct {
		Label string
		Value string
	}{
		{"Last Close", formatPrice(lastRow.Close)},
		{"Range", fmt.Sprintf("%s - %s", formatPrice(lastRow.Low), formatPrice(lastRow.High))},
		{"Volume", formatCompactNumber(lastRow.Volume)},
		{"Period Move", formatSignedPercent(changePct)},
		{"RSI", formatMetricValue(latest, "rsi", 2)},
		{"MACD", formatMetricValue(latest, "macd", 2)},
		{"MACD Hist", formatMetricValue(latest, "macdh", 2)},
		{"50 SMA", formatMetricValue(latest, "close_50_sma", 2)},
		{"200 SMA", formatMetricValue(latest, "close_200_sma", 2)},
	}
	for _, metric := range metrics {
		sideY = drawMetricRow(img, fonts, theme, sidePanel.Min.X+22, sideY, sidePanel.Dx()-44, metric.Label, metric.Value)
	}

	footer := fmt.Sprintf("Source: trading MCP | last row %s", friendlyTradingDate(lastRow.Date))
	drawText(img, fonts.Small, theme.Muted, 56, cardH-24, footer)

	return encodePNG(img)
}

func renderFundamentalsCard(ticker, date string, metrics map[string]string) ([]byte, error) {
	const (
		cardW = 1200
		cardH = 900
	)
	img := image.NewRGBA(image.Rect(0, 0, cardW, cardH))
	theme := tradingThemeDefault
	fonts := newTradingFonts()
	fillRect(img, img.Bounds(), theme.Background)

	fillRect(img, image.Rect(0, 0, cardW, 118), theme.Text)
	drawText(img, fonts.Title, color.White, 56, 72, strings.TrimSpace(defaultTicker(ticker))+" Fundamentals")
	drawText(img, fonts.Body, color.RGBA{0xD9, 0xDD, 0xE4, 0xFF}, 56, 102, fmt.Sprintf("Reference date %s", friendlyTradingDate(date)))

	mainPanel := image.Rect(42, 146, cardW-42, cardH-42)
	drawPanel(img, mainPanel, theme)

	name := valueOr(metrics, "Name", defaultTicker(ticker))
	sector := valueOr(metrics, "Sector", "N/A")
	industry := valueOr(metrics, "Industry", "N/A")
	drawText(img, fonts.Heading, theme.Text, mainPanel.Min.X+28, mainPanel.Min.Y+40, name)
	drawText(img, fonts.Body, theme.Muted, mainPanel.Min.X+28, mainPanel.Min.Y+68, sector+" | "+industry)

	rangeTop := mainPanel.Min.Y + 104
	drawRangeBar(img, fonts, theme, image.Rect(mainPanel.Min.X+28, rangeTop, mainPanel.Max.X-28, rangeTop+52),
		valueOr(metrics, "52 Week Low", "N/A"), valueOr(metrics, "52 Week High", "N/A"), valueOr(metrics, "50 Day Average", "N/A"))

	preferred := []string{
		"Market Cap", "Revenue (TTM)", "Net Income", "Free Cash Flow",
		"PE Ratio (TTM)", "Forward PE", "EPS (TTM)", "Forward EPS",
		"Profit Margin", "Operating Margin", "Return on Equity", "Return on Assets",
		"Debt to Equity", "Current Ratio", "Dividend Yield", "Beta",
	}
	selected := selectMetricCards(metrics, preferred, 10)
	cardTop := mainPanel.Min.Y + 190
	cardLeft := mainPanel.Min.X + 28
	cardGapX := 24
	cardGapY := 22
	cardWCell := (mainPanel.Dx() - 56 - cardGapX) / 2
	cardHCell := 112
	for idx, metric := range selected {
		col := idx % 2
		row := idx / 2
		x := cardLeft + col*(cardWCell+cardGapX)
		y := cardTop + row*(cardHCell+cardGapY)
		rect := image.Rect(x, y, x+cardWCell, y+cardHCell)
		drawMetricCard(img, fonts, theme, rect, metric.Label, metric.Value)
	}

	return encodePNG(img)
}

func renderAnalysisCard(parsed analysisPayload) ([]byte, error) {
	const (
		cardW = 1200
		cardH = 920
	)
	img := image.NewRGBA(image.Rect(0, 0, cardW, cardH))
	theme := tradingThemeDefault
	fonts := newTradingFonts()
	fillRect(img, img.Bounds(), theme.Background)

	signal := strings.ToUpper(strings.TrimSpace(parsed.Signal))
	accent := signalColor(theme, signal)
	fillRect(img, image.Rect(0, 0, cardW, 118), accent)
	drawText(img, fonts.Title, color.White, 56, 72, strings.TrimSpace(defaultTicker(parsed.Ticker))+" Trading Call")
	drawText(img, fonts.Body, color.RGBA{0xF1, 0xF4, 0xF7, 0xFF}, 56, 102, fmt.Sprintf("Decision snapshot for %s", friendlyTradingDate(parsed.Date)))
	drawPill(
		img,
		fonts.Badge,
		emptyFallback(signal, "ANALYSIS"),
		image.Rect(cardW-292, 22, cardW-40, 96),
		color.RGBA{0xFF, 0xFC, 0xF6, 0xFF},
		accent,
		color.RGBA{0xD7, 0xCF, 0xC2, 0xFF},
	)

	left := image.Rect(42, 146, 578, cardH-42)
	rightTop := image.Rect(606, 146, cardW-42, 480)
	rightBottom := image.Rect(606, 508, cardW-42, cardH-42)
	drawPanel(img, left, theme)
	drawPanel(img, rightTop, theme)
	drawPanel(img, rightBottom, theme)

	drawSectionBlock(img, fonts, theme, left, "Decision", emptyFallback(parsed.FinalDecision, "No final decision text available."))
	drawSectionBlock(img, fonts, theme, rightTop, "Market and Sentiment", joinSections(parsed.MarketReport, parsed.SentimentReport))
	drawSectionBlock(img, fonts, theme, rightBottom, "News and Fundamentals", joinSections(parsed.NewsReport, parsed.FundamentalsReport))

	return encodePNG(img)
}

func renderGenericTradingCard(toolName string, fields []genericField) ([]byte, string, error) {
	const (
		cardW = 1200
		cardH = 860
	)
	img := image.NewRGBA(image.Rect(0, 0, cardW, cardH))
	theme := tradingThemeDefault
	fonts := newTradingFonts()
	fillRect(img, img.Bounds(), theme.Background)

	ticker := "trading"
	for _, field := range fields {
		if strings.EqualFold(field.Label, "ticker") && strings.TrimSpace(field.Value) != "" {
			ticker = strings.TrimSpace(field.Value)
			break
		}
	}

	fillRect(img, image.Rect(0, 0, cardW, 118), theme.Text)
	drawText(img, fonts.Title, color.White, 56, 72, prettyToolTitle(toolName))
	drawText(img, fonts.Body, color.RGBA{0xD9, 0xDD, 0xE4, 0xFF}, 56, 102, "Telegram-friendly summary card")

	panel := image.Rect(42, 146, cardW-42, cardH-42)
	drawPanel(img, panel, theme)

	y := panel.Min.Y + 44
	for idx, field := range fields {
		if idx >= 12 {
			break
		}
		y = drawMetricRow(img, fonts, theme, panel.Min.X+28, y, panel.Dx()-56, field.Label, field.Value)
	}

	data, err := encodePNG(img)
	if err != nil {
		return nil, "", err
	}
	return data, ticker, nil
}

func parseLegacyPriceRows(raw string) []marketPriceRow {
	lines := make([]string, 0, 8)
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lines = append(lines, trimmed)
	}
	if len(lines) < 2 {
		return nil
	}

	reader := csv.NewReader(strings.NewReader(strings.Join(lines, "\n")))
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 {
		return nil
	}

	header := make(map[string]int, len(records[0]))
	for i, name := range records[0] {
		header[strings.ToLower(strings.TrimSpace(name))] = i
	}

	var rows []marketPriceRow
	for _, record := range records[1:] {
		if len(record) == 0 {
			continue
		}
		row := marketPriceRow{
			Date:   cell(record, header, "date"),
			Open:   parseFloat(cell(record, header, "open")),
			High:   parseFloat(cell(record, header, "high")),
			Low:    parseFloat(cell(record, header, "low")),
			Close:  parseFloat(cell(record, header, "close")),
			Volume: parseFloat(cell(record, header, "volume")),
		}
		if row.Date == "" {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func parseLegacyIndicatorLatest(raw map[string]string) map[string]indicatorPoint {
	out := make(map[string]indicatorPoint, len(raw))
	for name, blob := range raw {
		lines := strings.Split(blob, "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if len(trimmed) < 12 || trimmed[4] != '-' || trimmed[7] != '-' || !strings.Contains(trimmed, ":") {
				continue
			}
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) != 2 {
				continue
			}
			if strings.Contains(strings.ToUpper(parts[1]), "N/A") {
				continue
			}
			value, ok := extractFirstFloat(parts[1])
			if !ok {
				continue
			}
			out[name] = indicatorPoint{Date: strings.TrimSpace(parts[0]), Value: value}
		}
	}
	return out
}

func parseLegacyMetricsMap(raw string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, ":") {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func normalizeFundamentalsMap(raw map[string]any) map[string]string {
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		switch v := value.(type) {
		case string:
			out[key] = strings.TrimSpace(v)
		case float64:
			out[key] = formatNumericMetric(key, v)
		case int:
			out[key] = strconv.Itoa(v)
		case int64:
			out[key] = strconv.FormatInt(v, 10)
		default:
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" && s != "<nil>" {
				out[key] = s
			}
		}
	}
	return out
}

type genericField struct {
	Label string
	Value string
}

func genericTradingFields(payload string) []genericField {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return nil
	}

	var fields []genericField
	keys := make([]string, 0, len(parsed))
	for key := range parsed {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		value := normalizeGenericValue(parsed[key])
		if value == "" {
			continue
		}
		fields = append(fields, genericField{
			Label: prettyLabel(key),
			Value: value,
		})
	}
	return fields
}

func normalizeGenericValue(value any) string {
	switch v := value.(type) {
	case string:
		return summarizeMultiline(v, 3, 88)
	case float64:
		return trimFloat(v, 2)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case []any:
		if len(v) == 0 {
			return ""
		}
		return fmt.Sprintf("%d items", len(v))
	case map[string]any:
		if len(v) == 0 {
			return ""
		}
		return fmt.Sprintf("%d fields", len(v))
	default:
		if value == nil {
			return ""
		}
		return summarizeMultiline(fmt.Sprint(value), 2, 88)
	}
}

func extractWrappedMCPBody(wrapped string) string {
	if !strings.Contains(wrapped, mcpWrappedStart) {
		return wrapped
	}
	sepIdx := strings.Index(wrapped, "\n---\n")
	if sepIdx < 0 {
		return wrapped
	}
	body := wrapped[sepIdx+len("\n---\n"):]
	if reminderIdx := strings.Index(body, "\n[REMINDER:"); reminderIdx >= 0 {
		body = body[:reminderIdx]
	}
	if endIdx := strings.Index(body, mcpWrappedEnd); endIdx >= 0 {
		body = body[:endIdx]
	}
	return strings.TrimSpace(body)
}

func persistTradingRenderPNG(ctx context.Context, ticker, kind string, data []byte) (string, error) {
	baseDir := filepath.Join(os.TempDir(), "goclaw-market-renders", time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return "", err
	}

	filename := fmt.Sprintf("%s_%s_%d.png", slugTradingLabel(kind), slugTradingLabel(defaultTicker(ticker)), time.Now().UnixNano())
	path := filepath.Join(baseDir, filename)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func drawPriceChart(img *image.RGBA, rect image.Rectangle, rows []marketPriceRow, small font.Face, theme tradingTheme, accent color.RGBA) {
	fillRect(img, rect, color.RGBA{0xFC, 0xFA, 0xF7, 0xFF})
	strokeRect(img, rect, theme.Border)

	if len(rows) == 0 {
		return
	}

	minPrice := rows[0].Low
	maxPrice := rows[0].High
	for _, row := range rows {
		if row.Low < minPrice {
			minPrice = row.Low
		}
		if row.High > maxPrice {
			maxPrice = row.High
		}
	}
	if minPrice == maxPrice {
		minPrice -= 1
		maxPrice += 1
	}
	padding := (maxPrice - minPrice) * 0.1
	minPrice -= padding
	maxPrice += padding

	plotLeft := rect.Min.X + 14
	plotRight := rect.Max.X - 14
	plotTop := rect.Min.Y + 16
	plotBottom := rect.Max.Y - 26
	plotW := float64(plotRight - plotLeft)
	plotH := float64(plotBottom - plotTop)

	for i := 0; i <= 4; i++ {
		y := plotTop + int(float64(i)*plotH/4.0)
		drawLine(img, plotLeft, y, plotRight, y, theme.Grid)
		value := maxPrice - (maxPrice-minPrice)*float64(i)/4.0
		drawText(img, small, theme.Muted, rect.Min.X+18, y-4, trimFloat(value, 2))
	}

	var prevX, prevY int
	for i, row := range rows {
		x := plotLeft
		if len(rows) > 1 {
			x = plotLeft + int(float64(i)*plotW/float64(len(rows)-1))
		}
		y := plotBottom - int((row.Close-minPrice)/(maxPrice-minPrice)*plotH)
		if i > 0 {
			drawThickLine(img, prevX, prevY, x, y, accent, 3)
		}
		fillCircle(img, x, y, 4, accent)
		prevX, prevY = x, y
	}

	dateIdxs := []int{0, len(rows) / 2, len(rows) - 1}
	seen := map[int]bool{}
	for _, idx := range dateIdxs {
		if idx < 0 || idx >= len(rows) || seen[idx] {
			continue
		}
		seen[idx] = true
		x := plotLeft
		if len(rows) > 1 {
			x = plotLeft + int(float64(idx)*plotW/float64(len(rows)-1))
		}
		drawText(img, small, theme.Muted, x-18, rect.Max.Y-8, shortDate(rows[idx].Date))
	}
}

func drawVolumeBars(img *image.RGBA, rect image.Rectangle, rows []marketPriceRow, small font.Face, theme tradingTheme, accent color.RGBA) {
	fillRect(img, rect, color.RGBA{0xFC, 0xFA, 0xF7, 0xFF})
	strokeRect(img, rect, theme.Border)
	drawText(img, small, theme.Muted, rect.Min.X+12, rect.Min.Y+20, "Volume")
	if len(rows) == 0 {
		return
	}
	maxVolume := rows[0].Volume
	for _, row := range rows {
		if row.Volume > maxVolume {
			maxVolume = row.Volume
		}
	}
	if maxVolume <= 0 {
		return
	}
	barAreaTop := rect.Min.Y + 30
	barAreaBottom := rect.Max.Y - 22
	barAreaH := float64(barAreaBottom - barAreaTop)
	barW := max(12, (rect.Dx()-26)/max(1, len(rows)*2))
	gap := max(8, barW/2)
	x := rect.Min.X + 18
	barColor := color.RGBA{accent.R, accent.G, accent.B, 180}
	for _, row := range rows {
		h := int((row.Volume / maxVolume) * barAreaH)
		if row.Volume > 0 {
			h = max(18, h)
		}
		barRect := image.Rect(x, barAreaBottom-h, x+barW, barAreaBottom)
		fillRect(img, barRect, barColor)
		x += barW + gap
	}
	drawLine(img, rect.Min.X+14, barAreaBottom, rect.Max.X-14, barAreaBottom, theme.Border)
	drawText(img, small, theme.Muted, rect.Max.X-126, rect.Min.Y+20, "max "+formatCompactNumber(maxVolume))
}

func drawSectionBlock(img *image.RGBA, fonts tradingFontSet, theme tradingTheme, rect image.Rectangle, title, body string) {
	drawPanel(img, rect, theme)
	drawText(img, fonts.Heading, theme.Text, rect.Min.X+22, rect.Min.Y+34, title)
	drawWrappedText(img, fonts.Body, theme.Text, rect.Min.X+22, rect.Min.Y+66, rect.Dx()-44, 7, summarizeMultiline(body, 12, 92))
}

func drawMetricRow(img *image.RGBA, fonts tradingFontSet, theme tradingTheme, x, y, width int, label, value string) int {
	drawText(img, fonts.Small, theme.Muted, x, y, label)
	drawWrappedText(img, fonts.BodyBold, theme.Text, x, y+22, width, 4, emptyFallback(value, "N/A"))
	return y + 56
}

func drawMetricCard(img *image.RGBA, fonts tradingFontSet, theme tradingTheme, rect image.Rectangle, label, value string) {
	fillRect(img, rect, color.RGBA{0xFB, 0xF8, 0xF3, 0xFF})
	strokeRect(img, rect, theme.Border)
	drawText(img, fonts.Small, theme.Muted, rect.Min.X+18, rect.Min.Y+26, label)
	drawWrappedText(img, fonts.BodyBold, theme.Text, rect.Min.X+18, rect.Min.Y+62, rect.Dx()-36, 4, emptyFallback(value, "N/A"))
}

func drawRangeBar(img *image.RGBA, fonts tradingFontSet, theme tradingTheme, rect image.Rectangle, low, high, marker string) {
	drawText(img, fonts.Small, theme.Muted, rect.Min.X, rect.Min.Y+10, "52-week band")
	barRect := image.Rect(rect.Min.X, rect.Min.Y+20, rect.Max.X, rect.Min.Y+36)
	fillRect(img, barRect, color.RGBA{0xE9, 0xE2, 0xD7, 0xFF})
	strokeRect(img, barRect, theme.Border)
	drawText(img, fonts.Small, theme.Muted, rect.Min.X, rect.Max.Y, low)
	drawText(img, fonts.Small, theme.Muted, rect.Max.X-80, rect.Max.Y, high)
	if markerValue, ok := extractFirstFloat(marker); ok {
		if lowValue, okLow := extractFirstFloat(low); okLow {
			if highValue, okHigh := extractFirstFloat(high); okHigh && highValue > lowValue {
				ratio := (markerValue - lowValue) / (highValue - lowValue)
				ratio = math.Max(0, math.Min(1, ratio))
				x := barRect.Min.X + int(ratio*float64(barRect.Dx()))
				drawThickLine(img, x, barRect.Min.Y-4, x, barRect.Max.Y+4, theme.Text, 3)
				drawText(img, fonts.Small, theme.Text, max(barRect.Min.X, x-34), barRect.Min.Y-8, marker)
			}
		}
	}
}

func drawPanel(img *image.RGBA, rect image.Rectangle, theme tradingTheme) {
	fillRect(img, rect, theme.Panel)
	strokeRect(img, rect, theme.Border)
}

func drawPill(img *image.RGBA, face font.Face, text string, rect image.Rectangle, fill color.Color, fg color.Color, border color.Color) {
	fillRect(img, rect, fill)
	strokeRect(img, rect, border)
	textW := measureText(face, text)
	x := rect.Min.X + (rect.Dx()-textW)/2
	y := rect.Min.Y + rect.Dy()/2 + face.Metrics().Ascent.Ceil()/2 - 3
	drawText(img, face, fg, x, y, text)
}

func fillRect(img *image.RGBA, rect image.Rectangle, c color.Color) {
	draw.Draw(img, rect, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

func strokeRect(img *image.RGBA, rect image.Rectangle, c color.Color) {
	for x := rect.Min.X; x < rect.Max.X; x++ {
		img.Set(x, rect.Min.Y, c)
		img.Set(x, rect.Max.Y-1, c)
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		img.Set(rect.Min.X, y, c)
		img.Set(rect.Max.X-1, y, c)
	}
}

func drawText(img *image.RGBA, face font.Face, c color.Color, x, y int, text string) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(text)
}

func drawWrappedText(img *image.RGBA, face font.Face, c color.Color, x, y, width, gap int, text string) int {
	lines := wrapText(face, text, width)
	lineHeight := face.Metrics().Height.Ceil() + gap
	curY := y
	for _, line := range lines {
		drawText(img, face, c, x, curY, line)
		curY += lineHeight
	}
	return curY
}

func wrapText(face font.Face, text string, maxWidth int) []string {
	var out []string
	for _, para := range strings.Split(strings.TrimSpace(text), "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		words := strings.Fields(para)
		if len(words) == 0 {
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			candidate := line + " " + word
			if measureText(face, candidate) <= maxWidth {
				line = candidate
				continue
			}
			out = append(out, line)
			line = word
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.Color) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx := -1
	if x0 < x1 {
		sx = 1
	}
	sy := -1
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		img.Set(x0, y0, c)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func drawThickLine(img *image.RGBA, x0, y0, x1, y1 int, c color.Color, thickness int) {
	for offset := -thickness / 2; offset <= thickness/2; offset++ {
		drawLine(img, x0, y0+offset, x1, y1+offset, c)
	}
}

func fillCircle(img *image.RGBA, cx, cy, radius int, c color.Color) {
	for x := -radius; x <= radius; x++ {
		for y := -radius; y <= radius; y++ {
			if x*x+y*y <= radius*radius {
				img.Set(cx+x, cy+y, c)
			}
		}
	}
}

func newTradingFonts() tradingFontSet {
	return tradingFontSet{
		Title:    newTradingFace(30, true),
		Heading:  newTradingFace(20, true),
		Body:     newTradingFace(16, false),
		BodyBold: newTradingFace(17, true),
		Badge:    newTradingFace(22, true),
		Small:    newTradingFace(13, false),
	}
}

func newTradingFace(size float64, bold bool) font.Face {
	tradingFontLoadOnce.Do(func() {
		tradingRegularFont, _ = opentype.Parse(goregular.TTF)
		tradingBoldFont, _ = opentype.Parse(gobold.TTF)
	})

	var parsed *sfnt.Font
	if bold {
		parsed = tradingBoldFont
	} else {
		parsed = tradingRegularFont
	}
	if parsed == nil {
		return basicfont.Face7x13
	}

	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return basicfont.Face7x13
	}
	return face
}

func measureText(face font.Face, text string) int {
	return font.MeasureString(face, text).Ceil()
}

func formatMetricValue(values map[string]indicatorPoint, key string, decimals int) string {
	point, ok := values[key]
	if !ok {
		return "N/A"
	}
	return trimFloat(point.Value, decimals)
}

func formatNumericMetric(key string, value float64) string {
	switch {
	case strings.Contains(strings.ToLower(key), "cap"),
		strings.Contains(strings.ToLower(key), "revenue"),
		strings.Contains(strings.ToLower(key), "profit"),
		strings.Contains(strings.ToLower(key), "income"),
		strings.Contains(strings.ToLower(key), "cash"):
		return formatCompactNumber(value)
	default:
		return trimFloat(value, 2)
	}
}

func formatPrice(value float64) string {
	if value == 0 {
		return "0.00"
	}
	return trimFloat(value, 2)
}

func formatCompactNumber(value float64) string {
	if value == 0 {
		return "0"
	}
	absValue := math.Abs(value)
	switch {
	case absValue >= 1_000_000_000_000:
		return trimFloat(value/1_000_000_000_000, 2) + "T"
	case absValue >= 1_000_000_000:
		return trimFloat(value/1_000_000_000, 2) + "B"
	case absValue >= 1_000_000:
		return trimFloat(value/1_000_000, 2) + "M"
	case absValue >= 1_000:
		return trimFloat(value/1_000, 1) + "K"
	default:
		return trimFloat(value, 0)
	}
}

func trimFloat(value float64, decimals int) string {
	format := "%." + strconv.Itoa(max(0, decimals)) + "f"
	s := fmt.Sprintf(format, value)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "-0" {
		return "0"
	}
	return s
}

func formatSignedPercent(value float64) string {
	sign := "+"
	if value < 0 {
		sign = ""
	}
	return sign + trimFloat(value, 2) + "%"
}

func friendlyTradingDate(raw string) string {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return t.Format("Jan 2, 2006")
}

func shortDate(raw string) string {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return t.Format("Jan 2")
}

func prettyLabel(raw string) string {
	raw = strings.ReplaceAll(raw, "_", " ")
	parts := strings.Fields(strings.TrimSpace(raw))
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func prettyToolTitle(raw string) string {
	switch raw {
	case "get_news_sentiment":
		return "News Snapshot"
	case "get_insider_transactions":
		return "Insider Activity"
	default:
		return prettyLabel(raw)
	}
}

func summarizeMultiline(text string, maxLines, maxCols int) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(trimmed) > maxCols {
			trimmed = trimmed[:maxCols-1] + "..."
		}
		lines = append(lines, trimmed)
		if len(lines) >= maxLines {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func signalColor(theme tradingTheme, signal string) color.RGBA {
	switch strings.ToUpper(strings.TrimSpace(signal)) {
	case "BUY", "OVERWEIGHT":
		return theme.Good
	case "SELL", "UNDERWEIGHT":
		return theme.Bad
	case "HOLD":
		return theme.Warn
	default:
		return theme.Text
	}
}

func valueOr(values map[string]string, key, fallback string) string {
	if v, ok := values[key]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func selectMetricCards(metrics map[string]string, preferred []string, limit int) []genericField {
	var selected []genericField
	seen := map[string]bool{}
	for _, key := range preferred {
		value, ok := metrics[key]
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		selected = append(selected, genericField{Label: key, Value: value})
		seen[key] = true
		if len(selected) >= limit {
			return selected
		}
	}
	keys := make([]string, 0, len(metrics))
	for key := range metrics {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		selected = append(selected, genericField{Label: key, Value: metrics[key]})
		if len(selected) >= limit {
			break
		}
	}
	return selected
}

func joinSections(parts ...string) string {
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, summarizeMultiline(part, 6, 92))
	}
	if len(out) == 0 {
		return "No detailed section available."
	}
	return strings.Join(out, "\n\n")
}

func defaultTicker(ticker string) string {
	ticker = strings.TrimSpace(ticker)
	if ticker == "" {
		return "Trading"
	}
	return strings.ToUpper(ticker)
}

func slugTradingLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "trading"
	}
	return slug
}

func lastNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func emptyFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func cell(record []string, header map[string]int, key string) string {
	idx, ok := header[key]
	if !ok || idx < 0 || idx >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[idx])
}

func parseFloat(raw string) float64 {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, ",", ""))
	if raw == "" {
		return 0
	}
	value, _ := strconv.ParseFloat(raw, 64)
	return value
}

func extractFirstFloat(raw string) (float64, bool) {
	var buf strings.Builder
	started := false
	for _, r := range raw {
		if (r >= '0' && r <= '9') || r == '-' || r == '.' {
			buf.WriteRune(r)
			started = true
			continue
		}
		if started {
			break
		}
	}
	if buf.Len() == 0 {
		return 0, false
	}
	value, err := strconv.ParseFloat(buf.String(), 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
