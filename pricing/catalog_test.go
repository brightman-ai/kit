package pricing

import (
	"math"
	"testing"
	"time"
)

func atDate(v string) time.Time {
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCatalogOpenAIServiceTierAndEffort(t *testing.T) {
	c := DefaultCatalog()
	standard, ok := c.Quote(RequestQuery{Model: "gpt-5.6-sol", At: atDate("2026-07-14"), ServiceTier: "default", Effort: "low"})
	if !ok {
		t.Fatal("standard quote missing")
	}
	priority, ok := c.Quote(RequestQuery{Model: "gpt-5.6-sol", At: atDate("2026-07-14"), ServiceTier: "priority", Effort: "xhigh"})
	if !ok {
		t.Fatal("priority quote missing")
	}
	if standard.Price.InputPerM != 5 || priority.Price.InputPerM != 10 || standard.Price.OutputPerM != 30 || priority.Price.OutputPerM != 60 {
		t.Fatalf("tier rates wrong: standard=%+v priority=%+v", standard.Price.Tier, priority.Price.Tier)
	}
	// Effort is evidence, not a unit-price dimension.
	high, _ := c.Quote(RequestQuery{Model: "gpt-5.6-sol", At: atDate("2026-07-14"), ServiceTier: "default", Effort: "xhigh"})
	if high.RuleID != standard.RuleID || high.Price != standard.Price {
		t.Fatalf("effort changed unit price: low=%+v xhigh=%+v", standard, high)
	}
	credits, ok := standard.Credits(Usage{Input: 1_000_000, CacheRead: 1_000_000, Output: 1_000_000})
	if !ok || credits != 887.5 {
		t.Fatalf("credits=%v ok=%v, want 887.5", credits, ok)
	}
}

func TestCatalogOpenAIPriorityCurrentModels(t *testing.T) {
	tests := []struct {
		model                 string
		input, cached, output float64
	}{
		{"gpt-5.5", 12.5, 1.25, 75},
		{"gpt-5.4", 5, .5, 30},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			quote, ok := DefaultCatalog().Quote(RequestQuery{Model: tt.model, At: atDate("2026-07-15"), ServiceTier: "priority"})
			if !ok || quote.Price.InputPerM != tt.input || quote.Price.CacheReadPerM != tt.cached || quote.Price.OutputPerM != tt.output {
				t.Fatalf("priority quote=%+v ok=%v", quote, ok)
			}
		})
	}
}

func TestCatalogClaudeEffectiveDateAndCacheTTL(t *testing.T) {
	c := DefaultCatalog()
	promo, ok := c.Quote(RequestQuery{Model: "claude-sonnet-5", At: atDate("2026-08-31"), ServiceTier: "standard"})
	if !ok || promo.Price.InputPerM != 2 || promo.Price.CacheWrite1hPerM != 4 {
		t.Fatalf("promo quote=%+v ok=%v", promo, ok)
	}
	post, ok := c.Quote(RequestQuery{Model: "claude-sonnet-5", At: atDate("2026-09-01"), ServiceTier: "standard"})
	if !ok || post.Price.InputPerM != 3 || post.Price.CacheWrite1hPerM != 6 {
		t.Fatalf("post-promo quote=%+v ok=%v", post, ok)
	}
	cost, currency := promo.Cost(Usage{CacheWrite1h: 1_000_000})
	if cost != 4 || currency != "USD" {
		t.Fatalf("promo 1h cache cost=(%v,%s), want (4,USD)", cost, currency)
	}
}

func TestCatalogClaudeCurrentExactModels(t *testing.T) {
	tests := []struct {
		model                              string
		input, output, hit, write5, write1 float64
	}{
		{"claude-fable-5", 10, 50, 1, 12.5, 20},
		{"claude-mythos-5", 10, 50, 1, 12.5, 20},
		{"claude-opus-4-7", 5, 25, .5, 6.25, 10},
		{"claude-opus-4-6", 5, 25, .5, 6.25, 10},
		{"claude-opus-4-5", 5, 25, .5, 6.25, 10},
		{"claude-sonnet-4-6", 3, 15, .3, 3.75, 6},
		{"claude-sonnet-4-5", 3, 15, .3, 3.75, 6},
		{"claude-haiku-4-5-20251001", 1, 5, .1, 1.25, 2},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			quote, ok := DefaultCatalog().Quote(RequestQuery{Model: tt.model, At: atDate("2026-07-15"), ServiceTier: "standard"})
			if !ok {
				t.Fatal("official Claude quote missing")
			}
			got := quote.Price.Tier
			if got.InputPerM != tt.input || got.OutputPerM != tt.output || got.CacheReadPerM != tt.hit || got.CacheWrite5mPerM != tt.write5 || got.CacheWrite1hPerM != tt.write1 {
				t.Fatalf("rates=%+v", got)
			}
		})
	}

	fable, ok := DefaultCatalog().Quote(RequestQuery{Model: "claude-fable-5", At: atDate("2026-07-15")})
	if !ok {
		t.Fatal("Fable 5 quote missing")
	}
	cost, currency := fable.Cost(Usage{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheWrite5m: 1_000_000, CacheWrite1h: 1_000_000})
	if cost != 93.5 || currency != "USD" {
		t.Fatalf("Fable 5 full tier cost=(%v,%s), want (93.5,USD)", cost, currency)
	}
}

func TestCatalogNeverInfersFastOrUnknownFamily(t *testing.T) {
	fast, ok := DefaultCatalog().Quote(RequestQuery{Model: "gpt-5.5", At: atDate("2026-07-14"), ServiceTier: "fast"})
	if !ok || fast.Price.InputPerM != 5 {
		t.Fatalf("Fast must use standard API-equivalent price: %+v ok=%v", fast, ok)
	}
	if m, ok := FastCreditMultiplier("gpt-5.5", "standard"); ok || m != 1 {
		t.Fatalf("standard speed inferred Fast: multiplier=%v ok=%v", m, ok)
	}
	if m, ok := FastCreditMultiplier("gpt-5.5", "fast"); !ok || m != 2.5 {
		t.Fatalf("explicit Fast multiplier=%v ok=%v", m, ok)
	}
	if m, ok := FastCreditMultiplier("gpt-5.6-sol", "fast"); ok || m != 1 {
		t.Fatalf("unsupported 5.6 Fast guessed: multiplier=%v ok=%v", m, ok)
	}
	if _, ok := DefaultCatalog().Quote(RequestQuery{Model: "gpt-999", At: atDate("2026-07-14")}); ok {
		t.Fatal("unknown OpenAI family received a guessed quote")
	}
}

func TestCatalogLongContextIsPerRequestNotDailyAggregate(t *testing.T) {
	quote, ok := DefaultCatalog().Quote(RequestQuery{Model: "gpt-5.6-sol", At: atDate("2026-07-14"), ServiceTier: "standard"})
	if !ok {
		t.Fatal("quote missing")
	}
	base, _ := quote.Cost(Usage{Input: 200_000, CacheRead: 72_000, Output: 1_000})
	if math.Abs(base-1.066) > 1e-12 {
		t.Fatalf("272K boundary cost=%v, want base 1.066", base)
	}
	premium, _ := quote.Cost(Usage{Input: 200_001, CacheRead: 72_000, Output: 1_000})
	if math.Abs(premium-2.11701) > 1e-12 {
		t.Fatalf("272K+1 premium cost=%v, want 2.11701", premium)
	}
	var manySmall float64
	for range 100 {
		cost, _ := quote.Cost(Usage{Input: 3_000})
		manySmall += cost
	}
	if math.Abs(manySmall-1.5) > 1e-12 {
		t.Fatalf("small requests were aggregated into long context: %v", manySmall)
	}
}

// 2026-09-30: the GPT-6 cards, pinned the day they were transcribed. The numbers are the
// vendor's own page (short ≤272K / long >272K), not derivable from any neighbor model — a
// regression here is a wrong bill, which is worse than no bill.
func TestCatalogGPT6FamilyExactRates(t *testing.T) {
	c := DefaultCatalog()
	astra, ok := c.Quote(RequestQuery{Model: "gpt-6-astra", At: atDate("2026-09-28"), ServiceTier: "default"})
	if !ok {
		t.Fatal("gpt-6-astra standard quote missing")
	}
	if astra.Price.InputPerM != 10 || astra.Price.CacheReadPerM != 1 || astra.Price.OutputPerM != 50 {
		t.Fatalf("astra short rates wrong: %+v", astra.Price.Tier)
	}
	if astra.Price.Above == nil || astra.Price.Above.InputPerM != 20 || astra.Price.Above.CacheReadPerM != 2 || astra.Price.Above.OutputPerM != 75 {
		t.Fatalf("astra long band wrong: %+v", astra.Price.Above)
	}
	if astra.Price.ContextThreshold != 272_000 {
		t.Fatalf("astra threshold = %d, want 272000", astra.Price.ContextThreshold)
	}
	// No credits schedule is published for these models; absence must stay absence.
	if _, hasCredits := astra.Credits(Usage{Input: 1, Output: 1}); hasCredits {
		t.Fatal("gpt-6-astra must not carry an invented credits schedule")
	}
	sol, ok := c.Quote(RequestQuery{Model: "gpt-6-sol", At: atDate("2026-09-28"), ServiceTier: "default"})
	if !ok || sol.Price.InputPerM != 2 || sol.Price.CacheReadPerM != .2 || sol.Price.OutputPerM != 10 {
		t.Fatalf("gpt-6-sol standard rates wrong: ok=%v %+v", ok, sol.Price.Tier)
	}
	if sol.Price.Above == nil || sol.Price.Above.OutputPerM != 15 {
		t.Fatalf("gpt-6-sol long band wrong: %+v", sol.Price.Above)
	}
	astraFast, ok := c.Quote(RequestQuery{Model: "gpt-6-astra", At: atDate("2026-09-28"), ServiceTier: "priority"})
	if !ok || astraFast.Price.OutputPerM != 100 || astraFast.Price.Above == nil || astraFast.Price.Above.OutputPerM != 150 {
		t.Fatalf("gpt-6-astra priority(Fast) rates wrong: ok=%v %+v above=%+v", ok, astraFast.Price.Tier, astraFast.Price.Above)
	}
	if astra.VerifiedAt != atDate("2026-09-30") {
		t.Fatalf("verifiedAt = %v, want the 2026-09-30 transcription check", astra.VerifiedAt)
	}
}
