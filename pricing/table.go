package pricing

// priceTable is the embedded snapshot of model prices, in USD per MILLION tokens,
// validated against LiteLLM's model_prices_and_context_window.json (the same source
// ccusage uses). Keys are canonical lowercase model ids; the slice carries both
// specific ids and generic family fallbacks.
//
// Source order here is irrelevant: Lookup matches longest-key-first (see
// buildSortedTable), so a specific id (e.g. "claude-opus-4-8") always wins over a
// generic fallback (e.g. "opus"). To update prices, regenerate from the upstream
// LiteLLM JSON snapshot — see doc.go for the refresh mechanism.
//
// Column order: InputPerM, OutputPerM, CacheCreatePerM, CacheReadPerM.
//   - Anthropic models set all four (they have a cache-write tier).
//   - OpenAI/codex and Gemini set CacheCreatePerM == 0 (no cache-write tier).
var priceTable = []priceEntry{
	// ── Anthropic — Claude Opus ────────────────────────────────────────────────
	{"claude-opus-4-8", ModelPrice{5, 25, 6.25, 0.5, "USD"}},
	{"claude-opus-4-7", ModelPrice{5, 25, 6.25, 0.5, "USD"}},
	{"claude-opus-4-6", ModelPrice{5, 25, 6.25, 0.5, "USD"}},
	{"claude-opus-4-5", ModelPrice{5, 25, 6.25, 0.5, "USD"}},
	{"claude-opus-4-1", ModelPrice{15, 75, 18.75, 1.5, "USD"}},
	{"claude-opus-4", ModelPrice{15, 75, 18.75, 1.5, "USD"}},

	// ── Anthropic — Claude Sonnet ──────────────────────────────────────────────
	{"claude-sonnet-4-6", ModelPrice{3, 15, 3.75, 0.3, "USD"}},
	{"claude-sonnet-4-5", ModelPrice{3, 15, 3.75, 0.3, "USD"}},
	{"claude-sonnet-4", ModelPrice{3, 15, 3.75, 0.3, "USD"}},
	{"claude-3-7-sonnet", ModelPrice{3, 15, 3.75, 0.3, "USD"}},

	// ── Anthropic — Claude Haiku ───────────────────────────────────────────────
	{"claude-haiku-4-5", ModelPrice{1, 5, 1.25, 0.1, "USD"}},
	{"claude-3-5-haiku", ModelPrice{0.8, 4, 1.0, 0.08, "USD"}},

	// ── Anthropic — Claude Fable (premium tier, 2× opus) ───────────────────────
	// "claude-fable" (longer) must beat the generic "claude" key for ids like
	// "claude-fable-5"; "fable" stays as a bare-id fallback.
	{"claude-fable", ModelPrice{10, 50, 12.5, 1, "USD"}},
	{"fable", ModelPrice{10, 50, 12.5, 1, "USD"}},

	// ── Anthropic — generic family fallbacks ───────────────────────────────────
	{"opus", ModelPrice{5, 25, 6.25, 0.5, "USD"}},
	{"sonnet", ModelPrice{3, 15, 3.75, 0.3, "USD"}},
	{"haiku", ModelPrice{1, 5, 1.25, 0.1, "USD"}},
	{"claude", ModelPrice{3, 15, 3.75, 0.3, "USD"}},

	// ── OpenAI / codex (CacheCreatePerM == 0) ──────────────────────────────────
	{"gpt-5.5", ModelPrice{5, 30, 0, 0.5, "USD"}},
	{"gpt-5.4", ModelPrice{2.5, 15, 0, 0.25, "USD"}},
	{"gpt-5.3", ModelPrice{1.75, 14, 0, 0.175, "USD"}},
	{"gpt-5.2", ModelPrice{1.75, 14, 0, 0.175, "USD"}},
	{"gpt-5.1", ModelPrice{1.25, 10, 0, 0.125, "USD"}},
	{"gpt-5", ModelPrice{1.25, 10, 0, 0.125, "USD"}},
	{"codex-mini", ModelPrice{1.5, 6, 0, 0.375, "USD"}},
	{"o3", ModelPrice{2, 8, 0, 0.5, "USD"}},
	{"o1", ModelPrice{15, 60, 0, 7.5, "USD"}},

	// OpenAI generic fallbacks.
	{"codex", ModelPrice{1.25, 10, 0, 0.125, "USD"}},
	{"gpt", ModelPrice{1.25, 10, 0, 0.125, "USD"}},

	// ── Gemini (CacheCreatePerM == 0) ──────────────────────────────────────────
	{"gemini-2.5-pro", ModelPrice{1.25, 10, 0, 0.125, "USD"}},
	{"gemini-2.5-flash", ModelPrice{0.3, 2.5, 0, 0.03, "USD"}},
	{"gemini-3-pro", ModelPrice{2, 12, 0, 0.2, "USD"}},
	{"gemini-3-flash", ModelPrice{0.5, 3, 0, 0.05, "USD"}},

	// Gemini generic fallback.
	{"gemini", ModelPrice{1.25, 10, 0, 0.125, "USD"}},

	// ── Chinese vendors (CNY per MILLION tokens; no cache-write tier) ───────────
	// Official vendor list prices. Currency is CNY — Cost() returns CNY for these.
	{"glm-4-flash", ModelPrice{0.1, 0.1, 0, 0, "CNY"}},
	{"glm", ModelPrice{5, 5, 0, 0, "CNY"}},
	{"deepseek", ModelPrice{1, 2, 0, 0.1, "CNY"}},
	{"qwen-max", ModelPrice{2.4, 9.6, 0, 0, "CNY"}},
	{"qwen", ModelPrice{0.8, 2, 0, 0, "CNY"}},
}
