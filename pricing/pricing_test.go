package pricing

import (
	"math"
	"testing"
)

func approx(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

// TestHaikuAnchor is the primary per-request anchor: one real claude-haiku-4-5
// request whose cache-write is a 1-hour write. The 1h tokens MUST be priced at 2.0
// (NOT the 5m rate of 1.25).
//
//	in   8486  × 1.0   = 0.008486
//	out   117  × 5.0   = 0.000585
//	cr  15840  × 0.1   = 0.001584
//	cw1h 16644 × 2.0   = 0.033288  ← uses 2.0, not 1.25
//	                     ─────────
//	                     0.043943 USD
func TestHaikuAnchor(t *testing.T) {
	p, ok := Lookup("claude-haiku-4-5-20251001")
	if !ok {
		t.Fatalf("Lookup(claude-haiku-4-5-20251001) not found")
	}
	wantTier := Tier{InputPerM: 1, OutputPerM: 5, CacheReadPerM: 0.1, CacheWrite5mPerM: 1.25, CacheWrite1hPerM: 2}
	if p.Tier != wantTier {
		t.Fatalf("haiku tier = %+v, want %+v", p.Tier, wantTier)
	}
	if p.ContextThreshold != 0 || p.Above != nil {
		t.Fatalf("haiku must have NO context tier, got threshold=%d above=%v", p.ContextThreshold, p.Above)
	}

	u := Usage{Input: 8486, Output: 117, CacheRead: 15840, CacheWrite5m: 0, CacheWrite1h: 16644}
	cost, currency, ok := Cost("claude-haiku-4-5-20251001", u)
	if !ok {
		t.Fatalf("Cost(haiku) ok=false, want true")
	}
	if currency != "USD" {
		t.Fatalf("currency = %q, want USD", currency)
	}
	want := 8486*1.0/1e6 + 117*5.0/1e6 + 15840*0.1/1e6 + 16644*2.0/1e6 // = 0.043943
	if !approx(cost, want, 1e-9) {
		t.Fatalf("haiku request cost = %.6f, want %.6f (cw1h must use 2.0)", cost, want)
	}
	if !approx(cost, 0.043943, 1e-6) {
		t.Fatalf("haiku request cost = %.6f, want 0.043943", cost)
	}
	t.Logf("haiku request cost = %.6f USD (cw1h @ 2.0)", cost)
}

// TestCacheWrite1hVs5m verifies that 1h and 5m cache-write tokens are billed at
// DIFFERENT rates: identical token counts placed in CacheWrite5m vs CacheWrite1h
// yield different costs (opus: 6.25/M for 5m, 10/M for 1h).
func TestCacheWrite1hVs5m(t *testing.T) {
	const n = 1_000_000
	cost5m, _, ok := Cost("claude-opus-4-8", Usage{CacheWrite5m: n})
	if !ok {
		t.Fatalf("Cost(opus, 5m) ok=false")
	}
	cost1h, _, ok := Cost("claude-opus-4-8", Usage{CacheWrite1h: n})
	if !ok {
		t.Fatalf("Cost(opus, 1h) ok=false")
	}
	if !approx(cost5m, 6.25, 1e-9) {
		t.Fatalf("opus 1M 5m-write = %.6f, want 6.25", cost5m)
	}
	if !approx(cost1h, 10.0, 1e-9) {
		t.Fatalf("opus 1M 1h-write = %.6f, want 10.0", cost1h)
	}
	if approx(cost5m, cost1h, 1e-9) {
		t.Fatalf("5m and 1h must differ: both = %.6f", cost5m)
	}
	t.Logf("opus 1M cache-write: 5m=%.4f 1h=%.4f USD", cost5m, cost1h)
}

// TestContextTier verifies the long-context premium: gpt-5.5 just OVER the 272000
// threshold bills at the Above tier (input 10/M), just UNDER bills at base (5/M).
func TestContextTier(t *testing.T) {
	// Context = Input + CacheRead + CacheWrite5m + CacheWrite1h.
	over := Usage{Input: 272001}  // context 272001 > 272000 → Above
	under := Usage{Input: 272000} // context 272000 == threshold → base (strictly greater wins)

	costOver, _, ok := Cost("gpt-5.5", over)
	if !ok {
		t.Fatalf("Cost(gpt-5.5, over) ok=false")
	}
	costUnder, _, ok := Cost("gpt-5.5", under)
	if !ok {
		t.Fatalf("Cost(gpt-5.5, under) ok=false")
	}

	wantOver := 272001 * 10.0 / 1e6 // Above input rate 10/M
	wantUnder := 272000 * 5.0 / 1e6 // base input rate 5/M
	if !approx(costOver, wantOver, 1e-9) {
		t.Fatalf("gpt-5.5 over-threshold = %.6f, want %.6f (Above @10/M)", costOver, wantOver)
	}
	if !approx(costUnder, wantUnder, 1e-9) {
		t.Fatalf("gpt-5.5 under-threshold = %.6f, want %.6f (base @5/M)", costUnder, wantUnder)
	}
	if costOver <= costUnder {
		t.Fatalf("over (%.6f) must exceed under (%.6f) — premium tier not applied", costOver, costUnder)
	}
	t.Logf("gpt-5.5 tier: under(272000)=%.4f base, over(272001)=%.4f premium", costUnder, costOver)
}

// TestAnthropicNoContextTier verifies an Anthropic opus request with a giant 2M
// context still bills at BASE prices (ContextThreshold == 0 → no premium).
func TestAnthropicNoContextTier(t *testing.T) {
	p, _ := Lookup("claude-opus-4-8")
	if p.ContextThreshold != 0 || p.Above != nil {
		t.Fatalf("opus-4-8 must have no context tier, got threshold=%d above=%v", p.ContextThreshold, p.Above)
	}
	u := Usage{Input: 2_000_000} // 2M-token context
	cost, _, ok := Cost("claude-opus-4-8", u)
	if !ok {
		t.Fatalf("Cost(opus 2M) ok=false")
	}
	want := 2_000_000 * 5.0 / 1e6 // base input rate, NOT doubled
	if !approx(cost, want, 1e-9) {
		t.Fatalf("opus 2M context = %.4f, want %.4f (base prices, no premium)", cost, want)
	}
	t.Logf("opus 2M context = %.4f USD (base, no premium tier)", cost)
}

// TestNormalization verifies provider/region prefixes and context tags are stripped
// so they resolve to the same opus-4-8 price.
func TestNormalization(t *testing.T) {
	wantTier := Tier{InputPerM: 5, OutputPerM: 25, CacheReadPerM: 0.5, CacheWrite5mPerM: 6.25, CacheWrite1hPerM: 10}
	for _, id := range []string{
		"us.anthropic.claude-opus-4-8[1m]",
		"claude-opus-4-8[1m]",
		"anthropic.claude-opus-4-8",
		"claude-opus-4-8",
	} {
		p, ok := Lookup(id)
		if !ok {
			t.Fatalf("Lookup(%q) not found", id)
		}
		if p.Tier != wantTier {
			t.Fatalf("Lookup(%q) tier = %+v, want %+v", id, p.Tier, wantTier)
		}
	}
}

// TestLongestKeyFirst verifies a specific id beats the generic family fallback.
func TestLongestKeyFirst(t *testing.T) {
	p, ok := Lookup("claude-opus-4-1")
	if !ok {
		t.Fatalf("Lookup(claude-opus-4-1) not found")
	}
	// opus-4-1 is the legacy 15/75 tier, NOT the generic opus 5/25 fallback.
	if p.InputPerM != 15 || p.OutputPerM != 75 {
		t.Fatalf("claude-opus-4-1 = %+v, want 15/75 (specific beats generic opus)", p.Tier)
	}
}

// TestCodexNoCacheWrite verifies OpenAI/codex models have no cache-write tier and
// that cache-write tokens therefore cost nothing.
func TestCodexNoCacheWrite(t *testing.T) {
	p, ok := Lookup("gpt-5.5")
	if !ok {
		t.Fatalf("Lookup(gpt-5.5) not found")
	}
	if p.CacheWrite5mPerM != 0 || p.CacheWrite1hPerM != 0 {
		t.Fatalf("gpt-5.5 cache-write = %v/%v, want 0/0", p.CacheWrite5mPerM, p.CacheWrite1hPerM)
	}

	// Cache-write tokens (both TTLs) must contribute 0 to the cost.
	u := Usage{Input: 1000, Output: 1000, CacheWrite5m: 1_000_000, CacheWrite1h: 1_000_000}
	cost, _, ok := Cost("gpt-5.5", u)
	if !ok {
		t.Fatalf("Cost(gpt-5.5) ok=false")
	}
	// Note: context = 1000 + 1M + 1M = 2_001_000 > 272000 → Above tier (input 10/M).
	want := 1000*10.0/1e6 + 1000*45.0/1e6 // Above in/out; cache-write free
	if !approx(cost, want, 1e-9) {
		t.Fatalf("gpt-5.5 cost = %.9f, want %.9f (cache-write must be free)", cost, want)
	}
}

// TestUnknownModel verifies unknown models return ok=false and never guess a price.
func TestUnknownModel(t *testing.T) {
	if _, ok := Lookup("totally-unknown-model"); ok {
		t.Fatalf("Lookup(totally-unknown-model) ok=true, want false")
	}
	cost, currency, ok := Cost("totally-unknown-model", Usage{Input: 1000})
	if ok {
		t.Fatalf("Cost(unknown) ok=true, want false")
	}
	if cost != 0 || currency != "" {
		t.Fatalf("Cost(unknown) = (%v, %q), want (0, \"\")", cost, currency)
	}
}

// TestGeminiAndCodexFamilies spot-checks generic family fallbacks, bedrock suffix
// stripping, and the Gemini long-context tier resolve correctly.
func TestGeminiAndCodexFamilies(t *testing.T) {
	cases := []struct {
		id          string
		wantInput   float64
		wantCW5m    float64
		wantThresh  int
		wantHasAbov bool
	}{
		{"gemini-2.5-pro-latest", 1.25, 0, 200000, true},
		{"gemini-3-flash", 0.5, 0, 0, false},
		{"gemini-something-new", 1.25, 0, 0, false},             // generic gemini fallback
		{"codex-mini-latest", 1.5, 0, 0, false},                 // specific codex-mini
		{"anthropic.claude-3-5-haiku-v1:0", 0.8, 1.0, 0, false}, // bedrock suffix stripped; cw5m=1.0
	}
	for _, c := range cases {
		p, ok := Lookup(c.id)
		if !ok {
			t.Fatalf("Lookup(%q) not found", c.id)
		}
		if p.InputPerM != c.wantInput || p.CacheWrite5mPerM != c.wantCW5m {
			t.Fatalf("Lookup(%q) = %+v, want input=%v cw5m=%v", c.id, p.Tier, c.wantInput, c.wantCW5m)
		}
		if p.ContextThreshold != c.wantThresh || (p.Above != nil) != c.wantHasAbov {
			t.Fatalf("Lookup(%q) threshold=%d above=%v, want threshold=%d hasAbove=%v",
				c.id, p.ContextThreshold, p.Above, c.wantThresh, c.wantHasAbov)
		}
	}
}

// TestGeminiContextTier verifies the Gemini 200000 threshold doubles prices.
func TestGeminiContextTier(t *testing.T) {
	over, _, ok := Cost("gemini-2.5-pro", Usage{Input: 200001})
	if !ok {
		t.Fatalf("Cost(gemini-2.5-pro over) ok=false")
	}
	under, _, ok := Cost("gemini-2.5-pro", Usage{Input: 200000})
	if !ok {
		t.Fatalf("Cost(gemini-2.5-pro under) ok=false")
	}
	if !approx(over, 200001*2.5/1e6, 1e-9) {
		t.Fatalf("gemini-2.5-pro over = %.6f, want Above @2.5/M", over)
	}
	if !approx(under, 200000*1.25/1e6, 1e-9) {
		t.Fatalf("gemini-2.5-pro under = %.6f, want base @1.25/M", under)
	}
}
