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
// Future (OPTIONAL, not implemented now): a `go:generate` directive could fetch the
// upstream LiteLLM JSON and regenerate table.go, and a `Refresh`-style API could
// hot-reload from a cached file. Both are deferred — the snapshot is sufficient and
// keeps the package free of network dependencies. Do not add network code here
// without an explicit decision to take on that complexity.
//
//	//go:generate go run ./internal/gen-table  // future: regenerate from LiteLLM JSON
package pricing
