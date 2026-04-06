package mcp

import (
	"context"
	"encoding/json"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func TestMaybeBuildTradingTelegramRender_MarketData(t *testing.T) {
	ctx := tools.WithToolChannelType(context.Background(), "telegram")
	payload := marketDataPayload{
		Ticker: "AAPL",
		Date:   "2026-04-03",
		PriceRows: []marketPriceRow{
			{Date: "2026-03-31", Open: 247.91, High: 255.48, Low: 247.10, Close: 253.79, Volume: 49598100},
			{Date: "2026-04-01", Open: 254.08, High: 256.18, Low: 253.33, Close: 255.63, Volume: 40059400},
			{Date: "2026-04-02", Open: 254.20, High: 256.13, Low: 250.65, Close: 255.92, Volume: 31289400},
		},
		IndicatorLatest: map[string]indicatorPoint{
			"rsi":           {Date: "2026-04-02", Value: 50.37},
			"macd":          {Date: "2026-04-02", Value: -2.39},
			"macdh":         {Date: "2026-04-02", Value: 0.79},
			"close_50_sma":  {Date: "2026-04-02", Value: 260.30},
			"close_200_sma": {Date: "2026-04-02", Value: 248.80},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	render, err := maybeBuildTradingTelegramRender(ctx, "trading", "get_market_data", wrapMCPContent(string(raw), "trading", "get_market_data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if render == nil {
		t.Fatal("expected render")
	}
	if render.Media.MimeType != "image/png" {
		t.Fatalf("expected image/png, got %q", render.Media.MimeType)
	}
	if !strings.Contains(render.Note, "market chart image") {
		t.Fatalf("unexpected note: %q", render.Note)
	}
	assertPNGExists(t, render.Media.Path)
}

func TestMaybeBuildTradingTelegramRender_Fundamentals(t *testing.T) {
	ctx := tools.WithToolChannelType(context.Background(), "telegram")
	payload := fundamentalsPayload{
		Ticker: "AAPL",
		Date:   "2026-04-03",
		FundamentalsMap: map[string]any{
			"Name":             "Apple Inc.",
			"Sector":           "Technology",
			"Industry":         "Consumer Electronics",
			"Market Cap":       3806395367424.0,
			"PE Ratio (TTM)":   32.74,
			"Revenue (TTM)":    435617005568.0,
			"Net Income":       117776998400.0,
			"Free Cash Flow":   106312753152.0,
			"52 Week High":     288.62,
			"52 Week Low":      169.21,
			"50 Day Average":   260.36,
			"Return on Equity": 1.52,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	render, err := maybeBuildTradingTelegramRender(ctx, "trading", "get_fundamentals", wrapMCPContent(string(raw), "trading", "get_fundamentals"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if render == nil {
		t.Fatal("expected render")
	}
	assertPNGExists(t, render.Media.Path)
}

func TestMaybeBuildTradingTelegramRender_NonTelegram(t *testing.T) {
	render, err := maybeBuildTradingTelegramRender(context.Background(), "trading", "get_market_data", wrapMCPContent(`{"ticker":"AAPL"}`, "trading", "get_market_data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if render != nil {
		t.Fatal("expected no render outside telegram")
	}
}

func assertPNGExists(t *testing.T, path string) {
	t.Helper()
	defer os.Remove(path)

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open png: %v", err)
	}
	defer f.Close()

	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	if cfg.Width < 600 || cfg.Height < 400 {
		t.Fatalf("unexpected png dimensions: %dx%d", cfg.Width, cfg.Height)
	}
}
