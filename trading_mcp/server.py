"""
MCP stdio server wrapping TradingAgents for GoClaw integration.

Exposes both a high-level full-pipeline tool and granular data/analysis tools.
GoClaw spawns this as a subprocess and discovers tools via MCP protocol.
"""

import asyncio
import csv
import io
import json
import logging
import os
import re
from datetime import datetime, timedelta
from typing import Any

from mcp.server import Server
from mcp.server.stdio import stdio_server
from mcp.types import TextContent, Tool

logger = logging.getLogger(__name__)

server = Server("trading-agents")

# Lazy-initialized graph instance (TradingAgentsGraph is heavy to import)
_graph = None
_graph_config = None
_dataflow_initialized = False


def _ensure_dataflow_config():
    """Initialize the dataflow config layer for granular tools (called once)."""
    global _dataflow_initialized
    if _dataflow_initialized:
        return
    from tradingagents.default_config import DEFAULT_CONFIG
    from tradingagents.dataflows.config import set_config
    set_config(DEFAULT_CONFIG)
    _dataflow_initialized = True


def _get_graph(config_override: dict | None = None):
    """Get or create a TradingAgentsGraph instance. Recreates if config changes."""
    global _graph, _graph_config, _dataflow_initialized

    from tradingagents.default_config import DEFAULT_CONFIG

    cfg = {**DEFAULT_CONFIG}
    if config_override:
        cfg.update(config_override)

    # Reuse existing graph if config hasn't changed
    if _graph is not None and cfg == _graph_config:
        return _graph

    from tradingagents import TradingAgentsGraph

    _graph = TradingAgentsGraph(config=cfg)
    _graph_config = cfg
    _dataflow_initialized = True  # TradingAgentsGraph.__init__ calls set_config
    return _graph


def _compute_start_date(end_date: str, lookback_days: int) -> str:
    """Compute start date by subtracting lookback_days from end_date."""
    dt = datetime.strptime(end_date, "%Y-%m-%d")
    start = dt - timedelta(days=int(lookback_days * 1.5))  # pad for weekends/holidays
    return start.strftime("%Y-%m-%d")


def _serialize(obj: Any) -> Any:
    """Make objects JSON-serializable."""
    if hasattr(obj, "to_dict"):
        return obj.to_dict()
    if hasattr(obj, "__dict__"):
        return {k: _serialize(v) for k, v in obj.__dict__.items() if not k.startswith("_")}
    if isinstance(obj, dict):
        return {k: _serialize(v) for k, v in obj.items()}
    if isinstance(obj, (list, tuple)):
        return [_serialize(v) for v in obj]
    try:
        json.dumps(obj)
        return obj
    except (TypeError, ValueError):
        return str(obj)


def _to_number(value: str):
    """Best-effort numeric coercion for structured fields."""
    if value is None:
        return None
    text = str(value).strip().replace(",", "")
    if not text:
        return None
    try:
        if re.fullmatch(r"-?\d+", text):
            return int(text)
        return float(text)
    except ValueError:
        return None


def _parse_price_rows(raw_text: str) -> list[dict[str, Any]]:
    """Parse the CSV-like stock dump into structured OHLCV rows."""
    lines = []
    for line in str(raw_text).splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        lines.append(stripped)
    if not lines:
        return []

    reader = csv.DictReader(io.StringIO("\n".join(lines)))
    rows = []
    for row in reader:
        if not row:
            continue
        rows.append({
            "date": row.get("Date"),
            "open": _to_number(row.get("Open")),
            "high": _to_number(row.get("High")),
            "low": _to_number(row.get("Low")),
            "close": _to_number(row.get("Close")),
            "volume": _to_number(row.get("Volume")),
            "dividends": _to_number(row.get("Dividends")),
            "stock_splits": _to_number(row.get("Stock Splits")),
        })
    return rows


def _extract_latest_indicator_values(indicators: dict[str, str]) -> dict[str, dict[str, Any]]:
    """Extract the newest numeric indicator point from each verbose indicator dump."""
    latest = {}
    for name, raw_text in indicators.items():
        latest_point = None
        for line in str(raw_text).splitlines():
            stripped = line.strip()
            match = re.match(r"^(\d{4}-\d{2}-\d{2}):\s*(.+)$", stripped)
            if not match:
                continue
            date_str, value_text = match.groups()
            if "N/A" in value_text.upper():
                continue
            number_match = re.search(r"-?\d+(?:\.\d+)?", value_text)
            if not number_match:
                continue
            latest_point = {
                "date": date_str,
                "value": float(number_match.group(0)),
            }
        if latest_point is not None:
            latest[name] = latest_point
    return latest


def _parse_named_metrics_block(raw_text: str) -> dict[str, Any]:
    """Parse 'Key: Value' blocks into a structured dict while preserving strings."""
    parsed = {}
    for line in str(raw_text).splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#") or ":" not in stripped:
            continue
        key, value = stripped.split(":", 1)
        key = key.strip()
        value = value.strip()
        numeric = _to_number(value)
        parsed[key] = numeric if numeric is not None else value
    return parsed


# ---------------------------------------------------------------------------
# Tool definitions
# ---------------------------------------------------------------------------

TOOLS = [
    Tool(
        name="analyze_stock",
        description=(
            "Run the full TradingAgents multi-agent pipeline on a stock. "
            "Analysts gather data, bull/bear researchers debate, a trader proposes action, "
            "risk agents assess, and a portfolio manager decides. "
            "Returns a rating (BUY/OVERWEIGHT/HOLD/UNDERWEIGHT/SELL) with detailed rationale. "
            "This can take 2-5 minutes due to multiple internal LLM calls."
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "ticker": {
                    "type": "string",
                    "description": "Stock ticker symbol, e.g. AAPL, NVDA, TSLA",
                },
                "date": {
                    "type": "string",
                    "description": "Analysis date in YYYY-MM-DD format. Must be a past trading day.",
                },
                "config": {
                    "type": "object",
                    "description": (
                        "Optional config overrides. Keys: llm_provider, deep_think_llm, "
                        "quick_think_llm, max_debate_rounds, max_risk_discuss_rounds, output_language"
                    ),
                },
            },
            "required": ["ticker", "date"],
        },
    ),
    Tool(
        name="get_market_data",
        description=(
            "Fetch stock price data (OHLCV) and technical indicators for a ticker. "
            "Returns recent price history and key indicators (SMA, EMA, MACD, RSI, Bollinger Bands, ATR, VWMA, MFI)."
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "ticker": {"type": "string", "description": "Stock ticker symbol"},
                "date": {"type": "string", "description": "Reference date (YYYY-MM-DD)"},
                "lookback_days": {
                    "type": "integer",
                    "description": "Number of trading days to look back (default 30)",
                    "default": 30,
                },
                "indicators": {
                    "type": "array",
                    "items": {"type": "string"},
                    "description": (
                        "Technical indicators to fetch. Options: close_50_sma, close_200_sma, "
                        "close_10_ema, macd, macds, macdh, rsi, boll, boll_ub, boll_lb, atr, vwma, mfi. "
                        "Default: all."
                    ),
                },
            },
            "required": ["ticker", "date"],
        },
    ),
    Tool(
        name="get_fundamentals",
        description=(
            "Fetch fundamental financial data for a stock: PE ratio, PEG, EPS, profit margins, "
            "ROE, ROA, market cap, revenue, and more."
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "ticker": {"type": "string", "description": "Stock ticker symbol"},
                "date": {"type": "string", "description": "Reference date (YYYY-MM-DD)"},
                "include_statements": {
                    "type": "boolean",
                    "description": "Also include balance sheet, income statement, and cash flow (default false)",
                    "default": False,
                },
            },
            "required": ["ticker", "date"],
        },
    ),
    Tool(
        name="get_news_sentiment",
        description="Fetch recent news articles and global macro news for a stock ticker.",
        inputSchema={
            "type": "object",
            "properties": {
                "ticker": {"type": "string", "description": "Stock ticker symbol"},
                "date": {"type": "string", "description": "Reference date (YYYY-MM-DD)"},
            },
            "required": ["ticker", "date"],
        },
    ),
    Tool(
        name="get_insider_transactions",
        description="Fetch recent insider transactions (buys/sells by officers, directors) for a stock.",
        inputSchema={
            "type": "object",
            "properties": {
                "ticker": {"type": "string", "description": "Stock ticker symbol"},
            },
            "required": ["ticker"],
        },
    ),
    Tool(
        name="reflect_on_trade",
        description=(
            "After a trade outcome is known, run the reflection/learning system. "
            "Each agent (bull, bear, trader, judge, portfolio manager) analyzes what went right/wrong "
            "and stores lessons in memory for future analyses. Must be called after analyze_stock."
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "returns": {
                    "type": "number",
                    "description": "Actual returns as a decimal (e.g., 0.05 for 5% gain, -0.03 for 3% loss)",
                },
            },
            "required": ["returns"],
        },
    ),
]


@server.list_tools()
async def list_tools():
    return TOOLS


@server.call_tool()
async def call_tool(name: str, arguments: dict):
    try:
        if name == "analyze_stock":
            return await _handle_analyze_stock(arguments)
        elif name == "get_market_data":
            return await _handle_get_market_data(arguments)
        elif name == "get_fundamentals":
            return await _handle_get_fundamentals(arguments)
        elif name == "get_news_sentiment":
            return await _handle_get_news_sentiment(arguments)
        elif name == "get_insider_transactions":
            return await _handle_get_insider_transactions(arguments)
        elif name == "reflect_on_trade":
            return await _handle_reflect_on_trade(arguments)
        else:
            return [TextContent(type="text", text=f"Unknown tool: {name}")]
    except Exception as e:
        logger.exception("Tool %s failed", name)
        return [TextContent(type="text", text=f"Error: {type(e).__name__}: {e}")]


# ---------------------------------------------------------------------------
# Tool handlers
# ---------------------------------------------------------------------------


async def _handle_analyze_stock(args: dict):
    """Run the full TradingAgents pipeline."""
    ticker = args["ticker"].upper()
    date = args["date"]
    config_override = args.get("config")

    graph = await asyncio.to_thread(_get_graph, config_override)
    final_state, signal = await asyncio.to_thread(graph.propagate, ticker, date)

    # Extract the key reports from state
    result = {
        "signal": signal,
        "ticker": ticker,
        "date": date,
        "final_decision": final_state.get("final_trade_decision", ""),
        "market_report": final_state.get("market_report", ""),
        "sentiment_report": final_state.get("sentiment_report", ""),
        "news_report": final_state.get("news_report", ""),
        "fundamentals_report": final_state.get("fundamentals_report", ""),
    }

    return [TextContent(type="text", text=json.dumps(_serialize(result), indent=2))]


async def _handle_get_market_data(args: dict):
    """Fetch price data + technical indicators."""
    _ensure_dataflow_config()
    from tradingagents.dataflows.interface import route_to_vendor

    ticker = args["ticker"].upper()
    date = args["date"]
    lookback = args.get("lookback_days", 30)

    all_indicators = [
        "close_50_sma", "close_200_sma", "close_10_ema",
        "macd", "macds", "macdh", "rsi",
        "boll", "boll_ub", "boll_lb", "atr", "vwma", "mfi",
    ]
    requested = args.get("indicators", all_indicators)

    # get_stock_data expects (symbol, start_date, end_date)
    start_date = _compute_start_date(date, lookback)
    price_data = await asyncio.to_thread(
        route_to_vendor, "get_stock_data", ticker, start_date, date
    )

    # get_indicators expects (symbol, indicator, curr_date, look_back_days)
    indicators = {}
    for ind in requested:
        if ind in all_indicators:
            try:
                val = await asyncio.to_thread(
                    route_to_vendor, "get_indicators", ticker, ind, date, lookback
                )
                indicators[ind] = str(val)
            except Exception as e:
                indicators[ind] = f"Error: {e}"

    result = {
        "ticker": ticker,
        "date": date,
        "price_data": str(price_data),
        "price_rows": _parse_price_rows(price_data),
        "indicators": indicators,
        "indicator_latest": _extract_latest_indicator_values(indicators),
    }
    return [TextContent(type="text", text=json.dumps(result, indent=2))]


async def _handle_get_fundamentals(args: dict):
    """Fetch fundamental data and optionally financial statements."""
    _ensure_dataflow_config()
    from tradingagents.dataflows.interface import route_to_vendor

    ticker = args["ticker"].upper()
    date = args["date"]
    include_statements = args.get("include_statements", False)

    # get_fundamentals expects (ticker, curr_date=None)
    fundamentals = await asyncio.to_thread(route_to_vendor, "get_fundamentals", ticker, date)

    result = {
        "ticker": ticker,
        "date": date,
        "fundamentals": str(fundamentals),
        "fundamentals_map": _parse_named_metrics_block(fundamentals),
    }

    if include_statements:
        # get_balance_sheet/cashflow/income_statement expect (ticker, freq, curr_date)
        balance = await asyncio.to_thread(route_to_vendor, "get_balance_sheet", ticker, "quarterly", date)
        income = await asyncio.to_thread(route_to_vendor, "get_income_statement", ticker, "quarterly", date)
        cashflow = await asyncio.to_thread(route_to_vendor, "get_cashflow", ticker, "quarterly", date)
        result["balance_sheet"] = str(balance)
        result["income_statement"] = str(income)
        result["cash_flow"] = str(cashflow)

    return [TextContent(type="text", text=json.dumps(result, indent=2))]


async def _handle_get_news_sentiment(args: dict):
    """Fetch news and global macro news."""
    _ensure_dataflow_config()
    from tradingagents.dataflows.interface import route_to_vendor

    ticker = args["ticker"].upper()
    date = args["date"]
    start_date = _compute_start_date(date, 7)

    news = await asyncio.to_thread(route_to_vendor, "get_news", ticker, start_date, date)
    global_news = await asyncio.to_thread(route_to_vendor, "get_global_news", date)

    result = {
        "ticker": ticker,
        "date": date,
        "ticker_news": str(news),
        "global_news": str(global_news),
    }
    return [TextContent(type="text", text=json.dumps(result, indent=2))]


async def _handle_get_insider_transactions(args: dict):
    """Fetch insider transactions."""
    _ensure_dataflow_config()
    from tradingagents.dataflows.interface import route_to_vendor

    ticker = args["ticker"].upper()

    transactions = await asyncio.to_thread(route_to_vendor, "get_insider_transactions", ticker)

    result = {
        "ticker": ticker,
        "insider_transactions": str(transactions),
    }
    return [TextContent(type="text", text=json.dumps(result, indent=2))]


async def _handle_reflect_on_trade(args: dict):
    """Run post-trade reflection to update agent memories."""
    if _graph is None:
        return [TextContent(
            type="text",
            text="Error: No analysis has been run yet. Call analyze_stock first.",
        )]

    returns = args["returns"]
    await asyncio.to_thread(_graph.reflect_and_remember, returns)

    return [TextContent(
        type="text",
        text=json.dumps({
            "status": "ok",
            "message": f"Reflection complete. Agents updated memories based on {returns:+.2%} return.",
        }),
    )]


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------


async def main():
    async with stdio_server() as (read, write):
        await server.run(read, write, server.create_initialization_options())


if __name__ == "__main__":
    asyncio.run(main())
