package usage

import (
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
	Complete       bool     `json:"complete"`
	Diagnostics    []string `json:"diagnostics,omitempty"`
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
	quote, ok := pricing.DefaultCatalog().Quote(pricing.RequestQuery{
		Model: f.Model, At: f.At, ServiceTier: f.ServiceTier, Effort: f.Effort,
	})
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
