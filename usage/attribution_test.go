package usage

// Attribution axis — "whose bill is this, and may we price it?".
//
// Every pair below was measured on a real machine on 2026-08-22 across 30 days of codex rollouts
// and claude transcripts; none is invented:
//
//	openai                  gpt-5.6-sol   22,725
//	openai                  gpt-5.4            3
//	mimo2codex-kimi-coding  k3             3,115
//	mimo2codex-kimi-coding  gpt-5.6-sol      144   <- the defect: Kimi tokens priced at OpenAI's list
//	(claude, no endpoint)   glm-4.7 / glm-5.3  625

import (
	"testing"
	"time"

	"github.com/brightman-ai/kit/pricing"
	"github.com/brightman-ai/kit/transcript"
)

// withKimiRelay declares what the host knows: this proxy id bills Moonshot. Attribution must come
// from a DECLARATION, never from parsing the string "kimi" out of the id — proxy names are
// user-chosen and a substring rule would be a guess that rots.
func withKimiRelay(t *testing.T) {
	t.Helper()
	withCredentials(t, fakeCredentials{
		VendorMoonshot: {APIKey: "sk-kimi-test", RuntimeProviderIDs: []string{"mimo2codex-kimi-coding"}},
	})
}

func TestAttributeUsage_FourStates(t *testing.T) {
	withKimiRelay(t)

	cases := []struct {
		name      string
		endpoint  string
		model     string
		vendor    string
		basis     string
		priceable bool
	}{
		{
			// Every claude fact and every pre-2026 rollout. Nothing recorded an endpoint, so there
			// is nothing that could contradict the model id.
			name: "no endpoint recorded falls back to the model id",
			endpoint: "", model: "claude-opus-5",
			vendor: pricing.VendorAnthropic.ID, basis: AttributionModel, priceable: true,
		},
		{
			name: "endpoint and model agree", endpoint: "openai", model: "gpt-5.6-sol",
			vendor: pricing.VendorOpenAI.ID, basis: AttributionConfirmed, priceable: true,
		},
		{
			// 3,115 of the 3,259 relayed turns. The relay passes the real id through, so both
			// witnesses say Moonshot and the k3 rate card applies — this must NOT be collateral
			// damage of fixing the 144.
			name: "a relay that passes the real model id through stays priceable",
			endpoint: "mimo2codex-kimi-coding", model: "k3",
			vendor: pricing.VendorMoonshot.ID, basis: AttributionConfirmed, priceable: true,
		},
		{
			// THE defect. Attribution moves to Moonshot AND pricing is refused: `gpt-5.6-sol` does
			// not say which Moonshot model answered, and no rate card exists without one.
			name: "a facade model id loses to the endpoint and is not priced",
			endpoint: "mimo2codex-kimi-coding", model: "gpt-5.6-sol",
			vendor: pricing.VendorMoonshot.ID, basis: AttributionEndpoint, priceable: false,
		},
		{
			// Absence of evidence, not evidence of a facade. Someone who names their own OpenAI
			// relay "local_codex" must not watch every number on the page turn into「—」.
			name: "an unrecognised endpoint leaves the model id at face value",
			endpoint: "local_codex", model: "gpt-5.6-sol",
			vendor: pricing.VendorOpenAI.ID, basis: AttributionUnverified, priceable: true,
		},
		{
			// The mirror image of the codex case: claude records no endpoint and GLM sessions are
			// written honestly as glm-*, so the model id is the trustworthy witness here.
			name: "a GLM session under claude is attributed to Zhipu",
			endpoint: "", model: "glm-5.3",
			vendor: pricing.VendorZhipu.ID, basis: AttributionModel, priceable: true,
		},
		{
			// An endpoint named after its vendor resolves without any declaration — codex's own
			// default is literally "openai", so reading it costs nothing and invents nothing.
			name: "an endpoint named after the vendor resolves on its own",
			endpoint: "deepseek", model: "deepseek-chat",
			vendor: pricing.VendorDeepSeek.ID, basis: AttributionConfirmed, priceable: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := attributeUsage(tc.endpoint, tc.model)
			if got.Vendor.ID != tc.vendor {
				t.Errorf("vendor = %q, want %q", got.Vendor.ID, tc.vendor)
			}
			if got.Basis != tc.basis {
				t.Errorf("basis = %q, want %q", got.Basis, tc.basis)
			}
			if got.Priceable != tc.priceable {
				t.Errorf("priceable = %v, want %v", got.Priceable, tc.priceable)
			}
		})
	}
}

// Without the host's declaration the relay id means nothing to us, so the model id stands. This
// pins the DEPENDENCE deliberately: the fix is configuration-driven by design, and a reader who
// wonders "why is my Kimi row still wrong?" gets the answer from this test.
func TestAttributeUsage_UndeclaredRelayIsUnverifiedNotWrongVendor(t *testing.T) {
	withCredentials(t, fakeCredentials{})
	got := attributeUsage("mimo2codex-kimi-coding", "gpt-5.6-sol")
	if got.Basis != AttributionUnverified {
		t.Fatalf("basis = %q, want %q", got.Basis, AttributionUnverified)
	}
	if got.Vendor.ID != pricing.VendorOpenAI.ID {
		t.Fatalf("vendor = %q, want the model's (unverified, but never invented)", got.Vendor.ID)
	}
}

// A bucket's basis is DERIVED from two booleans rather than merged from labels. One contradicted
// request contradicts the bucket; nothing else can vary, because the endpoint is part of the key.
func TestDerivedBasis(t *testing.T) {
	if got := derivedBasis("", false, false); got != AttributionModel {
		t.Errorf("no endpoint = %q, want %q", got, AttributionModel)
	}
	if got := derivedBasis("local_codex", false, false); got != AttributionUnverified {
		t.Errorf("unresolved endpoint = %q, want %q", got, AttributionUnverified)
	}
	if got := derivedBasis("mimo2codex-kimi-coding", true, false); got != AttributionConfirmed {
		t.Errorf("resolved and agreeing = %q, want %q", got, AttributionConfirmed)
	}
	// The real mix: one endpoint serving both an honest id and a facade one.
	if got := derivedBasis("mimo2codex-kimi-coding", true, true); got != AttributionEndpoint {
		t.Errorf("one contradiction must contradict the bucket, got %q", got)
	}
}

// Both resolution hops, and the absence of either.
func TestResolveEndpointVendor_TwoHopsAndNeither(t *testing.T) {
	withKimiRelay(t)
	// Hop 1: the host DECLARED this id. A name-based rule could never get here — nothing in
	// "mimo2codex-kimi-coding" is parsed; the declaration is what is read.
	if v, ok := resolveEndpointVendor("mimo2codex-kimi-coding"); !ok || v.ID != VendorMoonshot {
		t.Errorf("declared relay = %q ok=%v, want moonshot", v.ID, ok)
	}
	// Hop 2: the id IS a vendor id, undeclared. codex's own default arrives this way.
	if v, ok := resolveEndpointVendor("openai"); !ok || v.ID != VendorOpenAI {
		t.Errorf("self-named endpoint = %q ok=%v, want openai", v.ID, ok)
	}
	// Neither: never a fallback vendor, never a guess.
	if v, ok := resolveEndpointVendor("local_codex"); ok {
		t.Errorf("unknown endpoint resolved to %q — must stay unknown", v.ID)
	}
	if _, ok := resolveEndpointVendor(""); ok {
		t.Error("empty endpoint must not resolve")
	}
}

// Attribution is an INPUT TO PRICING, and prices are cached per ended day under "a finished day
// cannot change". Declaring a relay changes what those days say, so it must change the cache key —
// otherwise the correction reaches only the days that happen to be re-read.
func TestAttributionRevision_ChangesWithTheDeclarations(t *testing.T) {
	withCredentials(t, fakeCredentials{})
	none := AttributionRevision()
	if none != "none" {
		t.Errorf("no declarations = %q, want a stable %q", none, "none")
	}

	withCredentials(t, fakeCredentials{
		VendorMoonshot: {APIKey: "k", RuntimeProviderIDs: []string{"mimo2codex-kimi-coding"}},
	})
	declared := AttributionRevision()
	if declared == none {
		t.Fatal("declaring a relay left the fingerprint unchanged — day caches would never reprice")
	}
	if again := AttributionRevision(); again != declared {
		t.Errorf("fingerprint is unstable: %q then %q", declared, again)
	}

	// A different mapping for the same vendor is a different meaning for the same past facts.
	withCredentials(t, fakeCredentials{
		VendorMoonshot: {APIKey: "k", RuntimeProviderIDs: []string{"some-other-relay"}},
	})
	if remapped := AttributionRevision(); remapped == declared {
		t.Error("remapping an endpoint left the fingerprint unchanged")
	}

	// The API KEY is not part of it: rotating a secret changes no attribution and must not throw
	// away every cached day.
	withCredentials(t, fakeCredentials{
		VendorMoonshot: {APIKey: "rotated", RuntimeProviderIDs: []string{"some-other-relay"}},
	})
	if rotated := AttributionRevision(); rotated == declared {
		// (still the remapped value, which is what we want — just not the original)
		t.Error("unexpected collision with the pre-remap fingerprint")
	}
}

// A row's unit price is the reader's only way to check its total. Publishing OpenAI's card under a
// Moonshot heading hands them a check that confirms the wrong thing — and the money on that row was
// already refused for exactly this reason.
func TestPublishedRatesFor_SuppressesAForeignVendorsCard(t *testing.T) {
	if cards := publishedRatesFor(pricing.VendorMoonshot, "gpt-5.6-sol"); len(cards) != 0 {
		t.Errorf("published %d OpenAI cards on a Moonshot row", len(cards))
	}
	if cards := publishedRatesFor(pricing.VendorOpenAI, "gpt-5.6-sol"); len(cards) == 0 {
		t.Error("suppressed the row's OWN vendor card — the check must survive")
	}
	// An unknown vendor still shows whatever the id resolves to: there is no mismatch to detect,
	// and the raw id beside it is the only handle the reader has.
	if cards := publishedRatesFor(pricing.VendorUnknown, "gpt-5.6-sol"); len(cards) == 0 {
		t.Error("an unknown-vendor row lost its only price context")
	}
}

// The agent scanner puts a provider KEY in the same field (whale-agent facts). It is a plain
// lowercase vendor name, so it carries endpoint semantics and the cross-check applies to it
// unchanged — an agent account named "anthropic" answering with a GLM model is a real contradiction,
// not a false alarm. Pinned because the field is shared and its contract is easy to drift.
func TestAttributeUsage_AgentProviderKeyIsTreatedAsAnEndpoint(t *testing.T) {
	withCredentials(t, fakeCredentials{})
	agreeing := attributeUsage("anthropic", "claude-opus-5")
	if agreeing.Basis != AttributionConfirmed || !agreeing.Priceable {
		t.Errorf("agent fact with a matching provider key = %+v, want confirmed and priceable", agreeing)
	}
	// A composite or bespoke key resolves to nothing and therefore accuses nobody.
	bespoke := attributeUsage("whale-agent", "claude-opus-5")
	if bespoke.Basis != AttributionUnverified || !bespoke.Priceable {
		t.Errorf("bespoke provider key = %+v, want unverified and still priced", bespoke)
	}
	if bespoke.Vendor.ID != pricing.VendorAnthropic.ID {
		t.Errorf("vendor = %q, want the model's", bespoke.Vendor.ID)
	}
}

// The refusal lives in ProjectRequestCost so it protects EVERY caller (the usage report, the agent
// report, anything added later), not one report that remembered to ask.
func TestProjectRequestCost_RefusesAFacadeModelID(t *testing.T) {
	withKimiRelay(t)
	fact := transcript.ModelRequestUsage{
		ID: "codex:test:1", Runtime: "codex", Provider: "mimo2codex-kimi-coding",
		Model: "gpt-5.6-sol", ServiceTier: "default", At: time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC),
		InputTokens: 1000, OutputTokens: 500,
	}
	got := ProjectRequestCost(fact)
	if got.Complete || got.APIEquivalent != nil {
		t.Fatalf("priced a facade model id: %+v", got)
	}
	if !hasDiagnostic(got.Diagnostics, "model_id_contradicted_by_endpoint") {
		t.Fatalf("diagnostics = %v, want the contradiction named", got.Diagnostics)
	}

	// Same relay, honest model id: still priced. The refusal must be surgical.
	fact.Model = "k3"
	if honest := ProjectRequestCost(fact); hasDiagnostic(honest.Diagnostics, "model_id_contradicted_by_endpoint") {
		t.Fatalf("refused an honest relayed id: %+v", honest)
	}
}

func hasDiagnostic(diagnostics []string, want string) bool {
	for _, d := range diagnostics {
		if d == want {
			return true
		}
	}
	return false
}

// End-to-end over the LIVE report path, with the exact corpus shape measured on this machine.
func TestAggregateDaily_SeparatesRelayedTrafficFromTheAccountItBorrowedFrom(t *testing.T) {
	withKimiRelay(t)
	at := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	fact := func(id, endpoint, model string, in, out int64) transcript.ModelRequestUsage {
		return transcript.ModelRequestUsage{
			ID: id, Runtime: "codex", Provider: endpoint, Model: model,
			ServiceTier: "default", At: at, InputTokens: in, OutputTokens: out,
		}
	}
	report := BuildRequestReport(Window7d, "UTC", at, []transcript.ModelRequestUsage{
		fact("a", "openai", "gpt-5.6-sol", 1000, 500),
		fact("b", "mimo2codex-kimi-coding", "k3", 2000, 700),
		fact("c", "mimo2codex-kimi-coding", "gpt-5.6-sol", 4000, 900),
	})

	byVendor := map[string]ProviderRow{}
	for _, row := range report.Providers {
		byVendor[row.Vendor] = row
	}
	if len(report.Providers) != 2 {
		t.Fatalf("providers = %d, want one per vendor: %+v", len(report.Providers), report.Providers)
	}

	openai, ok := byVendor[pricing.VendorOpenAI.ID]
	if !ok {
		t.Fatal("the direct OpenAI row disappeared")
	}
	if openai.TotalTokens != 1500 {
		t.Errorf("openai tokens = %d, want only the direct traffic (1500)", openai.TotalTokens)
	}
	if openai.RuntimeProvider != "openai" || openai.AttributionBasis != AttributionConfirmed {
		t.Errorf("openai row = %+v, want endpoint openai / confirmed", openai)
	}

	kimi, ok := byVendor[pricing.VendorMoonshot.ID]
	if !ok {
		t.Fatal("relayed traffic was not attributed to Moonshot")
	}
	if kimi.TotalTokens != 7600 {
		t.Errorf("moonshot tokens = %d, want both relayed turns (7600)", kimi.TotalTokens)
	}
	if kimi.RuntimeProvider != "mimo2codex-kimi-coding" {
		t.Errorf("endpoint = %q, want it published so the user can see which traffic this is", kimi.RuntimeProvider)
	}
	// The row mixes an honest id with a facade one, so it reports the weaker basis and its money
	// is incomplete — which is what puts「≈」on it instead of a confident wrong number.
	if kimi.AttributionBasis != AttributionEndpoint {
		t.Errorf("basis = %q, want %q (the bound, not the best case)", kimi.AttributionBasis, AttributionEndpoint)
	}
	if kimi.PricedRequests >= kimi.Requests {
		t.Errorf("priced %d/%d — the facade turn must not be priced", kimi.PricedRequests, kimi.Requests)
	}

	// Splitting rows must never lose tokens: the partition is finer, not lossy.
	var summed int64
	for _, row := range report.Providers {
		summed += row.TotalTokens
	}
	if summed != report.Summary.TotalTokens {
		t.Errorf("rows sum to %d but summary says %d", summed, report.Summary.TotalTokens)
	}
}

// The same corpus through the FALLBACK path (model bundles). Two report paths disagreeing about
// one corpus is exactly the drift this change exists to remove, so the invariant is pinned on both.
func TestBuildFromModelBundles_UsesTheSameAttributionRule(t *testing.T) {
	withKimiRelay(t)
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	report := buildFromModelBundles(Window7d, now, 7, today, today, []ModelTokens{
		{Date: today, Provider: "openai", Model: "gpt-5.6-sol", InputTokens: 1000, OutputTokens: 500},
		{Date: today, Provider: "mimo2codex-kimi-coding", Model: "gpt-5.6-sol", InputTokens: 4000, OutputTokens: 900},
	})
	byVendor := map[string]ProviderRow{}
	for _, row := range report.Providers {
		byVendor[row.Vendor] = row
	}
	if got := byVendor[pricing.VendorOpenAI.ID].TotalTokens; got != 1500 {
		t.Errorf("openai tokens = %d, want 1500 — the relayed bundle must not land here", got)
	}
	kimi := byVendor[pricing.VendorMoonshot.ID]
	if kimi.TotalTokens != 4900 {
		t.Errorf("moonshot tokens = %d, want 4900", kimi.TotalTokens)
	}
	if kimi.Cost != nil {
		t.Errorf("cost = %v, want none: a facade id cannot key a rate card", *kimi.Cost)
	}
	if report.Summary.CostComplete {
		t.Error("CostComplete must be false — this window's money knowingly under-counts")
	}
}
