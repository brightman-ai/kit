package pricing

// priceTable is the embedded snapshot of model prices, in USD (or CNY) per MILLION
// tokens, validated against LiteLLM's model_prices_and_context_window.json (the same
// source ccusage uses). Keys are canonical lowercase model ids. OpenAI and
// Anthropic intentionally have no generic family fallbacks: an unknown future
// model is unpriced until a verified rule is added.
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
	{"claude-opus-5", ModelPrice{Tier: Tier{5, 25, 0.5, 6.25, 10}, Currency: "USD"}},
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

	// ── Moonshot / Kimi (CNY per MILLION tokens; no cache-write tier) ───────────
	// Moonshot publishes TWO lists for the same model — ¥20/¥100 per 1M on
	// platform.moonshot.cn and $3.00/$15.00 on platform.kimi.ai, the same price at
	// its own 6.67 conversion. CNY is the one used, because that is the platform
	// billed against here; catalog.go carries the USD list alongside so a surface
	// can show both without this package ever holding an exchange rate.
	//
	// Listed per model, NOT as a generic "kimi" family: k3 and the k2.x line differ
	// several-fold, so a family fallback would price a future model by coincidence.
	// "k3" is the bare id codex records; "kimi-k3" is the canonical one.
	//
	// The k2.x line is deliberately absent: its CNY list price has not been
	// verified, and upstream's USD figure cannot be borrowed for it — one vendor,
	// two currencies is the split this whole file exists to prevent.
	{"kimi-k3", ModelPrice{Tier: Tier{20, 100, 2, 0, 0}, Currency: "CNY"}},
	{"k3", ModelPrice{Tier: Tier{20, 100, 2, 0, 0}, Currency: "CNY"}},

	// ── DeepSeek (USD per MILLION tokens; no cache-write tier) ──────────────────
	// Was a single generic "deepseek" key at CNY {1, 2, 0.1} — a V3-era number that
	// had gone stale in both the price AND the currency, and that a generic key let
	// silently apply to every future model. Now per model, from DeepSeek's own
	// pricing page (LiteLLM agrees to the digit). No generic fallback: v4-flash and
	// v4-pro differ by ~3×.
	{"deepseek-v4-flash", ModelPrice{Tier: Tier{0.14, 0.28, 0.0028, 0, 0}, Currency: "USD"}},
	{"deepseek-v4-pro", ModelPrice{Tier: Tier{0.435, 0.87, 0.003625, 0, 0}, Currency: "USD"}},
	{"deepseek-chat", ModelPrice{Tier: Tier{0.28, 0.42, 0.028, 0, 0}, Currency: "USD"}},

	// ── Zhipu / GLM (CNY per MILLION tokens; no cache-write tier) ───────────────
	// open.bigmodel.cn/pricing, read 2026-08-05. Exact ids only — the generic "glm"
	// key that used to sit here was a family fallback at ¥5/¥5 whose output rate was
	// 5.6× under the current flagship, and being generic it would have applied that
	// to every future GLM. Same rule OpenAI and Anthropic follow above, same reason.
	{"glm-5.2", ModelPrice{Tier: Tier{8, 28, 2, 0, 0}, Currency: "CNY"}},
	{"glm-5.1", ModelPrice{Tier: Tier{6, 24, 1.3, 0, 0}, Currency: "CNY"}},

	// ── Removed rather than left stale ─────────────────────────────────────────
	// "qwen" / "qwen-max" carried ¥0.8/¥2 and ¥2.4/¥9.6 from an unrecorded vintage,
	// and Alibaba's line has since moved to qwen3.8-max / qwen3.7-plus / qwen3.7-flash
	// — the ids those keys priced are no longer the models anyone runs. No current
	// figure was verifiable at the time of writing, so they are gone: a qwen request
	// now resolves to its vendor and shows「无价表」, which is a question the reader
	// can act on. A stale number is one they cannot even see is wrong.
	// "glm-4-flash" went the same way; upstream now lists that line as free, which is
	// a different claim from ¥0.1 and not one to guess at.
}
