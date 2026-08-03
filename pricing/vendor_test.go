package pricing

import "testing"

// The real ids observed on a live machine — claude transcripts, codex rollouts, and the shapes
// LiteLLM/Bedrock stack on top of them. Fixtures invented from the doc comment would only prove
// the doc comment.
func TestVendorForModel_RealWorldIDs(t *testing.T) {
	cases := []struct {
		model string
		want  Vendor
	}{
		{"claude-opus-5", VendorAnthropic},
		{"claude-opus-4-8", VendorAnthropic},
		{"claude-sonnet-5", VendorAnthropic},
		{"claude-fable-5", VendorAnthropic},
		{"claude-mythos-5", VendorAnthropic},
		{"us.anthropic.claude-3-5-haiku-v1:0", VendorAnthropic}, // stacked region+provider prefix
		{"claude-opus-5[1m]", VendorAnthropic},                  // context tag
		{"gpt-5.6-sol", VendorOpenAI},
		{"gpt-5.5", VendorOpenAI},
		{"codex-mini", VendorOpenAI},
		{"o3", VendorOpenAI},
		{"openai/gpt-5.4", VendorOpenAI},
		{"gemini-3-pro", VendorGoogle},
		{"k3", VendorMoonshot},      // the bare id codex actually writes
		{"kimi-k3", VendorMoonshot}, // the canonical id
		{"kimi-k2.5", VendorMoonshot},
		{"deepseek-v4-flash", VendorDeepSeek},
		{"glm-4-flash", VendorZhipu},
		{"qwen-max", VendorAlibaba},

		// Unknown must stay unknown. "<synthetic>" is claude's own placeholder row and appears in
		// every real transcript; the rest are the shapes a wrong matcher would swallow.
		{"<synthetic>", VendorUnknown},
		{"", VendorUnknown},
		{"zzz-unknown-1", VendorUnknown},
		{"my-private-gpt", VendorUnknown},     // substring match would have said OpenAI
		{"llama-3-70b-claude", VendorUnknown}, // ...and this would have said Anthropic
	}
	for _, tc := range cases {
		if got := VendorForModel(tc.model); got != tc.want {
			t.Errorf("VendorForModel(%q) = %+v, want %+v", tc.model, got, tc.want)
		}
	}
}

// The invariant that keeps the two tables in this package from drifting: anything we are willing
// to charge for must also be attributable. A priced model with no vendor is money landing in the
// "unknown" bucket — worse than unpriced, because it looks answered.
func TestEveryPricedModelHasAVendor(t *testing.T) {
	for _, e := range priceTable {
		if v := VendorForModel(e.key); !v.Known() {
			t.Errorf("priceTable key %q has a price but no vendor — add it to vendorTable", e.key)
		}
	}
	for _, rule := range buildCatalogRules() {
		for _, model := range rule.models {
			if v := VendorForModel(model); !v.Known() {
				t.Errorf("catalog rule %s prices %q but no vendor resolves it", rule.id, model)
			}
		}
	}
}

// A vendor's currency is a property of the vendor, not of the individual model. Two models of one
// vendor priced in different currencies would make "this vendor's total" undefined — which is the
// exact defect the vendor axis exists to fix, so it must not be reintroduced inside a vendor.
func TestOneCurrencyPerVendor(t *testing.T) {
	seen := map[string]string{} // vendor id → currency
	record := func(where, model, currency string) {
		v := VendorForModel(model)
		if !v.Known() || currency == "" {
			return
		}
		if prior, ok := seen[v.ID]; ok && prior != currency {
			t.Errorf("%s: vendor %s priced in both %s and %s (via %q)", where, v.ID, prior, currency, model)
			return
		}
		seen[v.ID] = currency
	}
	for _, e := range priceTable {
		record("priceTable", e.key, e.price.Currency)
	}
	for _, rule := range buildCatalogRules() {
		for _, model := range rule.models {
			record("catalog "+rule.id, model, rule.price.Currency)
		}
	}
}
