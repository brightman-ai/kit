package usage

import (
	"testing"
	"time"
)

// TestComputeCost_KnownModels verifies the single cost SSOT prices the four token
// categories per-million and matches the right currency/family.
func TestComputeCost_KnownModels(t *testing.T) {
	// claude opus 4.x (LiteLLM-authoritative): in 5, out 25, cacheRead 0.5, cacheCreate 6.25 per 1M.
	// 1M in + 1M out + 1M cacheRead + 1M cacheCreate = 5+25+0.5+6.25 = 36.75 USD.
	got := ComputeCost("claude-opus-4-8-20251101", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if !got.HasPrice {
		t.Fatal("expected HasPrice=true for opus")
	}
	if got.Currency != "USD" {
		t.Fatalf("currency = %q, want USD", got.Currency)
	}
	if got.TotalCost != 36.75 {
		t.Fatalf("opus total = %v, want 36.75", got.TotalCost)
	}

	// haiku 4.x: in 1, out 5 per 1M. 1M in + 1M out = 6 USD.
	h := ComputeCost("claude-haiku-4-5-20251001", 1_000_000, 1_000_000, 0, 0)
	if h.TotalCost != 6 {
		t.Fatalf("haiku total = %v, want 6", h.TotalCost)
	}

	// glm is a CNY-priced model in the SSOT (Chinese vendor list price): 1M input × ¥5/M = ¥5.
	g := ComputeCost("glm-4.7", 1_000_000, 0, 0, 0)
	if !g.HasPrice || g.Currency != "CNY" || g.TotalCost != 5 {
		t.Fatalf("glm should be CNY-priced (¥5 for 1M input), got %+v", g)
	}
}

// TestComputeCost_MissingPrice — unknown model → HasPrice=false (honest: 缺价不蒙).
func TestComputeCost_MissingPrice(t *testing.T) {
	got := ComputeCost("some-unknown-model-xyz", 1_000_000, 1_000_000, 0, 0)
	if got.HasPrice {
		t.Fatalf("expected HasPrice=false for unknown model, got %+v", got)
	}
	if got.TotalCost != 0 {
		t.Fatalf("expected zero cost for unpriced model, got %v", got.TotalCost)
	}
}

// TestComputeCost_LongestMatchWins — "haiku" must beat the generic "claude" row.
func TestComputeCost_LongestMatchWins(t *testing.T) {
	h := ComputeCost("claude-haiku-4-5", 1_000_000, 0, 0, 0)
	// haiku input = 1; generic claude input = 3. Must pick 1.
	if h.TotalCost != 1 {
		t.Fatalf("longest-match: haiku input cost = %v, want 1 (not generic claude 3)", h.TotalCost)
	}
}

// modelStub implements ModelScanSource for BuildReport cost-path testing.
type modelStub struct{ bundles []ModelTokens }

func (m modelStub) DailyTokens(string) (int64, int64, int64, error) { return 0, 0, 0, nil }
func (m modelStub) ScanModelRange(string, string) ([]ModelTokens, error) {
	return m.bundles, nil
}

// TestBuildReport_CostAndProviders — the report aggregates cache_create, computes
// per-day + window cost, and emits a per-provider breakdown.
func TestBuildReport_CostAndProviders(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	src := modelStub{bundles: []ModelTokens{
		{Date: today, Model: "claude-opus-4-8", InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 0, CacheCreateTokens: 0},
		{Date: today, Model: "gpt-5", InputTokens: 1_000_000, OutputTokens: 0, CacheReadTokens: 0, CacheCreateTokens: 0},
		{Date: today, Model: "unknown-model", InputTokens: 500_000, OutputTokens: 0, CacheReadTokens: 0, CacheCreateTokens: 0},
	}}
	rep := BuildReport(Window7d, src)
	if !rep.Available {
		t.Fatal("report not available")
	}
	// Summary tokens: in = 1M+1M+0.5M = 2.5M; out = 1M.
	if rep.Summary.InputTokens != 2_500_000 || rep.Summary.OutputTokens != 1_000_000 {
		t.Fatalf("summary tokens = in %d out %d, want 2.5M / 1M", rep.Summary.InputTokens, rep.Summary.OutputTokens)
	}
	// Cost present (opus + gpt-5 priced) but NOT complete (unknown-model has no price).
	if rep.Summary.Cost == nil {
		t.Fatal("summary cost should be non-nil (opus/gpt-5 priced)")
	}
	if rep.Summary.CostComplete {
		t.Fatal("CostComplete should be false — unknown-model has no price")
	}
	// opus 4.x: 1M*5 + 1M*25 = 30; gpt-5: 1M*1.25 = 1.25 → 31.25.
	if *rep.Summary.Cost != 31.25 {
		t.Fatalf("window cost = %v, want 31.25", *rep.Summary.Cost)
	}
	// Providers: Claude, OpenAI, 其他 (3 distinct).
	if len(rep.Providers) != 3 {
		t.Fatalf("providers = %d, want 3", len(rep.Providers))
	}
	// Claude should top (2M tokens) and carry a cost; 其他 (unknown) has nil cost.
	if rep.Providers[0].Provider != "Claude" || rep.Providers[0].Cost == nil {
		t.Fatalf("top provider = %+v, want Claude with cost", rep.Providers[0])
	}
	var other *ProviderRow
	for i := range rep.Providers {
		if rep.Providers[i].Provider == "其他" {
			other = &rep.Providers[i]
		}
	}
	if other == nil || other.Cost != nil {
		t.Fatalf("其他 provider should exist with nil cost (unpriced), got %+v", other)
	}
	// Per-day rows carry cache_create field (zero here) + correct count.
	if len(rep.Rows) != 7 {
		t.Fatalf("rows = %d, want 7 (7d window)", len(rep.Rows))
	}
}
