package pricing

import (
	"strings"
	"time"
)

// CatalogVersion identifies the immutable pricing snapshot used by a quote.
// Updating a price creates new effective-dated rules and a new version; it does
// not rewrite historical request facts.
const CatalogVersion = "2026-07-15.2"
const FastModeSourceURL = "https://developers.openai.com/codex/agent-configuration/speed"

// RequestQuery contains only request-time billing evidence. Effort is retained
// for explainability but deliberately does not participate in unit-price
// selection: effort changes token use and latency, not provider token rates.
type RequestQuery struct {
	Model       string
	At          time.Time
	ServiceTier string
	Effort      string
}

// RequestQuote is the auditable API-equivalent price selected for one request.
// CreditsPerM is populated only where the provider publishes a token-to-credit
// schedule; it is separate from API-equivalent currency cost.
type RequestQuote struct {
	RuleID         string
	CatalogVersion string
	SourceURL      string
	EffectiveFrom  time.Time
	EffectiveUntil *time.Time
	Price          ModelPrice
	CreditsPerM    *Tier
}

// Cost evaluates one request using this quote. Reasoning output must not be
// added separately when the provider's Output count already includes it.
func (q RequestQuote) Cost(u Usage) (float64, string) {
	tier := q.Price.Tier
	context := u.Input + u.CacheRead + u.CacheWrite5m + u.CacheWrite1h
	if q.Price.ContextThreshold > 0 && q.Price.Above != nil && context > q.Price.ContextThreshold {
		tier = *q.Price.Above
	}
	return tierCost(tier, u), q.Price.Currency
}

// Credits evaluates the published subscription-credit token schedule. The
// boolean is false when no token-level credit schedule is known.
func (q RequestQuote) Credits(u Usage) (float64, bool) {
	if q.CreditsPerM == nil {
		return 0, false
	}
	return tierCost(*q.CreditsPerM, u), true
}

type catalogRule struct {
	id          string
	models      []string
	serviceTier string
	from        time.Time
	until       *time.Time
	price       ModelPrice
	creditsPerM *Tier
	sourceURL   string
}

// Catalog owns effective-dated request pricing. It is immutable after build.
type Catalog struct{ rules []catalogRule }

var defaultCatalog = Catalog{rules: buildCatalogRules()}

func DefaultCatalog() Catalog { return defaultCatalog }

// Quote returns the most specific effective rule. Missing/unknown billing
// evidence never falls through to a guessed provider-family rate.
func (c Catalog) Quote(q RequestQuery) (RequestQuote, bool) {
	model := normalize(q.Model)
	if model == "" || q.At.IsZero() {
		return RequestQuote{}, false
	}
	tier := normalizeServiceTier(q.ServiceTier)
	if tier == "unknown" {
		return RequestQuote{}, false
	}
	for _, rule := range c.rules {
		if rule.serviceTier != tier || q.At.Before(rule.from) || (rule.until != nil && !q.At.Before(*rule.until)) {
			continue
		}
		if !matchesAnyModel(model, rule.models) {
			continue
		}
		return RequestQuote{
			RuleID: rule.id, CatalogVersion: CatalogVersion, SourceURL: rule.sourceURL,
			EffectiveFrom: rule.from, EffectiveUntil: rule.until, Price: rule.price,
			CreditsPerM: rule.creditsPerM,
		}, true
	}
	return RequestQuote{}, false
}

// FastCreditMultiplier returns a multiplier only for an explicit Fast mode and
// a model with an official published multiplier. service_tier=priority is not
// Fast evidence and must never call this function with speed="fast" implicitly.
func FastCreditMultiplier(model, speed string) (float64, bool) {
	if !strings.EqualFold(strings.TrimSpace(speed), "fast") {
		return 1, false
	}
	switch normalize(model) {
	case "gpt-5.5":
		return 2.5, true
	case "gpt-5.4":
		return 2, true
	default:
		return 1, false
	}
}

func buildCatalogRules() []catalogRule {
	openAIModels := "https://developers.openai.com/api/docs/models/"
	openAIPricing := "https://developers.openai.com/api/docs/pricing"
	claudePricing := "https://platform.claude.com/docs/en/about-claude/pricing"
	credits := func(in, cached, out float64) *Tier {
		return &Tier{InputPerM: in, CacheReadPerM: cached, OutputPerM: out}
	}
	openAILongPrice := func(in, cached, out float64) ModelPrice {
		return ModelPrice{
			Tier: Tier{InputPerM: in, CacheReadPerM: cached, OutputPerM: out}, Currency: "USD",
			ContextThreshold: 272_000,
			Above:            &Tier{InputPerM: in * 2, CacheReadPerM: cached * 2, OutputPerM: out * 1.5},
		}
	}
	from2026 := mustDate("2026-01-01")
	fable5Launch := mustDate("2026-06-09")
	sonnetPromoEnd := mustDate("2026-09-01")
	return []catalogRule{
		{id: "openai.gpt-5.5.standard.v1", models: []string{"gpt-5.5"}, serviceTier: "standard", from: from2026,
			price:       openAILongPrice(5, .5, 30),
			creditsPerM: credits(125, 12.5, 750), sourceURL: openAIModels + "gpt-5.5"},
		{id: "openai.gpt-5.5.priority.v1", models: []string{"gpt-5.5"}, serviceTier: "priority", from: from2026,
			price:       ModelPrice{Tier: Tier{InputPerM: 12.5, CacheReadPerM: 1.25, OutputPerM: 75}, Currency: "USD"},
			creditsPerM: credits(125, 12.5, 750), sourceURL: openAIPricing},
		{id: "openai.gpt-5.4.standard.v1", models: []string{"gpt-5.4"}, serviceTier: "standard", from: from2026,
			price:       openAILongPrice(2.5, .25, 15),
			creditsPerM: credits(62.5, 6.25, 375), sourceURL: openAIModels + "gpt-5.4"},
		{id: "openai.gpt-5.4.priority.v1", models: []string{"gpt-5.4"}, serviceTier: "priority", from: from2026,
			price:       ModelPrice{Tier: Tier{InputPerM: 5, CacheReadPerM: .5, OutputPerM: 30}, Currency: "USD"},
			creditsPerM: credits(62.5, 6.25, 375), sourceURL: openAIPricing},
		{id: "openai.gpt-5.6-sol.standard.v1", models: []string{"gpt-5.6-sol"}, serviceTier: "standard", from: from2026,
			price:       openAILongPrice(5, .5, 30),
			creditsPerM: credits(125, 12.5, 750), sourceURL: openAIModels + "gpt-5.6-sol"},
		{id: "openai.gpt-5.6-sol.priority.v1", models: []string{"gpt-5.6-sol"}, serviceTier: "priority", from: from2026,
			price:       openAILongPrice(10, 1, 60),
			creditsPerM: credits(125, 12.5, 750), sourceURL: openAIPricing},
		{id: "openai.gpt-5.6-terra.standard.v1", models: []string{"gpt-5.6-terra"}, serviceTier: "standard", from: from2026,
			price:       openAILongPrice(2.5, .25, 15),
			creditsPerM: credits(62.5, 6.25, 375), sourceURL: openAIModels + "gpt-5.6-terra"},
		{id: "openai.gpt-5.6-terra.priority.v1", models: []string{"gpt-5.6-terra"}, serviceTier: "priority", from: from2026,
			price:       openAILongPrice(5, .5, 30),
			creditsPerM: credits(62.5, 6.25, 375), sourceURL: openAIPricing},
		{id: "openai.gpt-5.6-luna.standard.v1", models: []string{"gpt-5.6-luna"}, serviceTier: "standard", from: from2026,
			price:       openAILongPrice(1, .1, 6),
			creditsPerM: credits(25, 2.5, 150), sourceURL: openAIModels + "gpt-5.6-luna"},
		{id: "openai.gpt-5.6-luna.priority.v1", models: []string{"gpt-5.6-luna"}, serviceTier: "priority", from: from2026,
			price:       openAILongPrice(2, .2, 12),
			creditsPerM: credits(25, 2.5, 150), sourceURL: openAIPricing},

		{id: "anthropic.claude-sonnet-5.promo.v1", models: []string{"claude-sonnet-5"}, serviceTier: "standard", from: from2026, until: &sonnetPromoEnd,
			price: ModelPrice{Tier: Tier{InputPerM: 2, CacheReadPerM: .2, OutputPerM: 10, CacheWrite5mPerM: 2.5, CacheWrite1hPerM: 4}, Currency: "USD"}, sourceURL: claudePricing},
		{id: "anthropic.claude-sonnet-5.standard.v1", models: []string{"claude-sonnet-5"}, serviceTier: "standard", from: sonnetPromoEnd,
			price: ModelPrice{Tier: Tier{InputPerM: 3, CacheReadPerM: .3, OutputPerM: 15, CacheWrite5mPerM: 3.75, CacheWrite1hPerM: 6}, Currency: "USD"}, sourceURL: claudePricing},
		{id: "anthropic.claude-opus-4-8.standard.v1", models: []string{"claude-opus-4-8"}, serviceTier: "standard", from: from2026,
			price: ModelPrice{Tier: Tier{InputPerM: 5, CacheReadPerM: .5, OutputPerM: 25, CacheWrite5mPerM: 6.25, CacheWrite1hPerM: 10}, Currency: "USD"}, sourceURL: claudePricing},
		{id: "anthropic.claude-fable-mythos-5.standard.v1", models: []string{"claude-fable-5", "claude-mythos-5"}, serviceTier: "standard", from: fable5Launch,
			price: ModelPrice{Tier: Tier{InputPerM: 10, CacheReadPerM: 1, OutputPerM: 50, CacheWrite5mPerM: 12.5, CacheWrite1hPerM: 20}, Currency: "USD"}, sourceURL: claudePricing},
		{id: "anthropic.claude-opus-4-x.standard.v1", models: []string{"claude-opus-4-7", "claude-opus-4-6", "claude-opus-4-5"}, serviceTier: "standard", from: from2026,
			price: ModelPrice{Tier: Tier{InputPerM: 5, CacheReadPerM: .5, OutputPerM: 25, CacheWrite5mPerM: 6.25, CacheWrite1hPerM: 10}, Currency: "USD"}, sourceURL: claudePricing},
		{id: "anthropic.claude-sonnet-4-x.standard.v1", models: []string{"claude-sonnet-4-6", "claude-sonnet-4-5"}, serviceTier: "standard", from: from2026,
			price: ModelPrice{Tier: Tier{InputPerM: 3, CacheReadPerM: .3, OutputPerM: 15, CacheWrite5mPerM: 3.75, CacheWrite1hPerM: 6}, Currency: "USD"}, sourceURL: claudePricing},
		{id: "anthropic.claude-haiku-4-5.standard.v1", models: []string{"claude-haiku-4-5"}, serviceTier: "standard", from: from2026,
			price: ModelPrice{Tier: Tier{InputPerM: 1, CacheReadPerM: .1, OutputPerM: 5, CacheWrite5mPerM: 1.25, CacheWrite1hPerM: 2}, Currency: "USD"}, sourceURL: claudePricing},
	}
}

func normalizeServiceTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "", "default", "standard", "fast":
		return "standard"
	case "priority":
		return "priority"
	default:
		return "unknown"
	}
}

func matchesAnyModel(model string, candidates []string) bool {
	for _, candidate := range candidates {
		if model == candidate || strings.HasPrefix(model, candidate+"-") {
			return true
		}
	}
	return false
}

func tierCost(t Tier, u Usage) float64 {
	cw1h := t.CacheWrite1hPerM
	if cw1h == 0 {
		cw1h = t.CacheWrite5mPerM
	}
	return float64(u.Input)*t.InputPerM/1e6 +
		float64(u.Output)*t.OutputPerM/1e6 +
		float64(u.CacheRead)*t.CacheReadPerM/1e6 +
		float64(u.CacheWrite5m)*t.CacheWrite5mPerM/1e6 +
		float64(u.CacheWrite1h)*cw1h/1e6
}

func mustDate(v string) time.Time {
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		panic(err)
	}
	return t
}
