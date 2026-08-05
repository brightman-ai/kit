package usage

import (
	"sort"
	"strings"
	"time"

	"github.com/brightman-ai/kit/pricing"
	"github.com/brightman-ai/kit/transcript"
)

type requestReportAcc struct {
	// Identity of a provider bucket. Held as fields rather than re-parsed out of the map key:
	// the vendor id may legitimately be EMPTY (unknown vendor), and a key-splitting scheme that
	// has to survive an empty component is a bug waiting for its first unknown model.
	vendor  pricing.Vendor
	runtime string
	billing string

	in, out, read, write int64
	costs                map[string]float64
	priced, requests     int
	byModel              map[string]int64
	byDay                map[string]int64
	// priceVerifiedAt is the OLDEST verification date among this bucket's priced requests — a
	// staleness BOUND. Newest would flatter: one freshly-checked model would make a row full of
	// half-year-old prices look current.
	priceVerifiedAt time.Time
}

// BuildRequestReport is the compatibility report rebuilt from request-grain economic facts.
// It preserves the existing /usage/report wire shape while fixing per-request
// tier/effective-date/cache-TTL and local-calendar semantics.
//
// One implementation, expressed as what it is: aggregate the facts by local day, then fold
// the days this window covers. A caller that already holds day aggregates skips the first
// half — see BuildRequestReportFromDaily — and gets the identical number, because there is
// no second copy of the arithmetic for it to disagree with.
func BuildRequestReport(window WindowKind, timezone string, now time.Time, facts []transcript.ModelRequestUsage) UsageReport {
	return BuildRequestReportFromDaily(window, timezone, now,
		[]map[string]DailyUsage{AggregateDaily(timezone, facts)})
}

// assembleRequestReport turns folded accumulators into the wire shape. Shared by both entry
// points so day rows, provider rows and the summary can only ever be three views of one fold.
func assembleRequestReport(window WindowKind, start, endExclusive time.Time, days int,
	dayTotals, runtimes map[string]*requestReportAcc, all *requestReportAcc) UsageReport {
	rows := make([]ReportRow, 0, days)
	for i := 0; i < days; i++ {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		a := dayTotals[date]
		row := ReportRow{Date: date}
		if a != nil {
			row.InputTokens, row.OutputTokens = a.in, a.out
			row.CacheReadTokens, row.CacheCreateTokens = a.read, a.write
			row.TotalTokens = a.in + a.out + a.read + a.write
			row.Costs = roundedCosts(a.costs)
			row.Cost, row.Currency = scalarCost(row.Costs)
		}
		rows = append(rows, row)
	}
	summary := ReportSummary{
		InputTokens: all.in, OutputTokens: all.out, CacheReadTokens: all.read, CacheCreateTokens: all.write,
		TotalTokens: all.in + all.out + all.read + all.write, Costs: roundedCosts(all.costs),
		CostComplete: all.requests > 0 && all.priced == all.requests,
	}
	summary.Cost, summary.Currency = scalarCost(summary.Costs)
	providers := make([]ProviderRow, 0, len(runtimes))
	for _, a := range runtimes {
		// A zero-token bucket is not usage. claude writes `model:"<synthetic>"` placeholder rows
		// into every transcript; on a live machine they arrive as ~10 requests carrying nothing at
		// all. Rendering them costs a line that says「未知厂商 · 0 tok · —」— three unknowns and no
		// fact — and invites the reader to go investigate nothing. The legacy report path has
		// skipped these since it was written; this one was quietly showing them.
		//
		// Safe for the "rows sum to the summary" invariant precisely because the sum they
		// contribute is zero.
		if a.in+a.out+a.read+a.write == 0 {
			continue
		}
		row := ProviderRow{
			Vendor: a.vendor.ID, VendorDisplay: a.vendor.Display,
			Runtime: a.runtime, BillingMode: a.billing, BillingCoverage: billingCoverage(a.billing),
			InputTokens: a.in, OutputTokens: a.out,
			CacheReadTokens: a.read, CacheCreateTokens: a.write,
			TotalTokens: a.in + a.out + a.read + a.write, TopModel: topKey(a.byModel),
			UnitPrices: pricing.PublishedRates(topKey(a.byModel)),
			Requests:   a.requests, PricedRequests: a.priced,
			Spark: requestDaySpark(start, days, a.byDay), Costs: roundedCosts(a.costs),
		}
		if !a.priceVerifiedAt.IsZero() {
			row.PriceVerifiedAt = a.priceVerifiedAt.Format("2006-01-02")
		}
		row.Cost, row.Currency = scalarCost(row.Costs)
		providers = append(providers, row)
	}
	// Biggest spend first, then a total order over the identity so the list is stable across
	// refreshes. Map iteration is random, and a table that reshuffles under a stationary cursor is
	// a table you cannot read.
	sort.Slice(providers, func(i, j int) bool {
		l, r := providers[i], providers[j]
		if l.TotalTokens != r.TotalTokens {
			return l.TotalTokens > r.TotalTokens
		}
		if l.Vendor != r.Vendor {
			return l.Vendor < r.Vendor
		}
		if l.Runtime != r.Runtime {
			return l.Runtime < r.Runtime
		}
		return l.BillingMode < r.BillingMode
	})
	return UsageReport{
		Window: window, StartDate: start.Format("2006-01-02"), EndDate: endExclusive.Add(-time.Nanosecond).Format("2006-01-02"),
		Rows: rows, Summary: summary, Providers: providers, DataSource: "model_request_usage", Available: true,
	}
}

func normalizeRequestBilling(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "subscription", "chatgpt", "plan":
		return "subscription"
	case "api", "api_key":
		return "api"
	default:
		return "unknown"
	}
}

func billingCoverage(mode string) string {
	if mode == "unknown" {
		return "missing"
	}
	return "complete"
}

func reportDays(window WindowKind) int {
	switch window {
	case Window24h:
		return 1
	case Window14d:
		return 14
	case Window30d:
		return 30
	default:
		return 7
	}
}

func ensureRequestAcc(values map[string]*requestReportAcc, key string) *requestReportAcc {
	value := values[key]
	if value == nil {
		value = &requestReportAcc{costs: make(map[string]float64)}
		values[key] = value
	}
	return value
}

// ensureProviderAcc buckets by the FULL identity (vendor, caller, billing).
//
// All three belong in the key. Dropping vendor is the bug being repaid — it merged a DeepSeek
// request into a codex row that then read as OpenAI. Dropping billing would merge the two halves
// of a mid-window auth switch, and dropping the caller would lose the "who spent it" sub-rows the
// UI shows under each vendor. Splitting later is impossible; summing later is trivial.
func ensureProviderAcc(values map[string]*requestReportAcc, vendor pricing.Vendor, runtime, billing string) *requestReportAcc {
	key := vendor.ID + "\x00" + runtime + "\x00" + billing
	if value := values[key]; value != nil {
		return value
	}
	value := &requestReportAcc{
		vendor: vendor, runtime: runtime, billing: billing,
		costs: make(map[string]float64),
	}
	values[key] = value
	return value
}

func roundedCosts(costs map[string]float64) map[string]float64 {
	if len(costs) == 0 {
		return nil
	}
	out := make(map[string]float64, len(costs))
	for currency, amount := range costs {
		out[currency] = round4(amount)
	}
	return out
}

func scalarCost(costs map[string]float64) (*float64, string) {
	if len(costs) != 1 {
		return nil, ""
	}
	for currency, amount := range costs {
		value := amount
		return &value, currency
	}
	return nil, ""
}

func requestDaySpark(start time.Time, days int, values map[string]int64) []int64 {
	out := make([]int64, 0, days)
	for i := 0; i < days; i++ {
		out = append(out, values[start.AddDate(0, 0, i).Format("2006-01-02")])
	}
	return out
}
