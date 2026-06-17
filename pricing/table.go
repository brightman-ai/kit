package pricing

// priceTable is the embedded snapshot of model prices, in USD (or CNY) per MILLION
// tokens, validated against LiteLLM's model_prices_and_context_window.json (the same
// source ccusage uses). Keys are canonical lowercase model ids; the slice carries
// both specific ids and generic family fallbacks.
//
// Source order here is irrelevant: Lookup matches longest-key-first (see
// buildSortedTable), so a specific id (e.g. "claude-opus-4-8") always wins over a
// generic fallback (e.g. "opus"). To update prices, regenerate from the upstream
// LiteLLM JSON snapshot — see doc.go for the refresh mechanism.
//
// Tier columns (per-MTok): InputPerM, OutputPerM, CacheReadPerM, CacheWrite5mPerM,
// CacheWrite1hPerM.
//   - Anthropic models set all five (5m = 1.25× input, 1h = 2× input). No context
//     tier (ContextThreshold 0, Above nil).
//   - OpenAI/codex and Gemini set CacheWrite5m == CacheWrite1h == 0 (no cache-write
//     tier). gpt-5.4/5.5 and gemini 2.5-pro / 3-pro carry a long-context premium
//     (ContextThreshold + Above).
//   - Chinese vendors are priced in CNY, no cache tier, no context tier.
var priceTable = []priceEntry{
	// ── Anthropic — Claude Opus (no context tier; cw1h = 2× input) ──────────────
	{"claude-opus-4-8", ModelPrice{Tier: Tier{5, 25, 0.5, 6.25, 10}, Currency: "USD"}},
	{"claude-opus-4-7", ModelPrice{Tier: Tier{5, 25, 0.5, 6.25, 10}, Currency: "USD"}},
	{"claude-opus-4-6", ModelPrice{Tier: Tier{5, 25, 0.5, 6.25, 10}, Currency: "USD"}},
	{"claude-opus-4-5", ModelPrice{Tier: Tier{5, 25, 0.5, 6.25, 10}, Currency: "USD"}},
	{"claude-opus-4-1", ModelPrice{Tier: Tier{15, 75, 1.5, 18.75, 30}, Currency: "USD"}},
	{"claude-opus-4", ModelPrice{Tier: Tier{15, 75, 1.5, 18.75, 30}, Currency: "USD"}},

	// ── Anthropic — Claude Sonnet ──────────────────────────────────────────────
	{"claude-sonnet-4-6", ModelPrice{Tier: Tier{3, 15, 0.3, 3.75, 6}, Currency: "USD"}},
	{"claude-sonnet-4-5", ModelPrice{Tier: Tier{3, 15, 0.3, 3.75, 6}, Currency: "USD"}},
	{"claude-sonnet-4", ModelPrice{Tier: Tier{3, 15, 0.3, 3.75, 6}, Currency: "USD"}},
	{"claude-3-7-sonnet", ModelPrice{Tier: Tier{3, 15, 0.3, 3.75, 6}, Currency: "USD"}},

	// ── Anthropic — Claude Haiku ───────────────────────────────────────────────
	{"claude-haiku-4-5", ModelPrice{Tier: Tier{1, 5, 0.1, 1.25, 2}, Currency: "USD"}},
	{"claude-3-5-haiku", ModelPrice{Tier: Tier{0.8, 4, 0.08, 1.0, 1.6}, Currency: "USD"}},

	// ── Anthropic — Claude Fable (premium tier, 2× opus) ───────────────────────
	// "claude-fable" (longer) must beat the generic "claude" key for ids like
	// "claude-fable-5"; "fable" stays as a bare-id fallback.
	{"claude-fable", ModelPrice{Tier: Tier{10, 50, 1, 12.5, 20}, Currency: "USD"}},
	{"fable", ModelPrice{Tier: Tier{10, 50, 1, 12.5, 20}, Currency: "USD"}},

	// ── Anthropic — generic family fallbacks ───────────────────────────────────
	{"opus", ModelPrice{Tier: Tier{5, 25, 0.5, 6.25, 10}, Currency: "USD"}},
	{"sonnet", ModelPrice{Tier: Tier{3, 15, 0.3, 3.75, 6}, Currency: "USD"}},
	{"haiku", ModelPrice{Tier: Tier{1, 5, 0.1, 1.25, 2}, Currency: "USD"}},
	{"claude", ModelPrice{Tier: Tier{3, 15, 0.3, 3.75, 6}, Currency: "USD"}},

	// ── OpenAI / codex (no cache-write tier) ───────────────────────────────────
	// gpt-5.4 / gpt-5.5 carry a long-context premium above 272000 tokens.
	{"gpt-5.5", ModelPrice{Tier: Tier{5, 30, 0.5, 0, 0}, Currency: "USD",
		ContextThreshold: 272000, Above: &Tier{10, 45, 1, 0, 0}}},
	{"gpt-5.4", ModelPrice{Tier: Tier{2.5, 15, 0.25, 0, 0}, Currency: "USD",
		ContextThreshold: 272000, Above: &Tier{5, 22.5, 0.5, 0, 0}}},
	{"gpt-5.3", ModelPrice{Tier: Tier{1.75, 14, 0.175, 0, 0}, Currency: "USD"}},
	{"gpt-5.2", ModelPrice{Tier: Tier{1.75, 14, 0.175, 0, 0}, Currency: "USD"}},
	{"gpt-5.1", ModelPrice{Tier: Tier{1.25, 10, 0.125, 0, 0}, Currency: "USD"}},
	{"gpt-5", ModelPrice{Tier: Tier{1.25, 10, 0.125, 0, 0}, Currency: "USD"}},
	{"codex-mini", ModelPrice{Tier: Tier{1.5, 6, 0.375, 0, 0}, Currency: "USD"}},
	{"o3", ModelPrice{Tier: Tier{2, 8, 0.5, 0, 0}, Currency: "USD"}},
	{"o1", ModelPrice{Tier: Tier{15, 60, 7.5, 0, 0}, Currency: "USD"}},

	// OpenAI generic fallbacks.
	{"codex", ModelPrice{Tier: Tier{1.25, 10, 0.125, 0, 0}, Currency: "USD"}},
	{"gpt", ModelPrice{Tier: Tier{1.25, 10, 0.125, 0, 0}, Currency: "USD"}},

	// ── Gemini (no cache-write tier) ───────────────────────────────────────────
	// gemini-2.5-pro / gemini-3-pro carry a long-context premium above 200000 tokens.
	{"gemini-2.5-pro", ModelPrice{Tier: Tier{1.25, 10, 0.125, 0, 0}, Currency: "USD",
		ContextThreshold: 200000, Above: &Tier{2.5, 15, 0.25, 0, 0}}},
	{"gemini-2.5-flash", ModelPrice{Tier: Tier{0.3, 2.5, 0.03, 0, 0}, Currency: "USD"}},
	{"gemini-3-pro", ModelPrice{Tier: Tier{2, 12, 0.2, 0, 0}, Currency: "USD",
		ContextThreshold: 200000, Above: &Tier{4, 18, 0.4, 0, 0}}},
	{"gemini-3-flash", ModelPrice{Tier: Tier{0.5, 3, 0.05, 0, 0}, Currency: "USD"}},

	// Gemini generic fallback.
	{"gemini", ModelPrice{Tier: Tier{1.25, 10, 0.125, 0, 0}, Currency: "USD"}},

	// ── Chinese vendors (CNY per MILLION tokens; no cache tier, no context tier) ─
	// Official vendor list prices. Currency is CNY — Cost() returns CNY for these.
	{"glm-4-flash", ModelPrice{Tier: Tier{0.1, 0.1, 0, 0, 0}, Currency: "CNY"}},
	{"glm", ModelPrice{Tier: Tier{5, 5, 0, 0, 0}, Currency: "CNY"}},
	{"deepseek", ModelPrice{Tier: Tier{1, 2, 0.1, 0, 0}, Currency: "CNY"}},
	{"qwen-max", ModelPrice{Tier: Tier{2.4, 9.6, 0, 0, 0}, Currency: "CNY"}},
	{"qwen", ModelPrice{Tier: Tier{0.8, 2, 0, 0, 0}, Currency: "CNY"}},
}
