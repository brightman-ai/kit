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
// Here the vendor is derived from the MODEL ID and nothing else. The alternatives were considered
// and rejected on evidence:
//
//   - fact.Provider — for codex it is `model_provider_id`, a name the USER invents in
//     ~/.codex/config.toml. On this machine those are "local_codex", "deepseek" and
//     "mimo2codex-kimi-coding": one canonical, one meaningless, one a relay's brand. It is routing
//     configuration, not identity. (claude used to hardcode "anthropic" here; that was simply a
//     value invented at the scanner and has since been removed — Claude Code records no endpoint.)
//   - the endpoint URL — not recorded in the transcript at all.
//
// A model id, by contrast, is what the provider itself echoes back on every request. It is the
// strongest evidence present, so it is the only evidence used HERE.
//
// That last word matters. This function answers "whose model id is this?", which is all pricing
// needs and all it can defend. It does NOT answer "whose bill did this land on" — a relay can pass
// the upstream's id straight through, and 144 measured turns of Kimi traffic carried `gpt-5.6-sol`.
// Catching that needs a second witness (the endpoint) used as a falsifier, which is a different
// question with a different failure mode, so it lives in usage/attribution.go and calls this from
// there. Do not fold the two: the day this function starts consulting an endpoint, every price it
// returns inherits a user-invented string's reliability.
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
	VendorXAI       = Vendor{ID: "xai", Display: "xAI"}
	VendorMistral   = Vendor{ID: "mistral", Display: "Mistral"}
	VendorMiniMax   = Vendor{ID: "minimax", Display: "MiniMax"}
	// VendorUnknown is the honest answer, not a fallback bucket. It never absorbs a model into a
	// neighbouring vendor's row, because a wrong vendor is a wrong currency is a wrong number.
	VendorUnknown = Vendor{}
)

// vendorsByID resolves a canonical vendor id to its Vendor. It is what makes the generated
// table's vendor column meaningful — and its ABSENCE is a real answer: an id here with no entry
// means upstream started carrying a vendor this codebase has never named, which
// TestGeneratedVendorsAreCanonical turns into a failed build rather than a row labelled with a
// raw provider slug.
var vendorsByID = map[string]Vendor{
	VendorAnthropic.ID: VendorAnthropic,
	VendorOpenAI.ID:    VendorOpenAI,
	VendorGoogle.ID:    VendorGoogle,
	VendorMoonshot.ID:  VendorMoonshot,
	VendorDeepSeek.ID:  VendorDeepSeek,
	VendorZhipu.ID:     VendorZhipu,
	VendorAlibaba.ID:   VendorAlibaba,
	VendorXAI.ID:       VendorXAI,
	VendorMistral.ID:   VendorMistral,
	VendorMiniMax.ID:   VendorMiniMax,
}

// VendorByID resolves a canonical vendor id. ok=false for an id this codebase has never named —
// which callers must render as UNKNOWN rather than as a plausible-looking vendor: the whole point
// of the id table is that being un-named is a real, reportable answer.
func VendorByID(id string) (Vendor, bool) {
	v, ok := vendorsByID[id]
	return v, ok
}

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

	// Reachable because a first-party CLI can be pointed at any OpenAI-compatible endpoint — the
	// same door kimi and deepseek came through. Only the family stems are listed: the exact ids
	// upstream carries resolve through table_gen.go, which states the vendor rather than inferring
	// it from spelling.
	{"grok", VendorXAI},
	{"mistral", VendorMistral},
	{"minimax", VendorMiniMax},
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
	// Upstream states the provider for every id it carries, and a stated fact outranks a rule that
	// infers ownership from how the id is spelled. It has to: "chatgpt-4o-latest" and "o4-mini" are
	// OpenAI's, and no prefix rule over those strings says so without also claiming every future
	// model that happens to start the same way.
	if e, ok := generatedByModel[m]; ok {
		if v, known := vendorsByID[e.vendor]; known {
			return v
		}
	}
	for _, e := range sortedVendorTable {
		if matchesVendorFamily(m, e.key) {
			return e.vendor
		}
	}
	return VendorUnknown
}

// matchesVendorFamily accepts a family stem followed by a separator OR a digit.
//
// The digit case is not a nicety — it is how these vendors actually name things. Alibaba ships
// `qwen3.8-max` and `qwen3.7-plus`, with no separator at all between the family and its version,
// so a dash-only rule silently loses every current Qwen model to 未知厂商 while happily resolving
// the discontinued `qwen-max`. The same shape shows up as gpt4 / claude3 / grok4 elsewhere.
//
// This is looser than the PRICE matcher on purpose, and the asymmetry is the whole design: a
// family guess on the vendor puts a row under a heading, where being wrong is visible and costs a
// label. A family guess on the price invents money, where being wrong is invisible.
func matchesVendorFamily(model, key string) bool {
	if model == key {
		return true
	}
	if !strings.HasPrefix(model, key) {
		return false
	}
	switch next := model[len(key)]; {
	case next == '-', next == '.', next == '_', next == '/':
		return true
	default:
		return next >= '0' && next <= '9'
	}
}
