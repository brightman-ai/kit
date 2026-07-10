// Package usage — multi_source.go: merges several ModelScanSource (claude
// JSONL, codex rollout, ...) into one so BuildReport can price every wired
// runtime's usage in a single report (CHG cost-across-windows: codex cost was
// previously absent from the usage report because the only wired source was
// claude-only JSONLTokenSource).
package usage

// CombinedModelScanSource merges N ModelScanSource into one. Concatenating
// their bundles is safe: BuildReport's buildFromModelBundles re-aggregates
// every ModelTokens bundle into its own (date, model) key regardless of which
// source produced it — callers don't need to pre-merge across sources, and
// different runtimes never collide on (date, model) since model ids are
// runtime-distinct (e.g. "claude-opus-4-8" vs "gpt-5.4").
type CombinedModelScanSource struct {
	Sources []ModelScanSource
}

// ScanModelRange implements ModelScanSource. One source's error does not
// abort the whole report — the other sources' usage still surfaces (honest
// partial data beats an all-or-nothing report); it is never itself an error.
func (c *CombinedModelScanSource) ScanModelRange(startDate, endDate string) ([]ModelTokens, error) {
	var out []ModelTokens
	for _, s := range c.Sources {
		if s == nil {
			continue
		}
		bundles, err := s.ScanModelRange(startDate, endDate)
		if err != nil {
			continue
		}
		out = append(out, bundles...)
	}
	return out, nil
}

// DailyTokens implements TokenSource for interface completeness (BuildReport's
// signature requires it). BuildReport always prefers the ModelScanSource path
// above — which never errors here — so this is not exercised in practice, but
// sums correctly across sources if ever called directly.
func (c *CombinedModelScanSource) DailyTokens(date string) (inputTokens, outputTokens, cacheReadTokens int64, err error) {
	bundles, err := c.ScanModelRange(date, date)
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
