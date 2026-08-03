package pricing

import (
	"testing"
	"time"
)

// Third-party prices pinned to the digit, the same way the ccusage anchors are. A price is a
// claim about someone else's invoice; it does not get to change because a refactor swept through.
func TestThirdPartyPricesArePinned(t *testing.T) {
	at := mustDate("2026-08-01")
	cases := []struct {
		model              string
		in, out, cacheRead float64
		currency           string
	}{
		// Moonshot: $3.00 cache-miss in / $15.00 out / $0.30 cache-hit per 1M (ex-tax).
		{"kimi-k3", 3, 15, 0.30, "USD"},
		{"k3", 3, 15, 0.30, "USD"},
		// DeepSeek: agrees digit-for-digit with LiteLLM's snapshot.
		{"deepseek-v4-flash", 0.14, 0.28, 0.0028, "USD"},
		{"deepseek-v4-pro", 0.435, 0.87, 0.003625, "USD"},
	}
	for _, tc := range cases {
		price, ok := LookupAt(tc.model, at, "standard")
		if !ok {
			t.Errorf("%s: no price rule", tc.model)
			continue
		}
		if price.InputPerM != tc.in || price.OutputPerM != tc.out || price.CacheReadPerM != tc.cacheRead {
			t.Errorf("%s: got in=%v out=%v cacheRead=%v, want %v/%v/%v",
				tc.model, price.InputPerM, price.OutputPerM, price.CacheReadPerM, tc.in, tc.out, tc.cacheRead)
		}
		if price.Currency != tc.currency {
			t.Errorf("%s: currency = %q, want %q", tc.model, price.Currency, tc.currency)
		}
		// No cache-write charge is published by either vendor. A non-zero here would invent one.
		if price.CacheWrite5mPerM != 0 || price.CacheWrite1hPerM != 0 {
			t.Errorf("%s: invented a cache-write tier (%v/%v)", tc.model, price.CacheWrite5mPerM, price.CacheWrite1hPerM)
		}
		if price.ContextThreshold != 0 || price.Above != nil {
			t.Errorf("%s: invented a long-context tier", tc.model)
		}
	}
}

// Every quote must be able to state its own age. A price table cannot notice that a vendor changed
// its prices, so the only defence a reader has is knowing how old the number is.
func TestEveryQuoteCarriesItsVerificationDate(t *testing.T) {
	at := mustDate("2026-08-01")
	for _, rule := range buildCatalogRules() {
		quote, ok := DefaultCatalog().Quote(RequestQuery{Model: rule.models[0], At: at, ServiceTier: rule.serviceTier})
		if !ok {
			continue // rule not effective at `at` (e.g. a promo that has ended) — not this test's business
		}
		if quote.VerifiedAt.IsZero() {
			t.Errorf("rule %s produced a quote with no VerifiedAt", rule.id)
		}
		if quote.SourceURL == "" {
			t.Errorf("rule %s produced a quote with no SourceURL", rule.id)
		}
	}
}

// The third-party rules were read off the vendors' pages on a specific day; that day is what the
// UI shows. Guard the wiring, not the constant's exact value.
func TestThirdPartyQuoteVerifiedRecently(t *testing.T) {
	quote, ok := DefaultCatalog().Quote(RequestQuery{Model: "k3", At: mustDate("2026-08-01"), ServiceTier: "default"})
	if !ok {
		t.Fatal("k3 has no quote")
	}
	if !quote.VerifiedAt.After(catalogSnapshotDate) {
		t.Errorf("k3 VerifiedAt = %s, expected to be newer than the bulk snapshot %s",
			quote.VerifiedAt.Format(time.DateOnly), catalogSnapshotDate.Format(time.DateOnly))
	}
}
