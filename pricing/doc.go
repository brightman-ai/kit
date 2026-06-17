// SSOT & update mechanism
// ────────────────────────
//
// This package is the single source of truth for LLM model pricing across
// deepwork-terminal and deepwork-pro. Both consume it as peers; neither owns it.
//
// The embedded priceTable (table.go) is a hand-curated SNAPSHOT of LiteLLM's
// model_prices_and_context_window.json — the same dataset ccusage uses. It carries
// no network code by design: pricing must be deterministic, offline, and reviewable
// in a diff. To update prices, edit table.go directly with the new LiteLLM values
// (USD per million tokens) and re-run the tests, which pin the ccusage-verified
// anchors.
//
// Cost is computed PER REQUEST, with two refinements over a flat table:
//
//   - Cache-write is split by TTL: CacheWrite5m (5-minute, 1.25× input) vs
//     CacheWrite1h (1-hour, 2× input). The transcript usage exposes these as
//     cache_creation.ephemeral_5m_input_tokens / ephemeral_1h_input_tokens.
//   - A long-context PREMIUM tier (ModelPrice.Above) applies when a request's
//     context exceeds ModelPrice.ContextThreshold (OpenAI gpt-5.4/5.5: 272000;
//     Gemini 2.5-pro / 3-pro: 200000). Anthropic 4.x has no context tier.
//
// Future (OPTIONAL, not implemented now): a `go:generate` directive could fetch the
// upstream LiteLLM JSON and regenerate table.go, and a `Refresh`-style API could
// hot-reload from a cached file. Both are deferred — the snapshot is sufficient and
// keeps the package free of network dependencies. Do not add network code here
// without an explicit decision to take on that complexity.
//
//	//go:generate go run ./internal/gen-table  // future: regenerate from LiteLLM JSON
package pricing
