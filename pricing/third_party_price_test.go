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
		// Moonshot, DOMESTIC list (platform.moonshot.cn): ¥20.00 cache-miss in /
		// ¥100.00 out / ¥2.00 cache-hit per 1M. The international list ($3.00/$15.00/
		// $0.30 on platform.kimi.ai) is the same price at Moonshot's own 6.67
		// conversion and rides along in alsoPublishedAs — see
		// TestKimiPublishesBothListsAndNeitherIsAnFXConversion.
		{"kimi-k3", 20, 100, 2, "CNY"},
		{"k3", 20, 100, 2, "CNY"},
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

// Moonshot sells the same model on two platforms at two list prices. Both are published numbers,
// and the gap between them (6.67×) is exactly the size of error that looks plausible on screen —
// which is why the surface shows both instead of picking one and hoping.
//
// The invariant that matters: the second card is a SECOND PUBLISHED PRICE, never a converted one.
// If anyone ever "helpfully" derives it with an exchange rate, this test is what should stop them —
// an FX rate is a third fact, with its own source and its own staleness, and this package holds no
// such thing.
func TestKimiPublishesBothListsAndNeitherIsAnFXConversion(t *testing.T) {
	cards := PublishedRates("k3")
	if len(cards) != 2 {
		t.Fatalf("k3 has %d rate cards, want 2 (domestic + international)", len(cards))
	}
	primary, alt := cards[0], cards[1]
	if !primary.Primary || alt.Primary {
		t.Errorf("exactly the first card must be marked primary: %+v / %+v", primary, alt)
	}
	if primary.Currency != "CNY" || primary.InputPerM != 20 || primary.OutputPerM != 100 || primary.CacheReadPerM != 2 {
		t.Errorf("primary card = %+v, want the domestic CNY list ¥20/¥100/¥2", primary)
	}
	if alt.Currency != "USD" || alt.InputPerM != 3 || alt.OutputPerM != 15 || alt.CacheReadPerM != 0.30 {
		t.Errorf("alternate card = %+v, want the international USD list $3/$15/$0.30", alt)
	}
	// Each card names the page it was read from, so neither can be mistaken for a derived number.
	for _, c := range cards {
		if c.SourceURL == "" {
			t.Errorf("%s card has no source page", c.Currency)
		}
	}
	if primary.SourceURL == alt.SourceURL {
		t.Error("both cards cite the same page — one of them is not actually a separate publication")
	}
}

// A model with one published list reports one card, not a padded pair.
func TestPublishedRates_SingleListStaysSingle(t *testing.T) {
	cards := PublishedRates("claude-opus-5")
	if len(cards) != 1 || cards[0].Currency != "USD" || !cards[0].Primary {
		t.Errorf("claude-opus-5 rate cards = %+v, want exactly one primary USD card", cards)
	}
	if cards := PublishedRates("zzz-unknown-1"); len(cards) != 0 {
		t.Errorf("an unpriced model reported rate cards: %+v", cards)
	}
}

// GLM, read off open.bigmodel.cn/pricing. Pinned like the ccusage anchors: a price is a claim
// about someone else's invoice and does not get to drift because a refactor swept through.
func TestGLMPricesArePinned(t *testing.T) {
	at := mustDate("2026-08-01")
	for _, tc := range []struct {
		model              string
		in, out, cacheRead float64
	}{
		{"glm-5.2", 8, 28, 2},
		{"glm-5.1", 6, 24, 1.3},
	} {
		price, ok := LookupAt(tc.model, at, "standard")
		if !ok {
			t.Errorf("%s: no price", tc.model)
			continue
		}
		if price.InputPerM != tc.in || price.OutputPerM != tc.out || price.CacheReadPerM != tc.cacheRead {
			t.Errorf("%s: got %v/%v/%v, want %v/%v/%v", tc.model,
				price.InputPerM, price.OutputPerM, price.CacheReadPerM, tc.in, tc.out, tc.cacheRead)
		}
		if price.Currency != "CNY" {
			t.Errorf("%s: currency %q, want CNY", tc.model, price.Currency)
		}
	}
}

// The family fallbacks are gone on purpose. An unknown GLM or Qwen must stay unpriced rather than
// inherit a neighbour's rate — the same rule OpenAI and Anthropic have always followed here, and
// the reason the old generic "glm" key was wrong by 5.6× on output without anyone noticing.
func TestNoFamilyFallbackForChineseVendors(t *testing.T) {
	for _, model := range []string{"glm-9-imaginary", "qwen-max", "qwen3.8-max", "qwen-imaginary"} {
		if price, ok := Lookup(model); ok {
			t.Errorf("%s was priced at %+v; an unverified model must stay unpriced", model, price)
		}
		// Still attributable, though: the money shows up under its vendor with「无价表」rather
		// than vanishing into the unknown bucket.
		if v := VendorForModel(model); !v.Known() {
			t.Errorf("%s lost its vendor as well as its price", model)
		}
	}
}
