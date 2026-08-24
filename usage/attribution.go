// Package usage — attribution.go: WHICH VENDOR does this request owe, and may we price it?
//
// # Two witnesses, not one
//
// A usage fact carries two independent statements about who served it:
//
//	the MODEL ID   — what the vendor echoed back on the request.
//	the ENDPOINT   — what the runtime says it dialled (codex's session_meta.model_provider /
//	                 thread_settings_applied.model_provider_id). claude records none at all.
//
// Until now only the model id was used, and pricing/vendor.go documents why: an endpoint name is
// a string the USER invents in ~/.codex/config.toml ("local_codex", "mimo2codex-kimi-coding"), so
// it is routing configuration rather than identity. That reasoning stands and is NOT overturned
// here. The endpoint is used as a FALSIFIER: it can prove a model id is decoration even when it
// cannot, by itself, name a vendor.
//
// It has to be, because the model id is not always the vendor's. Measured on this machine over 30
// days of codex rollouts:
//
//	openai                  gpt-5.6-sol   22,725   model id honest
//	mimo2codex-kimi-coding  k3             3,115   model id honest — a relay that passes the name through
//	mimo2codex-kimi-coding  gpt-5.6-sol      144   FACADE: Kimi's traffic wearing OpenAI's id
//
// Those 144 were billed to OpenAI AT OPENAI'S RATES. Mislabelling a row costs a heading, which a
// reader can see and question; pricing it off another vendor's rate card invents money, which
// nobody can see. This file exists to keep the second one from happening.
//
// # claude is the mirror image
//
// Claude Code sessions record `glm-4.7` / `glm-5.3` honestly (625 rows here, never disguised as
// claude-*), so on that runtime the model id IS authoritative — the opposite of codex. Neither
// fact is a special case of the other; both are stated where they are used.
package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/brightman-ai/kit/pricing"
)

// Attribution bases — how a row's vendor was decided. Published on ProviderRow so a reader can ask
// "how sure are you?" of the row itself rather than of the codebase.
const (
	// AttributionModel — the model id alone. No endpoint was recorded, so nothing can contradict
	// it. This is every claude row and every pre-2026 codex rollout.
	AttributionModel = "model"
	// AttributionConfirmed — endpoint and model id agree. The strongest thing we can say.
	AttributionConfirmed = "confirmed"
	// AttributionEndpoint — they DISAGREE, so the endpoint wins and the model id is treated as
	// decoration. Not priced: see priceable below.
	AttributionEndpoint = "endpoint"
	// AttributionUnverified — an endpoint was recorded but nothing on this host says whose it is.
	// The model id is taken at face value, and this basis is how the row admits it.
	AttributionUnverified = "unverified"
)

// derivedBasis names a bucket's basis from the two facts that determine it.
//
// A bucket is keyed by its endpoint, so "was an endpoint recorded" and "did it resolve" are fixed
// across every request in it; the ONLY thing that can vary is whether some request's model id was
// contradicted. Two booleans therefore carry the whole answer, and the four names are derived
// rather than stored and merged.
//
// This replaced a stored basis plus a "weakest wins" ranking. The ranking had to claim endpoint
// was less trustworthy than model, which is false — an endpoint the host DECLARED is the strongest
// evidence here; what it lacks is a priceable model, and that is a different question. Conflating
// the two invited exactly the wrong intuition at every later edit.
func derivedBasis(endpoint string, endpointKnown, conflict bool) string {
	switch {
	case endpoint == "":
		return AttributionModel
	case !endpointKnown:
		return AttributionUnverified
	case conflict:
		return AttributionEndpoint
	default:
		return AttributionConfirmed
	}
}

// attribution is the resolved answer for one request or one token bundle.
type attribution struct {
	Vendor pricing.Vendor
	Basis  string
	// EndpointKnown / Conflict are the two bucket-level facts derivedBasis folds. Carried
	// separately so a bucket never has to merge display strings.
	EndpointKnown bool
	Conflict      bool
	// Priceable is false when the evidence forbids using the model id as a rate-card key. It is
	// NOT "we happen to have no price": that case is already reported as price_rule_missing.
	Priceable bool
}

// attributeUsage resolves the vendor for one (endpoint, model) pair.
//
// The four states, and why each behaves as it does:
//
//	endpoint absent          → model. Nothing to contradict it; unchanged from before this file.
//	endpoint agrees          → model. Two witnesses concur.
//	endpoint contradicts     → ENDPOINT wins, and pricing is refused.
//	endpoint unrecognised    → model, at face value, flagged.
//
// The third case refuses to price rather than repricing at the endpoint vendor's rates, and that
// is deliberate: we know the money went to Moonshot, but `gpt-5.6-sol` does not say WHICH Moonshot
// model answered (k3? k3-max?), and a rate card needs both. Substituting k3's price would be the
// same crime a second time. Under-counting under a visible「≈」is the honest reading.
//
// The fourth case does NOT refuse. An unrecognised endpoint is an absence of evidence, not
// evidence of a facade — a user who names their own OpenAI relay "local_codex" would otherwise
// watch every number on the page turn into「—」. The cost of being wrong there is a label; the
// cost of the third case was money. Different evidence, different response.
func attributeUsage(endpoint, model string) attribution {
	byModel := pricing.VendorForModel(model)
	if endpoint == "" {
		return attribution{Vendor: byModel, Basis: AttributionModel, Priceable: true}
	}
	byEndpoint, known := resolveEndpointVendor(endpoint)
	switch {
	case !known:
		return attribution{Vendor: byModel, Basis: AttributionUnverified, Priceable: true}
	case byEndpoint.ID == byModel.ID:
		return attribution{
			Vendor: byModel, Basis: AttributionConfirmed, EndpointKnown: true, Priceable: true,
		}
	default:
		return attribution{
			Vendor: byEndpoint, Basis: AttributionEndpoint, EndpointKnown: true, Conflict: true,
		}
	}
}

// publishedRatesFor returns the rate cards to SHOW beside a row, which is not always the ones for
// its biggest model.
//
// A row's unit price is the reader's only way to check its total ("$85.93 at which list?"). When
// the top model is a facade — `gpt-5.6-sol` on a row billed to Moonshot — publishing OpenAI's card
// under a Moonshot heading offers a check that silently confirms the wrong thing. Its money was
// already refused; the card beside it must go too, or the refusal is undone in the one place the
// reader was told to trust.
//
// Only a MISMATCH suppresses. A row with no vendor (unknown model id) still shows whatever the id
// resolves to, exactly as before.
func publishedRatesFor(vendor pricing.Vendor, topModel string) []pricing.RateCard {
	if vendor.ID != "" && pricing.VendorForModel(topModel).ID != vendor.ID {
		return nil
	}
	return pricing.PublishedRates(topModel)
}

// attributionVendors is the order endpoint declarations are searched in, and the order
// AttributionRevision hashes them in. One list, because a lookup that consulted a different set
// than the fingerprint covers would produce a mapping that never invalidates its own cache.
var attributionVendors = []string{VendorMoonshot, VendorZhipu, VendorOpenAI, VendorAnthropic, VendorGoogle}

// AttributionRevision fingerprints the endpoint→vendor declarations currently installed.
//
// It exists because attribution is now an INPUT TO PRICING, and prices are cached per day under
// the assumption that "a day that has ended cannot change". That assumption holds only while
// everything a price depends on is part of the cache key. Declare a relay for the first time and
// yesterday's relayed turns must stop being priced at the borrowed vendor's rates — without this
// fingerprint the correction would land only on days that happened to be re-read, which is the
// worst of both worlds: applied, invisible, and inconsistent between two windows of one report.
//
// "none" when no source is installed, so an embedder without credentials still has a stable key.
func AttributionRevision() string {
	var b strings.Builder
	for _, vendor := range attributionVendors {
		cred, ok := credentialFor(vendor)
		if !ok || len(cred.RuntimeProviderIDs) == 0 {
			continue
		}
		b.WriteString(vendor)
		b.WriteByte('=')
		b.WriteString(strings.Join(cred.RuntimeProviderIDs, ","))
		b.WriteByte(';')
	}
	if b.Len() == 0 {
		return "none"
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:6])
}

// resolveEndpointVendor maps a runtime-side endpoint id onto the vendor it bills, strongest
// evidence first. ok=false means "nobody here claims this id" — never a guess, and never a
// fallback vendor.
//
//  1. The host DECLARED it (Credential.RuntimeProviderIDs). A human said "mimo2codex-kimi-coding
//     is my Kimi plan", which outranks any inference.
//  2. The id IS a vendor id. Users routinely name the provider after the vendor — codex's own
//     default is literally "openai" — and reading that costs nothing and invents nothing.
//
// Two limits, stated because they are load-bearing rather than hypothetical:
//
//   - Hop 2 believes a NAME. Someone who calls their Azure or OpenRouter relay "openai" is read as
//     OpenAI. That is no worse than the model-id-only behaviour it replaces (both say OpenAI), and
//     hop 1 is the escape hatch: a declaration wins over the name.
//   - The mapping is CURRENT configuration applied to HISTORICAL facts. Reuse an endpoint id for a
//     different vendor and last month's requests re-attribute under this month's meaning. Storing
//     the resolution on each fact instead would freeze it correctly, at the cost of never being
//     able to fix a past misattribution. Neither is free; this one is at least self-correcting, and
//     AttributionRevision makes the change visible to every cache that depends on it.
func resolveEndpointVendor(endpoint string) (pricing.Vendor, bool) {
	if endpoint == "" {
		return pricing.VendorUnknown, false
	}
	if id := vendorForRuntimeProvider(endpoint); id != "" {
		if v, ok := pricing.VendorByID(id); ok {
			return v, true
		}
	}
	if v, ok := pricing.VendorByID(endpoint); ok {
		return v, true
	}
	return pricing.VendorUnknown, false
}
