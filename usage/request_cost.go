package usage

import (
	"time"

	"github.com/brightman-ai/kit/pricing"
	"github.com/brightman-ai/kit/transcript"
)

// RequestCostProjection is an auditable projection over an immutable request
// fact. APIEquivalent is not a claim about the user's payment method. Credits
// is emitted only when an official token-level credit schedule is published.
type RequestCostProjection struct {
	RequestID      string   `json:"request_id"`
	RuleID         string   `json:"rule_id,omitempty"`
	CatalogVersion string   `json:"catalog_version,omitempty"`
	SourceURL      string   `json:"source_url,omitempty"`
	APIEquivalent  *float64 `json:"api_equivalent,omitempty"`
	Currency       string   `json:"currency,omitempty"`
	Credits        *float64 `json:"credits,omitempty"`
	FastMultiplier *float64 `json:"fast_multiplier,omitempty"`
	FastSourceURL  string   `json:"fast_source_url,omitempty"`
	// PriceVerifiedAt is when the rule behind this number was last checked against
	// SourceURL. Carried through so a report can publish the AGE of its own prices:
	// an embedded table cannot detect a vendor's price change, so saying how old it
	// is, is the only defence the reader gets.
	PriceVerifiedAt time.Time `json:"price_verified_at,omitempty"`
	Complete        bool      `json:"complete"`
	Diagnostics     []string  `json:"diagnostics,omitempty"`
}

// ProjectRequestCost prices exactly one request. Unknown cache-write TTL is a
// hard incompleteness boundary because Claude's 5m and 1h rates differ.
func ProjectRequestCost(f transcript.ModelRequestUsage) RequestCostProjection {
	result := RequestCostProjection{RequestID: f.ID}
	if f.Model == "" || f.At.IsZero() {
		result.Diagnostics = append(result.Diagnostics, "missing_model_or_timestamp")
		return result
	}
	if f.CacheWriteUnknownTokens > 0 {
		result.Diagnostics = append(result.Diagnostics, "cache_write_ttl_unknown")
		return result
	}
	// Tier is a request fact, not a model default. In particular, Codex
	// priority/Fast evidence must never be reconstructed from a later session
	// setting. Missing evidence stays unpriced and visible in coverage.
	if f.ServiceTier == "" {
		result.Diagnostics = append(result.Diagnostics, "service_tier_missing")
		return result
	}
	// Two hops, in order of how much each source knows.
	//
	// The catalog knows when a price CHANGED and what each service tier costs, so it answers first
	// and always wins. It only ever holds models a human entered by hand.
	//
	// The embedded tables know breadth — hundreds of exact ids, refreshed from upstream by
	// gen-table. Until this second hop existed that breadth was unreachable HERE: a catalog miss
	// ended pricing outright, so a thoroughly well-known model (o4-mini, kimi-k2.6) came back
	// price_rule_missing and rendered「—」, which reads as a broken panel rather than as the
	// missing price it was.
	//
	// QuoteFromSnapshot is exact-id and standard-tier only, so widening reach here does not widen
	// guessing: an unknown model and a non-standard tier both still fall through to unpriced, and
	// the report's coverage counts still say so out loud.
	quote, ok := pricing.DefaultCatalog().Quote(pricing.RequestQuery{
		Model: f.Model, At: f.At, ServiceTier: f.ServiceTier, Effort: f.Effort,
	})
	if !ok {
		quote, ok = pricing.QuoteFromSnapshot(f.Model, f.ServiceTier)
	}
	if !ok {
		result.Diagnostics = append(result.Diagnostics, "price_rule_missing")
		return result
	}
	u := pricing.Usage{
		Input: int(f.InputTokens), Output: int(f.OutputTokens), CacheRead: int(f.CachedInputTokens),
		CacheWrite5m: int(f.CacheWrite5mTokens), CacheWrite1h: int(f.CacheWrite1hTokens),
	}
	amount, currency := quote.Cost(u)
	result.RuleID = quote.RuleID
	result.CatalogVersion = quote.CatalogVersion
	result.SourceURL = quote.SourceURL
	result.PriceVerifiedAt = quote.VerifiedAt
	result.APIEquivalent = &amount
	result.Currency = currency
	if credits, known := quote.Credits(u); known {
		if multiplier, fastKnown := pricing.FastCreditMultiplier(f.Model, f.Speed); fastKnown {
			credits *= multiplier
			result.FastMultiplier = &multiplier
			result.FastSourceURL = pricing.FastModeSourceURL
		}
		result.Credits = &credits
	}
	result.Complete = true
	return result
}
