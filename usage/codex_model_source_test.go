// codex_model_source_test.go — CHG usage-report codex cost coverage.
//
// Proves CodexModelScanSource (a) correctly dedups the codex rollout DEDUP
// HAZARD (a cumulative total_token_usage repeated across several token_count
// lines, plus a genuine alternating-delta pattern matching real observed data:
// a total repeated 4×, then two distinct deltas 26827/15037 alternating —
// consistent with a main+subagent or multi-turn rollout), (b) that BuildReport
// merges it alongside a claude source so a codex ProviderRow with a real,
// nonzero cost falls out, and (c) that all four report windows resolve.
//
// All fixtures are synthetic, written under t.TempDir() — this test NEVER
// touches the real ~/.codex or ~/.claude directories.
package usage

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/brightman-ai/kit/pricing"
)

// writeCodexRolloutFixture writes one synthetic rollout-*.jsonl under
// <root>/sessions/YYYY/MM/DD/ for UTC date `date` (YYYY-MM-DD), modeling the
// DEDUP HAZARD from real ~/.codex data:
//   - a token_count line with a real per-turn delta (last_token_usage nonzero)
//   - 3 more token_count lines carrying the SAME cumulative total_token_usage
//     but a ZERO last_token_usage (repeated snapshot lines codex emits, e.g.
//     around rate-limit refresh) — these MUST be skipped, not summed.
//   - two more real per-turn deltas (26827 / 15037 — the exact numbers
//     "observed" per the task brief) under the SAME model, proving the
//     alternating-delta pattern sums correctly instead of being maxed or
//     double-counted.
//
// Returns the per-model expected totals for the caller to assert against.
func writeCodexRolloutFixture(t *testing.T, root, date, model string) (wantInput, wantOutput, wantCacheRead int64) {
	t.Helper()
	ts, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatalf("parse date: %v", err)
	}
	dir := filepath.Join(root, "sessions", ts.Format("2006"), ts.Format("01"), ts.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	id := "0199test-" + date + "-dead-beef-000000000001"
	base := date + "T09:00:00.000Z"

	tokenCountLine := func(hhmmss string, total, last struct{ in, cached, out, reasoning int }) string {
		ttl := date + "T" + hhmmss + ".000Z"
		return `{"timestamp":"` + ttl + `","type":"event_msg","payload":{"type":"token_count","info":{` +
			`"total_token_usage":{"input_tokens":` + strconv.Itoa(total.in) + `,"cached_input_tokens":` + strconv.Itoa(total.cached) +
			`,"output_tokens":` + strconv.Itoa(total.out) + `,"reasoning_output_tokens":` + strconv.Itoa(total.reasoning) +
			`,"total_tokens":` + strconv.Itoa(total.in+total.out+total.reasoning) + `},` +
			`"last_token_usage":{"input_tokens":` + strconv.Itoa(last.in) + `,"cached_input_tokens":` + strconv.Itoa(last.cached) +
			`,"output_tokens":` + strconv.Itoa(last.out) + `,"reasoning_output_tokens":` + strconv.Itoa(last.reasoning) +
			`,"total_tokens":` + strconv.Itoa(last.in+last.out+last.reasoning) + `}}}}`
	}

	type tu = struct{ in, cached, out, reasoning int }

	lines := []string{
		`{"timestamp":"` + base + `","type":"session_meta","payload":{"id":"` + id + `","timestamp":"` + base + `","cwd":"/tmp/codex-fixture"}}`,
		`{"timestamp":"` + base + `","type":"turn_context","payload":{"turn_id":"t1","model":"` + model + `"}}`,
		// real turn 1: 11790 in / 9000 cached / 200 out / 10 reasoning
		tokenCountLine("09:01:00", tu{11790, 9000, 200, 10}, tu{11790, 9000, 200, 10}),
		// 3 repeated snapshots of the SAME cumulative total, zero delta → must be skipped
		tokenCountLine("09:01:05", tu{11790, 9000, 200, 10}, tu{0, 0, 0, 0}),
		tokenCountLine("09:01:10", tu{11790, 9000, 200, 10}, tu{0, 0, 0, 0}),
		tokenCountLine("09:01:15", tu{11790, 9000, 200, 10}, tu{0, 0, 0, 0}),
		// real turn 2: delta 26827 in / 11000 cached / 700 out / 40 reasoning
		tokenCountLine("09:02:00", tu{38617, 20000, 900, 50}, tu{26827, 11000, 700, 40}),
		// real turn 3: delta 15037 in / 9000 cached / 300 out / 10 reasoning
		tokenCountLine("09:03:00", tu{53654, 29000, 1200, 60}, tu{15037, 9000, 300, 10}),
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	path := filepath.Join(dir, "rollout-"+date+"T09-00-00-"+id+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write rollout fixture: %v", err)
	}

	wantInput = 11790 + 26827 + 15037
	wantOutput = (200 + 10) + (700 + 40) + (300 + 10) // reasoning folds into output
	wantCacheRead = 9000 + 11000 + 9000
	return wantInput, wantOutput, wantCacheRead
}

// (a) CodexModelScanSource dedups the interleaved cumulative/repeat pattern
// correctly: exactly one bucket for (date, model), tokens = sum of the 3 REAL
// per-turn deltas, not 4× the repeated total and not a naive max.
func TestCodexModelScanSource_DedupsInterleavedTotals(t *testing.T) {
	root := t.TempDir()
	today := time.Now().UTC().Format("2006-01-02")
	wantIn, wantOut, wantCacheRead := writeCodexRolloutFixture(t, root, today, "gpt-5.4")

	src := NewCodexModelScanSourceAt(root)
	bundles, err := src.ScanModelRange(today, today)
	if err != nil {
		t.Fatalf("ScanModelRange: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected exactly 1 (date,model) bundle, got %d: %+v", len(bundles), bundles)
	}
	b := bundles[0]
	if b.Date != today || b.Model != "gpt-5.4" {
		t.Fatalf("bundle key = (%s,%s), want (%s,gpt-5.4)", b.Date, b.Model, today)
	}
	if b.InputTokens != wantIn {
		t.Errorf("InputTokens = %d, want %d (11790+26827+15037, NOT 11790×4=%d)", b.InputTokens, wantIn, 11790*4)
	}
	if b.OutputTokens != wantOut {
		t.Errorf("OutputTokens = %d, want %d", b.OutputTokens, wantOut)
	}
	if b.CacheReadTokens != wantCacheRead {
		t.Errorf("CacheReadTokens = %d, want %d", b.CacheReadTokens, wantCacheRead)
	}
	if b.CacheCreateTokens != 0 {
		t.Errorf("CacheCreateTokens = %d, want 0 (codex has no cache-write concept)", b.CacheCreateTokens)
	}
}

// (b) BuildReport, given a CombinedModelScanSource merging a claude fixture and
// this codex fixture, emits a codex ProviderRow (runtime="codex") with a real,
// nonzero cost — proving codex cost now reaches the report end-to-end.
func TestBuildReport_MergesCodexProviderRowWithCost(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")

	codexRoot := t.TempDir()
	wantIn, wantOut, wantCacheRead := writeCodexRolloutFixture(t, codexRoot, today, "gpt-5.4")
	codexSrc := NewCodexModelScanSourceAt(codexRoot)

	claudeSrc := modelStub{bundles: []ModelTokens{
		{Date: today, Model: "claude-opus-4-8", InputTokens: 1_000_000, OutputTokens: 200_000},
	}}

	combined := &CombinedModelScanSource{Sources: []ModelScanSource{claudeSrc, codexSrc}}
	rep := BuildReport(Window7d, combined)

	if !rep.Available {
		t.Fatalf("report not available (reason=%q)", rep.Reason)
	}

	var codexRow, claudeRow *ProviderRow
	for i := range rep.Providers {
		switch rep.Providers[i].Runtime {
		case "codex":
			codexRow = &rep.Providers[i]
		case "claude":
			claudeRow = &rep.Providers[i]
		}
	}
	if claudeRow == nil {
		t.Fatalf("expected a claude ProviderRow (merge sanity check), providers=%+v", rep.Providers)
	}
	if codexRow == nil {
		t.Fatalf("expected a codex ProviderRow (runtime=codex), providers=%+v", rep.Providers)
	}
	if codexRow.Provider != "OpenAI" {
		t.Errorf("codex row Provider = %q, want OpenAI", codexRow.Provider)
	}
	wantTotal := wantIn + wantOut + wantCacheRead
	if codexRow.TotalTokens != wantTotal {
		t.Errorf("codex row TotalTokens = %d, want %d (deduped)", codexRow.TotalTokens, wantTotal)
	}
	if codexRow.Cost == nil {
		t.Fatal("codex row Cost is nil — expected a real price for gpt-5.4")
	}
	t.Logf("codex row (deduped): input=%d output=%d cacheRead=%d total=%d cost=%v currency=%s",
		codexRow.InputTokens, codexRow.OutputTokens, codexRow.CacheReadTokens, codexRow.TotalTokens, *codexRow.Cost, codexRow.Currency)
	if *codexRow.Cost <= 0 {
		t.Fatalf("codex row Cost = %v, want > 0", *codexRow.Cost)
	}
	// Cross-check against the SAME pricing SSOT the report itself calls
	// (ComputeCost/kit-pricing), proving BuildReport wired the deduped tokens
	// through correctly rather than some other number.
	want := ComputeCost("gpt-5.4", wantIn, wantOut, wantCacheRead, 0)
	if !want.HasPrice {
		t.Fatal("expected kit/pricing to have a gpt-5.4 row (test setup invariant)")
	}
	if *codexRow.Cost != want.TotalCost {
		t.Errorf("codex row Cost = %v, want %v (ComputeCost on the same deduped tokens)", *codexRow.Cost, want.TotalCost)
	}
	if codexRow.Currency != want.Currency {
		t.Errorf("codex row Currency = %q, want %q", codexRow.Currency, want.Currency)
	}
	if !rep.Summary.CostComplete {
		t.Errorf("CostComplete should be true — both claude-opus-4-8 and gpt-5.4 are priced")
	}
}

// gpt-5.x codex CLI model-id variants actually observed in real ~/.codex data
// (gpt-5.N / gpt-5.N-codex / gpt-5.N-codex-max) must all resolve to a real
// price via kit/pricing — otherwise the report would silently under-count
// codex cost (HasPrice=false) for the exact ids codex emits.
func TestCodexObservedModelIDs_HaveKnownPrices(t *testing.T) {
	models := []string{
		"gpt-5.5", "gpt-5.4",
		"gpt-5.3-codex", "gpt-5.2-codex",
		"gpt-5.1-codex-max", "gpt-5.2-codex-max",
	}
	for _, m := range models {
		if _, ok := pricing.Lookup(m); !ok {
			t.Errorf("pricing.Lookup(%q) = not found; kit/pricing/table.go needs a row (or family fallback) for this real codex model id", m)
		}
	}
}

// (c) All four report windows resolve against the combined claude+codex source.
func TestBuildReport_AllFourWindowsResolve(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	codexRoot := t.TempDir()
	writeCodexRolloutFixture(t, codexRoot, today, "gpt-5.4")
	combined := &CombinedModelScanSource{Sources: []ModelScanSource{
		modelStub{bundles: []ModelTokens{{Date: today, Model: "claude-opus-4-8", InputTokens: 1000, OutputTokens: 500}}},
		NewCodexModelScanSourceAt(codexRoot),
	}}

	cases := []struct {
		window   WindowKind
		wantDays int
	}{
		{Window24h, 1},
		{Window7d, 7},
		{Window14d, 14},
		{Window30d, 30},
	}
	for _, c := range cases {
		rep := BuildReport(c.window, combined)
		if !rep.Available {
			t.Errorf("window %s: report not available (reason=%q)", c.window, rep.Reason)
			continue
		}
		if rep.Window != c.window {
			t.Errorf("window %s: rep.Window = %q", c.window, rep.Window)
		}
		if len(rep.Rows) != c.wantDays {
			t.Errorf("window %s: rows = %d, want %d", c.window, len(rep.Rows), c.wantDays)
		}
		if len(rep.Providers) == 0 {
			t.Errorf("window %s: expected non-empty providers (today's fixture is always in range)", c.window)
		}
	}
}
