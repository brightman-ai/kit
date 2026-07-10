// Package usage is the host-agnostic SSOT for LLM usage, cost, and subscription
// quota reporting. It reads locally-written agent transcript logs and rate-limit
// drop files, aggregates per-model token deltas, and prices them via kit/pricing —
// ccusage-style: local logs + public pricing, no proprietary data, no network.
//
// Layering (why this lives in kit, not in a host):
//   - It sits on kit/transcript (session enumeration + per-turn token scanning)
//     and kit/pricing (model → USD/CNY, the pricing SSOT).
//   - It is pure compute + local file reads: every source is derived from $HOME
//     or an env override (CLAUDE_CONFIG_DIR / DEEPWORK_HOME / DW_CODEX_HOME).
//     No DB, no config injection, no HTTP. Missing/empty inputs degrade honestly
//     (ok=false) rather than guessing.
//   - HTTP exposure ( /usage/quota, /usage/report ) belongs to the HOST that
//     mounts it (deepwork-terminal serves it; deepwork-pro forwards to the same
//     handler). Routing is deliberately NOT in this package — kit ships primitives,
//     hosts ship endpoints.
//
// Both deepwork-terminal (standalone) and deepwork-pro (embedded) import this one
// package, so the numbers can never drift between deployments.
//
// The three entry points:
//   - BuildReport(window, TokenSource) — per-provider token + cost report.
//   - ComputeCost(model, in, out, cacheRead, cacheCreate) — single-call cost.
//   - QueryAllQuotas() — subscription 5h/7d remaining%, per detected runtime.
//
// Sources (compose via CombinedModelScanSource / JSONLTokenSource /
// CodexModelScanSource) turn on-disk transcripts into the token deltas the report
// and cost paths consume.
package usage
