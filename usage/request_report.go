package usage

import (
	"sort"
	"strings"
	"time"

	"github.com/brightman-ai/kit/transcript"
)

type requestReportAcc struct {
	in, out, read, write int64
	costs                map[string]float64
	priced, requests     int
	byModel              map[string]int64
	byDay                map[string]int64
}

// BuildRequestReport is the compatibility report rebuilt from request-grain
// economic facts. It preserves the existing /usage/report wire shape while
// fixing per-request tier/effective-date/cache-TTL and local-calendar semantics.
func BuildRequestReport(window WindowKind, timezone string, now time.Time, facts []transcript.ModelRequestUsage) UsageReport {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
		timezone = "UTC"
	}
	days := reportDays(window)
	localNow := now.In(loc)
	y, m, d := localNow.Date()
	endExclusive := time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	start := endExclusive.AddDate(0, 0, -days)

	daysAcc := make(map[string]*requestReportAcc)
	runtimes := make(map[string]*requestReportAcc)
	all := &requestReportAcc{costs: make(map[string]float64)}
	for _, fact := range facts {
		if fact.At.Before(start) || !fact.At.Before(endExclusive) {
			continue
		}
		date := fact.At.In(loc).Format("2006-01-02")
		runtime := fact.Runtime
		if runtime == "" {
			_, runtime = providerForModel(fact.Model)
		}
		day := ensureRequestAcc(daysAcc, date)
		billing := normalizeRequestBilling(fact.BillingMode)
		rt := ensureRequestAcc(runtimes, runtime+"\x00"+billing)
		physicalInput := fact.InputTokens
		cacheWrite := fact.CacheWrite5mTokens + fact.CacheWrite1hTokens + fact.CacheWriteUnknownTokens
		physicalTotal := physicalInput + fact.CachedInputTokens + cacheWrite + fact.OutputTokens
		for _, target := range []*requestReportAcc{day, rt, all} {
			target.in += physicalInput
			target.out += fact.OutputTokens
			target.read += fact.CachedInputTokens
			target.write += cacheWrite
			target.requests++
		}
		if rt.byModel == nil {
			rt.byModel = make(map[string]int64)
		}
		if rt.byDay == nil {
			rt.byDay = make(map[string]int64)
		}
		rt.byModel[fact.Model] += physicalTotal
		rt.byDay[date] += physicalTotal
		projection := ProjectRequestCost(fact)
		if projection.Complete && projection.APIEquivalent != nil && projection.Currency != "" {
			for _, target := range []*requestReportAcc{day, rt, all} {
				if target.costs == nil {
					target.costs = make(map[string]float64)
				}
				target.costs[projection.Currency] += *projection.APIEquivalent
				target.priced++
			}
		}
	}

	rows := make([]ReportRow, 0, days)
	for i := 0; i < days; i++ {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		a := daysAcc[date]
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
	for runtimeBilling, a := range runtimes {
		runtime, billing, _ := strings.Cut(runtimeBilling, "\x00")
		display := runtimeDisplay(runtime)
		row := ProviderRow{
			Provider: display, Runtime: runtime, BillingMode: billing, BillingCoverage: billingCoverage(billing), InputTokens: a.in, OutputTokens: a.out,
			CacheReadTokens: a.read, CacheCreateTokens: a.write,
			TotalTokens: a.in + a.out + a.read + a.write, TopModel: topKey(a.byModel),
			Spark: requestDaySpark(start, days, a.byDay), Costs: roundedCosts(a.costs),
		}
		row.Cost, row.Currency = scalarCost(row.Costs)
		providers = append(providers, row)
	}
	sort.Slice(providers, func(i, j int) bool {
		if providers[i].Runtime == providers[j].Runtime {
			return providers[i].BillingMode < providers[j].BillingMode
		}
		return providers[i].TotalTokens > providers[j].TotalTokens
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

func runtimeDisplay(runtime string) string {
	switch runtime {
	case "claude":
		return "Claude"
	case "codex":
		return "OpenAI"
	case "gemini":
		return "Gemini"
	default:
		return "其他"
	}
}

func requestDaySpark(start time.Time, days int, values map[string]int64) []int64 {
	out := make([]int64, 0, days)
	for i := 0; i < days; i++ {
		out = append(out, values[start.AddDate(0, 0, i).Format("2006-01-02")])
	}
	return out
}
