package pricing

import (
	"math"
	"testing"
)

func approx(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

// TestHaikuAnchor is the primary ccusage-verified anchor: the exact cost of a real
// claude-haiku-4-5 usage record must compute to 5.2507 USD (within ±0.001).
func TestHaikuAnchor(t *testing.T) {
	p, ok := Lookup("claude-haiku-4-5-20251001")
	if !ok {
		t.Fatalf("Lookup(claude-haiku-4-5-20251001) not found")
	}
	want := ModelPrice{InputPerM: 1, OutputPerM: 5, CacheCreatePerM: 1.25, CacheReadPerM: 0.1, Currency: "USD"}
	if p != want {
		t.Fatalf("haiku price = %+v, want %+v", p, want)
	}

	u := Usage{Input: 1495, Output: 111971, CacheCreate: 1449501, CacheRead: 28774876}
	cost, currency, ok := Cost("claude-haiku-4-5-20251001", u)
	if !ok {
		t.Fatalf("Cost(haiku) ok=false, want true")
	}
	if currency != "USD" {
		t.Fatalf("currency = %q, want USD", currency)
	}
	if !approx(cost, 5.2507, 0.001) {
		t.Fatalf("haiku cost = %.6f, want 5.2507 (±0.001)", cost)
	}
	t.Logf("haiku anchor cost = %.6f USD (want 5.2507)", cost)
}

// TestOpusBase verifies the opus-4-8 BASE price cost.
//
// ccusage reports ~344 USD for this usage record because it applies the 1M-context
// PREMIUM tier (input/output priced higher above 200K context). That premium tier is
// a P2 refinement not modeled here; the BASE price (5/25/6.25/0.5) is correct and
// yields ~326.64 USD. See the comment in pricing.go / table.go.
func TestOpusBase(t *testing.T) {
	u := Usage{Input: 430123, Output: 1431987, CacheCreate: 7706582, CacheRead: 481038345}
	cost, _, ok := Cost("claude-opus-4-8", u)
	if !ok {
		t.Fatalf("Cost(claude-opus-4-8) ok=false, want true")
	}
	if !approx(cost, 326.64, 0.1) {
		t.Fatalf("opus base cost = %.4f, want ~326.64 (±0.1)", cost)
	}
	t.Logf("opus base cost = %.4f USD (ccusage reports ~344 due to 1M-context premium tier)", cost)
}

// TestNormalization verifies provider/region prefixes and context tags are stripped
// so they resolve to the same opus-4-8 price.
func TestNormalization(t *testing.T) {
	want := ModelPrice{InputPerM: 5, OutputPerM: 25, CacheCreatePerM: 6.25, CacheReadPerM: 0.5, Currency: "USD"}
	for _, id := range []string{
		"us.anthropic.claude-opus-4-8",
		"claude-opus-4-8[1m]",
		"anthropic.claude-opus-4-8",
		"claude-opus-4-8",
	} {
		p, ok := Lookup(id)
		if !ok {
			t.Fatalf("Lookup(%q) not found", id)
		}
		if p != want {
			t.Fatalf("Lookup(%q) = %+v, want %+v", id, p, want)
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
		t.Fatalf("claude-opus-4-1 = %+v, want 15/75 (specific beats generic opus)", p)
	}
}

// TestCodexNoCacheCreate verifies OpenAI/codex models have no cache-create tier and
// that cache-create tokens therefore cost nothing.
func TestCodexNoCacheCreate(t *testing.T) {
	p, ok := Lookup("gpt-5.5")
	if !ok {
		t.Fatalf("Lookup(gpt-5.5) not found")
	}
	if p.CacheCreatePerM != 0 {
		t.Fatalf("gpt-5.5 CacheCreatePerM = %v, want 0", p.CacheCreatePerM)
	}

	// CacheCreate tokens must contribute 0 to the cost.
	u := Usage{Input: 1000, Output: 1000, CacheCreate: 1_000_000, CacheRead: 0}
	cost, _, ok := Cost("gpt-5.5", u)
	if !ok {
		t.Fatalf("Cost(gpt-5.5) ok=false")
	}
	want := 1000*5.0/1e6 + 1000*30.0/1e6 // cache-create contributes nothing
	if !approx(cost, want, 1e-9) {
		t.Fatalf("gpt-5.5 cost = %.9f, want %.9f (cache-create must be free)", cost, want)
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

// TestGeminiAndCodexFamilies spot-checks generic family fallbacks and bedrock suffix
// stripping resolve correctly.
func TestGeminiAndCodexFamilies(t *testing.T) {
	cases := []struct {
		id           string
		wantInput    float64
		wantCacheCrt float64
	}{
		{"gemini-2.5-pro-latest", 1.25, 0},
		{"gemini-3-flash", 0.5, 0},
		{"gemini-something-new", 1.25, 0},             // generic gemini fallback
		{"codex-mini-latest", 1.5, 0},                 // specific codex-mini
		{"anthropic.claude-3-5-haiku-v1:0", 0.8, 1.0}, // bedrock version suffix stripped
	}
	for _, c := range cases {
		p, ok := Lookup(c.id)
		if !ok {
			t.Fatalf("Lookup(%q) not found", c.id)
		}
		if p.InputPerM != c.wantInput || p.CacheCreatePerM != c.wantCacheCrt {
			t.Fatalf("Lookup(%q) = %+v, want input=%v cacheCreate=%v",
				c.id, p, c.wantInput, c.wantCacheCrt)
		}
	}
}
