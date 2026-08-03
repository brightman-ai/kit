package pricing

import "strings"

// ── who you owe, as opposed to who spent it ───────────────────────────────────────────────────
//
// A usage fact answers two different money questions, and collapsing them is how a DeepSeek
// request ends up filed under an OpenAI subscription:
//
//	CALLER (runtime)  — which CLI made the request: claude / codex / whale / …
//	VENDOR (this file) — whose model answered it, i.e. who the money is owed to.
//
// They used to be one axis because they used to be the same thing: a `claude` process could only
// ever talk to Anthropic. That stopped being true the moment a CLI could point at any
// OpenAI-compatible endpoint, and the axis has been quietly lying ever since.
//
// The vendor is derived from the MODEL ID and nothing else. The alternatives were considered and
// rejected on evidence:
//
//   - fact.Provider — for claude it is hardcoded "anthropic" (request_usage.go), and for codex it
//     is `model_provider_id`, a name the USER invents in ~/.codex/config.toml. On this machine
//     those are "local_codex", "deepseek" and "mimo2codex-kimi-coding": one canonical, one
//     meaningless, one a relay's brand. It is routing configuration, not identity.
//   - the endpoint URL — not recorded in the transcript at all.
//
// A model id, by contrast, is what the provider itself echoes back on every request. It is the
// strongest evidence present, so it is the only evidence used.
//
// Vendor lives HERE, next to the prices, because a vendor and a currency are the same fact seen
// twice: DeepSeek bills in USD, Kimi bills in USD, and a row that mixes vendors mixes currencies
// and therefore has no computable total. Keeping the two tables in one package makes
// "priced but no vendor" a compile-and-test-visible mistake rather than money in the wrong bucket
// (TestEveryPricedModelHasAVendor pins exactly that).

// Vendor is the billing subject behind a model id.
//
// ID is canonical, lowercase and wire-stable — it is what the API returns and what clients group
// on, so it must not carry product branding that could be rebranded later. Display is the name a
// human recognises on their own invoice, which is NOT always the company name: Moonshot AI bills
// you for "Kimi".
//
// The zero Vendor means "this model id names no vendor we know". It deliberately carries an EMPTY
// Display rather than a "未知" string: naming the unknown is presentation, and the surface that
// renders it also wants to show the raw model id beside it. kit does not get to compose that
// sentence.
type Vendor struct {
	ID      string
	Display string
}

// Known reports whether this vendor was actually resolved. Callers must branch on it before
// putting a row under a vendor heading — an unresolved vendor grouped with a resolved one is the
// misattribution this whole file exists to prevent.
func (v Vendor) Known() bool { return v.ID != "" }

// The canonical vendors. IDs are the company/org, since that is who the invoice comes from.
var (
	VendorAnthropic = Vendor{ID: "anthropic", Display: "Anthropic"}
	VendorOpenAI    = Vendor{ID: "openai", Display: "OpenAI"}
	VendorGoogle    = Vendor{ID: "google", Display: "Google"}
	VendorMoonshot  = Vendor{ID: "moonshot", Display: "Kimi"}
	VendorDeepSeek  = Vendor{ID: "deepseek", Display: "DeepSeek"}
	VendorZhipu     = Vendor{ID: "zhipu", Display: "智谱 GLM"}
	VendorAlibaba   = Vendor{ID: "alibaba", Display: "通义千问"}
	// VendorUnknown is the honest answer, not a fallback bucket. It never absorbs a model into a
	// neighbouring vendor's row, because a wrong vendor is a wrong currency is a wrong number.
	VendorUnknown = Vendor{}
)

type vendorEntry struct {
	key    string
	vendor Vendor
}

// vendorTable maps model-id prefixes onto vendors.
//
// Keys are matched with the SAME rule as the price table (exact, or prefix followed by "-"), on
// the SAME normalized id — so "us.anthropic.claude-opus-5[1m]" and "claude-opus-5" cannot resolve
// to different vendors, and a key that prices a model always also names it. Substring matching was
// rejected: it makes any future "gpt-alike-by-someone-else" silently OpenAI's bill.
//
// Bare keys ("k3", "fable") exist because CLIs record bare ids — codex writes model:"k3", not
// "kimi-k3". They are as specific as the id we actually have to work with.
var vendorTable = []vendorEntry{
	{"claude", VendorAnthropic},
	{"opus", VendorAnthropic},
	{"sonnet", VendorAnthropic},
	{"haiku", VendorAnthropic},
	{"fable", VendorAnthropic},
	{"mythos", VendorAnthropic},

	{"gpt", VendorOpenAI},
	{"codex", VendorOpenAI},
	{"o1", VendorOpenAI},
	{"o3", VendorOpenAI},

	{"gemini", VendorGoogle},

	{"kimi", VendorMoonshot},
	{"moonshot", VendorMoonshot},
	{"k2", VendorMoonshot},
	{"k3", VendorMoonshot},

	{"deepseek", VendorDeepSeek},
	{"glm", VendorZhipu},
	{"qwen", VendorAlibaba},
}

// sortedVendorTable is vendorTable ordered longest-key-first, mirroring buildSortedTable, so a
// specific key always beats a generic one regardless of source order.
var sortedVendorTable = buildSortedVendorTable()

func buildSortedVendorTable() []vendorEntry {
	out := make([]vendorEntry, len(vendorTable))
	copy(out, vendorTable)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(out[j].key) > len(out[j-1].key); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// VendorForModel resolves a model id to the vendor that bills for it.
//
// An unrecognised id returns VendorUnknown — never a guess. Downstream that surfaces as an
// explicitly named "unknown vendor" row showing the raw model id, which is a question the user can
// answer; a plausible-looking wrong vendor is one they never even get asked.
func VendorForModel(model string) Vendor {
	m := normalize(model)
	if m == "" {
		return VendorUnknown
	}
	for _, e := range sortedVendorTable {
		if m == e.key || strings.HasPrefix(m, e.key+"-") {
			return e.vendor
		}
	}
	return VendorUnknown
}
