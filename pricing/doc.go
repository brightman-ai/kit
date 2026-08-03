// SSOT & update mechanism
// ────────────────────────
//
// This package is the single source of truth for LLM model pricing across
// deepwork-terminal and deepwork-pro. Both consume it as peers; neither owns it.
//
// Prices come from three layers, and which layer answered is itself a fact the
// caller can see (RequestQuote.VerifiedAt / GeneratedSnapshot):
//
//	catalog.go    — effective-dated, service-tier-aware rules a human read from the
//	                vendor's own page. The ONLY layer the per-request report path
//	                uses, and the only one carrying credits and priority tiers.
//	table.go      — hand-curated table. Anthropic's split cache-write TTLs, the
//	                long-context premium tiers and the CNY vendors live only here.
//	table_gen.go  — generated from LiteLLM's model_prices_and_context_window.json,
//	                the same dataset ccusage uses. Breadth: ~289 exact model ids.
//
// The package itself still carries NO network code — pricing must be deterministic,
// offline and reviewable in a diff. The fetch lives in a separate main package that
// a human runs, and what lands in the repo is a .go file reviewed like any other:
//
//	go generate ./pricing/...
//
// To correct a single price, edit table.go or catalog.go — both outrank the
// generated table on an exact id, and the tests pin the ccusage-verified anchors.
// Lookup order and the reason for it are documented on lookupLegacy.
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
// Still deliberately NOT here: a Refresh-style API that hot-reloads prices at
// runtime. A price that can change without a diff cannot be audited after the fact,
// and every number this package produces is meant to be explainable months later.
//
//go:generate go run ./internal/gen-table -out table_gen.go
package pricing
