// Package pricing is the single source of truth (SSOT) for LLM model pricing and
// cost calculation across Brightman AI projects. It maps a model id to a per-token
// price table and computes the USD cost of a SINGLE REQUEST's token usage record.
//
// The numbers match ccusage, which sources its prices from LiteLLM's official
// model_prices_and_context_window.json. Prices are expressed in USD per MILLION
// tokens (USD/MTok). Token classes (one request):
//
//   - Input         — fresh prompt tokens.
//   - Output        — generated completion tokens.
//   - CacheRead     — tokens served from a prompt cache (cache-read), cheapest.
//   - CacheWrite5m  — tokens written into a 5-minute-TTL prompt cache (Anthropic:
//     1.25× input). OpenAI/codex and Gemini have NO cache-write tier (0).
//   - CacheWrite1h  — tokens written into a 1-hour-TTL prompt cache (Anthropic:
//     2× input). A 0 rate in the table falls back to the 5m rate.
//
// Two billing tiers exist:
//
//   - BASE tier — the embedded Tier prices.
//   - ABOVE tier (long-context premium) — OpenAI gpt-5.x and Gemini roughly double
//     per-token prices once a request's context exceeds ContextThreshold tokens
//     (gpt-5.4/5.5: 272000; gemini 2.5-pro / 3-pro: 200000). Anthropic 4.x models
//     have NO context tier (ContextThreshold == 0, Above == nil).
//
// Cost is computed PER REQUEST because the context-tier decision is per-request; a
// caller sums Cost over the requests of a session. It lives in the shared kit
// (github.com/brightman-ai/kit), consumed equally by deepwork-terminal and
// deepwork-pro. Neither owns it; the SSOT is here.
package pricing

import (
	"sort"
	"strings"
)

// Tier is the per-MILLION-token unit prices for one billing tier.
// CacheWrite5m/CacheWrite1h are 0 for providers without a cache-write tier
// (OpenAI, Gemini, Chinese vendors). CacheWrite1h == 0 means "fall back to the 5m
// rate" — never free unless CacheWrite5m is also 0.
type Tier struct {
	InputPerM        float64 // USD per 1M input tokens
	OutputPerM       float64 // USD per 1M output tokens
	CacheReadPerM    float64 // USD per 1M cache-read tokens (cheapest)
	CacheWrite5mPerM float64 // USD per 1M cache-write tokens, 5-minute TTL (Anthropic: 1.25× input)
	CacheWrite1hPerM float64 // USD per 1M cache-write tokens, 1-hour TTL (Anthropic: 2× input); 0 → fall back to 5m
}

// ModelPrice is the per-million-token price of a model: a base Tier plus an
// optional long-context premium Tier (Above) that applies once a request's context
// exceeds ContextThreshold tokens.
type ModelPrice struct {
	Tier                    // base prices (embedded)
	Currency         string // "USD" or "CNY"
	ContextThreshold int    // tokens; context > this → Above tier applies (0 = no long-context tier)
	Above            *Tier  // long-context prices (nil = none)
}

// Usage is the token counts of a SINGLE request, with cache-write split by TTL.
type Usage struct {
	Input        int
	Output       int
	CacheRead    int
	CacheWrite5m int
	CacheWrite1h int
}

// priceEntry is one row of the embedded price table: a canonical lowercase key
// matched against a normalized model id, plus its price.
type priceEntry struct {
	key   string
	price ModelPrice
}

// sortedTable is priceTable ordered longest-key-first, so that a specific key
// (e.g. "claude-opus-4-8") is matched before a generic fallback (e.g. "opus").
// Computed once at package init from priceTable; source order is irrelevant.
var sortedTable = buildSortedTable()

func buildSortedTable() []priceEntry {
	t := make([]priceEntry, len(priceTable))
	copy(t, priceTable)
	sort.SliceStable(t, func(i, j int) bool {
		return len(t[i].key) > len(t[j].key)
	})
	return t
}

// providerPrefixes are stripped (longest-first) from the head of a model id during
// normalization. They denote provider/region routing, not the model family.
var providerPrefixes = []string{
	"anthropic.",
	"azure_ai/",
	"bedrock/",
	"vertex_ai/",
	"openai/",
	"global.",
	"apac.",
	"us.",
	"eu.",
	"au.",
	"jp.",
}

// normalize canonicalizes a raw model id so it can be matched against the table:
//
//  1. lowercase
//  2. strip a provider/region prefix (anthropic., azure_ai/, openai/, us., eu.,
//     au., apac., jp., global., bedrock/, vertex_ai/) — applied repeatedly, since
//     ids may stack a region on a provider (e.g. "us.anthropic.claude-...").
//  3. strip a trailing context tag in square brackets (e.g. "[1m]", "[...]").
//  4. strip a trailing bedrock version suffix (e.g. "-v2:0", "-v1:0", "-v1").
func normalize(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))

	// Strip stacked provider/region prefixes (longest match wins each pass).
	for {
		stripped := false
		for _, p := range providerPrefixes {
			if strings.HasPrefix(m, p) {
				m = m[len(p):]
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}

	// Strip a trailing context tag like "[1m]" or "[200k]".
	if i := strings.IndexByte(m, '['); i >= 0 && strings.HasSuffix(m, "]") {
		m = strings.TrimSpace(m[:i])
	}

	// Strip a trailing bedrock version suffix: "-vN:M" or "-vN".
	m = stripBedrockVersion(m)

	return m
}

// stripBedrockVersion removes a trailing "-vN:M" or "-vN" suffix (N, M digits),
// e.g. "claude-3-5-haiku-v1:0" -> "claude-3-5-haiku".
func stripBedrockVersion(m string) string {
	i := strings.LastIndex(m, "-v")
	if i < 0 {
		return m
	}
	rest := m[i+2:] // characters after "-v"
	if rest == "" {
		return m
	}
	sawDigit := false
	sawColon := false
	for _, r := range rest {
		switch {
		case r >= '0' && r <= '9':
			sawDigit = true
		case r == ':':
			if sawColon { // at most one colon
				return m
			}
			sawColon = true
		default:
			return m // any other char => not a version suffix
		}
	}
	if !sawDigit {
		return m
	}
	return m[:i]
}

// Lookup normalizes model and returns its price via longest-key-first
// prefix/substring matching against the embedded table. The second result is
// false when no family matches — callers MUST NOT guess a price in that case.
func Lookup(model string) (ModelPrice, bool) {
	m := normalize(model)
	if m == "" {
		return ModelPrice{}, false
	}
	for _, e := range sortedTable {
		if strings.Contains(m, e.key) {
			return e.price, true
		}
	}
	return ModelPrice{}, false
}

// Cost returns the USD (or CNY) cost of ONE request u for model.
//
// It first picks the billing tier: context := Input + CacheRead + CacheWrite5m +
// CacheWrite1h; when ContextThreshold > 0 && context > ContextThreshold and an
// Above tier exists, the Above (long-context premium) prices apply, otherwise the
// base Tier. Within the tier, cache-write tokens are charged per TTL: 5m tokens at
// CacheWrite5mPerM, 1h tokens at CacheWrite1hPerM (falling back to the 5m rate when
// CacheWrite1hPerM == 0). When the model is unknown, ok is false and cost is 0 —
// the cost is NEVER guessed from a fallback price.
func Cost(model string, u Usage) (cost float64, currency string, ok bool) {
	p, found := Lookup(model)
	if !found {
		return 0, "", false
	}

	tier := p.Tier
	context := u.Input + u.CacheRead + u.CacheWrite5m + u.CacheWrite1h
	if p.ContextThreshold > 0 && p.Above != nil && context > p.ContextThreshold {
		tier = *p.Above
	}

	cw1h := tier.CacheWrite1hPerM
	if cw1h == 0 {
		cw1h = tier.CacheWrite5mPerM // 0 → fall back to the 5m rate
	}

	cost = float64(u.Input)*tier.InputPerM/1e6 +
		float64(u.Output)*tier.OutputPerM/1e6 +
		float64(u.CacheRead)*tier.CacheReadPerM/1e6 +
		float64(u.CacheWrite5m)*tier.CacheWrite5mPerM/1e6 +
		float64(u.CacheWrite1h)*cw1h/1e6
	return cost, p.Currency, true
}
