// Package pricing is the single source of truth (SSOT) for LLM model pricing and
// cost calculation across Brightman AI projects. It maps a model id to a per-token
// price table and computes the USD cost of a token usage record.
//
// The numbers match ccusage, which sources its prices from LiteLLM's official
// model_prices_and_context_window.json. Prices are expressed in USD per MILLION
// tokens (USD/MTok). Token classes:
//
//   - Input          — fresh prompt tokens.
//   - Output         — generated completion tokens.
//   - CacheCreate    — tokens written into a prompt cache (cache-write). Anthropic
//     only; OpenAI/codex and Gemini have NO cache-create tier (CacheCreatePerM == 0).
//   - CacheRead      — tokens served from a prompt cache (cache-read), discounted.
//
// It lives in the shared kit (github.com/brightman-ai/kit), consumed equally by
// deepwork-terminal and deepwork-pro. Neither owns it; the SSOT is here.
package pricing

import (
	"sort"
	"strings"
)

// ModelPrice is the per-million-token price of a model, by token class.
// CacheCreatePerM is 0 for providers without a cache-write tier (OpenAI, Gemini).
type ModelPrice struct {
	InputPerM       float64 // USD per 1M input tokens
	OutputPerM      float64 // USD per 1M output tokens
	CacheCreatePerM float64 // USD per 1M cache-create (write) tokens; 0 if unsupported
	CacheReadPerM   float64 // USD per 1M cache-read tokens
	Currency        string  // always "USD" in the current table
}

// Usage is a token usage record for a single model invocation (or an aggregate).
type Usage struct {
	Input       int
	Output      int
	CacheCreate int
	CacheRead   int
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

// Cost returns the total USD cost of u for model:
//
//	Σ tokens × pricePerM / 1e6
//
// across the four token classes. cacheCreate uses CacheCreatePerM and cacheRead
// uses CacheReadPerM; for models with CacheCreatePerM == 0 (OpenAI, Gemini),
// cache-create tokens cost nothing. When the model is unknown, ok is false and
// cost is 0 — the cost is NEVER guessed from a fallback price.
func Cost(model string, u Usage) (cost float64, currency string, ok bool) {
	p, found := Lookup(model)
	if !found {
		return 0, "", false
	}
	cost = float64(u.Input)*p.InputPerM/1e6 +
		float64(u.Output)*p.OutputPerM/1e6 +
		float64(u.CacheCreate)*p.CacheCreatePerM/1e6 +
		float64(u.CacheRead)*p.CacheReadPerM/1e6
	return cost, p.Currency, true
}
