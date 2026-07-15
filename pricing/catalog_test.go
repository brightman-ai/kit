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
