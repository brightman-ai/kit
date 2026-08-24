package usage

import (
	"time"

	"github.com/brightman-ai/kit/transcript"
)

// DailyUsage is one local calendar day's slice of a usage report, in a form that ADDS.
//
// A day that has ended cannot change. Its facts are written, its prices are fixed, and no
// later request will ever carry its timestamp. Yet a report over a window re-derived every
// day in it on every request: the 30-day report walked 90,000 facts to answer a question
// whose first 29 days had the same answer as the last time it was asked — 117 ms, against
// 0.6 ms for 24h, a cost that grew with the window and with the corpus, forever.
//
// Aggregating a day once and folding days per report is the same arithmetic in a different
// order, and it decouples the cost of a report from the length of its window.
//
// Date is local to Timezone, because a calendar day is a timezone's notion and nobody
// else's. Both are carried so the value is self-describing: a day aggregated for one zone
// cannot be silently folded into a report for another.
type DailyUsage struct {
	Date     string
	Timezone string
	// providers is keyed by the FULL (vendor, caller, billing) identity — see ensureProviderAcc
	// for why all three belong in the key. Unexported because it is an accumulator, not a
	// wire type: holders pass it back, they do not read it.
	providers map[string]*requestReportAcc
}

// AggregateDaily buckets facts into per-local-date aggregates.
//
// It applies NO window. A day is a fact about the day, and the same day serves the 24h, 7d,
// 14d and 30d reports without being recomputed for each — which is the entire point.
func AggregateDaily(timezone string, facts []transcript.ModelRequestUsage) map[string]DailyUsage {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc, timezone = time.UTC, "UTC"
	}
	out := make(map[string]DailyUsage)
	for _, fact := range facts {
		date := fact.At.In(loc).Format("2006-01-02")
		day, ok := out[date]
		if !ok {
			day = DailyUsage{Date: date, Timezone: timezone, providers: make(map[string]*requestReportAcc)}
			out[date] = day
		}
		// The two money axes, resolved independently from the evidence each one actually has.
		// Vendor from the model id CROSS-CHECKED against the endpoint the runtime dialled (see
		// attribution.go — the model id alone made a relayed Kimi turn read as OpenAI); caller from
		// the transcript that recorded the fact. Only when the transcript names no caller at all
		// does the vendor get to suggest one, and that is flagged as a guess.
		attributed := attributeUsage(fact.Provider, fact.Model)
		vendor := attributed.Vendor
		runtime := fact.Runtime
		if runtime == "" {
			runtime = runtimeGuessForVendor(vendor)
		}
		acc := ensureProviderAcc(day.providers, vendor, runtime, normalizeRequestBilling(fact.BillingMode), fact.Provider)
		acc.endpointKnown = attributed.EndpointKnown
		acc.conflict = acc.conflict || attributed.Conflict

		physicalInput := fact.InputTokens
		cacheWrite := fact.CacheWrite5mTokens + fact.CacheWrite1hTokens + fact.CacheWriteUnknownTokens
		acc.in += physicalInput
		acc.out += fact.OutputTokens
		acc.read += fact.CachedInputTokens
		acc.write += cacheWrite
		acc.requests++
		if acc.byModel == nil {
			acc.byModel = make(map[string]int64)
		}
		acc.byModel[fact.Model] += physicalInput + fact.CachedInputTokens + cacheWrite + fact.OutputTokens

		projection := ProjectRequestCost(fact)
		if projection.Complete && projection.APIEquivalent != nil && projection.Currency != "" {
			acc.costs[projection.Currency] += *projection.APIEquivalent
			acc.priced++
			if !projection.PriceVerifiedAt.IsZero() &&
				(acc.priceVerifiedAt.IsZero() || projection.PriceVerifiedAt.Before(acc.priceVerifiedAt)) {
				acc.priceVerifiedAt = projection.PriceVerifiedAt
			}
		}
	}
	return out
}

// BuildRequestReportFromDaily folds pre-aggregated days into one window's report.
//
// Sources are the per-origin aggregates (one per transcript file, say); they are read, never
// mutated, so a caller may hold them as a cache and hand the same maps to every window. A
// source aggregated for a different timezone is skipped rather than silently mis-dated — the
// caller must key its cache by timezone, and this is the assertion of that.
func BuildRequestReportFromDaily(window WindowKind, timezone string, now time.Time, sources []map[string]DailyUsage) UsageReport {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc, timezone = time.UTC, "UTC"
	}
	days := reportDays(window)
	y, m, d := now.In(loc).Date()
	endExclusive := time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	start := endExclusive.AddDate(0, 0, -days)

	inWindow := make(map[string]bool, days)
	for i := range days {
		inWindow[start.AddDate(0, 0, i).Format("2006-01-02")] = true
	}

	dayTotals := make(map[string]*requestReportAcc, days)
	providers := make(map[string]*requestReportAcc)
	all := &requestReportAcc{costs: make(map[string]float64)}
	for _, source := range sources {
		for date, day := range source {
			if !inWindow[date] || day.Timezone != timezone {
				continue
			}
			total := ensureRequestAcc(dayTotals, date)
			for key, acc := range day.providers {
				merged := providers[key]
				if merged == nil {
					merged = &requestReportAcc{
						vendor: acc.vendor, runtime: acc.runtime, billing: acc.billing,
						endpoint: acc.endpoint, endpointKnown: acc.endpointKnown,
						costs: make(map[string]float64), byModel: make(map[string]int64),
						byDay: make(map[string]int64),
					}
					providers[key] = merged
				}
				// One contradicted day contradicts the window. Same shape as priceVerifiedAt taking
				// the oldest: a window publishes its bound, not its best day.
				merged.conflict = merged.conflict || acc.conflict
				// Every target gets the same addition. The day row and the window summary are
				// not separate measurements of the corpus, they are the same one folded over
				// different keys — deriving them from anything else is how they drift apart.
				for _, target := range []*requestReportAcc{merged, total, all} {
					target.in += acc.in
					target.out += acc.out
					target.read += acc.read
					target.write += acc.write
					target.requests += acc.requests
					target.priced += acc.priced
					for currency, amount := range acc.costs {
						target.costs[currency] += amount
					}
					// The OLDEST verification date behind the money — a staleness BOUND, so it
					// survives a merge as a minimum, never as "whichever day was folded last".
					if !acc.priceVerifiedAt.IsZero() &&
						(target.priceVerifiedAt.IsZero() || acc.priceVerifiedAt.Before(target.priceVerifiedAt)) {
						target.priceVerifiedAt = acc.priceVerifiedAt
					}
				}
				for model, tokens := range acc.byModel {
					merged.byModel[model] += tokens
				}
				merged.byDay[date] += acc.in + acc.out + acc.read + acc.write
			}
		}
	}
	return assembleRequestReport(window, start, endExclusive, days, dayTotals, providers, all)
}
