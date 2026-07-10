// Package usage — codex_model_source.go: ModelScanSource backed by the codex
// CLI's own rollout transcripts.
//
// Data path: ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl (or DW_CODEX_HOME for
// tests — the same override github.com/brightman-ai/kit/transcript.CodexSource
// honors).
//
// SSOT: rollout discovery is delegated to transcript.CodexSource.ListSessions
// (the SAME source runtime_session_routes.go's buildAggregator wires for
// per-session transcript viewing), and per-turn token extraction is delegated
// to transcript.ScanCodexModelUsage (the shared primitive factored out of
// CodexSource.LoadTranscript's usage-footer logic). This file adds NO rollout
// parsing of its own — only (date, model) bucketing over the shared source's
// output, mirroring the shape of JSONLTokenSource.ScanModelRange for claude.
package usage

import (
	"context"

	"github.com/brightman-ai/kit/transcript"
)

// CodexModelScanSource implements ModelScanSource by walking every codex
// rollout transcript (across all projects) and bucketing their per-turn,
// model-tagged token deltas by UTC date.
type CodexModelScanSource struct {
	src *transcript.CodexSource
}

// NewCodexModelScanSource creates a source rooted at ~/.codex (or DW_CODEX_HOME).
func NewCodexModelScanSource() *CodexModelScanSource {
	return &CodexModelScanSource{src: transcript.NewCodexSource()}
}

// NewCodexModelScanSourceAt creates a source for a custom codex home dir
// (tests). dir must contain a "sessions/" subtree, matching the real ~/.codex
// layout (transcript.CodexSource.Root).
func NewCodexModelScanSourceAt(dir string) *CodexModelScanSource {
	return &CodexModelScanSource{src: &transcript.CodexSource{Root: dir}}
}

// ScanModelRange implements ModelScanSource: enumerates every codex rollout
// (ListSessions with an empty projectDir applies no cwd filter — every session
// across every project is included, matching JSONLTokenSource's whole-tree
// scan), extracts each one's per-turn model-tagged deltas via the shared
// transcript.ScanCodexModelUsage primitive, and re-aggregates into
// per-(date, model) bundles restricted to [startDate, endDate] (UTC, inclusive).
//
// DEDUP: transcript.ScanCodexModelUsage already sums only last_token_usage (the
// per-turn delta) — never the cumulative total_token_usage a rollout carries —
// so this layer never re-derives or double-sums a running total; it only adds
// up already-deduped per-turn deltas.
func (s *CodexModelScanSource) ScanModelRange(startDate, endDate string) ([]ModelTokens, error) {
	if startDate > endDate {
		startDate, endDate = endDate, startDate
	}

	metas, err := s.src.ListSessions(context.Background(), "")
	if err != nil {
		return nil, err
	}

	type dateModel struct{ date, model string }
	agg := make(map[dateModel]*ModelTokens, len(metas))

	for _, m := range metas {
		if m.SsotPath == "" {
			continue
		}
		events, scanErr := transcript.ScanCodexModelUsage(m.SsotPath)
		if scanErr != nil {
			continue // unreadable/corrupt rollout → honest skip, never abort the whole scan
		}
		for _, e := range events {
			if e.At.IsZero() {
				continue
			}
			date := e.At.UTC().Format("2006-01-02")
			if date < startDate || date > endDate {
				continue
			}
			model := e.Model
			if model == "" {
				model = "unknown"
			}
			key := dateModel{date: date, model: model}
			b := agg[key]
			if b == nil {
				b = &ModelTokens{Date: date, Model: model}
				agg[key] = b
			}
			b.InputTokens += int64(e.InputTokens)
			b.OutputTokens += int64(e.OutputTokens)
			b.CacheReadTokens += int64(e.CacheReadTokens)
			// codex has no cache-WRITE concept (only cache-read); CacheCreateTokens
			// stays 0 — honest, never fabricated.
		}
	}

	out := make([]ModelTokens, 0, len(agg))
	for _, b := range agg {
		out = append(out, *b)
	}
	return out, nil
}

// DailyTokens implements TokenSource for interface completeness (a
// *CodexModelScanSource can then be passed to BuildReport standalone, or type-
// asserted as ModelScanSource by CombinedModelScanSource). BuildReport prefers
// the richer ScanModelRange path above whenever it succeeds — which is always,
// since ScanModelRange never itself returns an error — so this is not on the
// hot path in practice.
func (s *CodexModelScanSource) DailyTokens(date string) (inputTokens, outputTokens, cacheReadTokens int64, err error) {
	bundles, err := s.ScanModelRange(date, date)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, b := range bundles {
		inputTokens += b.InputTokens
		outputTokens += b.OutputTokens
		cacheReadTokens += b.CacheReadTokens
	}
	return inputTokens, outputTokens, cacheReadTokens, nil
}
