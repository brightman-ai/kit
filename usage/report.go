package usage

import (
	"sort"
	"time"

	"github.com/brightman-ai/kit/pricing"
)

// WindowKind names a reporting window.
type WindowKind string

const (
	Window24h WindowKind = "24h" // 最近 24 小时（按日粒度 = 今天）
	Window7d  WindowKind = "7d"
	Window14d WindowKind = "14d"
	Window30d WindowKind = "30d" // 月
)

// ReportRow represents one time-bucketed usage measurement.
type ReportRow struct {
	// Date is the ISO-8601 date string for this bucket (YYYY-MM-DD).
	Date string `json:"date"`
	// InputTokens consumed in this bucket.
	InputTokens int64 `json:"input_tokens"`
	// OutputTokens generated in this bucket. (thinking tokens roll into output —
	// Claude usage has no separate thinking field; see cost.go header.)
	OutputTokens int64 `json:"output_tokens"`
	// CacheReadTokens read from cache in this bucket.
	CacheReadTokens int64 `json:"cache_read_tokens"`
	// CacheCreateTokens written to cache in this bucket (CHG-014 R3 — newly aggregated).
	CacheCreateTokens int64 `json:"cache_create_tokens"`
	// TotalTokens is the sum of all token categories.
	TotalTokens int64 `json:"total_tokens"`
	// Cost is the per-day estimated cost (nil when no model in the day had a
	// known price → honest「—」, never a fabricated default).
	Cost *float64 `json:"cost"`
	// Currency for Cost ("USD"|"CNY"|""). Empty when Cost is nil.
	Currency string `json:"currency,omitempty"`
	// Costs is the lossless multi-currency projection. Cost remains populated
	// only when this bucket has exactly one currency.
	Costs map[string]float64 `json:"costs,omitempty"`
}

// ReportSummary holds aggregate totals for the window.
type ReportSummary struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CacheReadTokens   int64 `json:"cache_read_tokens"`
	CacheCreateTokens int64 `json:"cache_create_tokens"`
	TotalTokens       int64 `json:"total_tokens"`
	// Cost / Currency: window total estimated cost. Cost nil → no priced model.
	Cost     *float64           `json:"cost"`
	Currency string             `json:"currency,omitempty"`
	Costs    map[string]float64 `json:"costs,omitempty"`
	// CostComplete is false when ≥1 model in the window had NO price row (so the
	// shown cost UNDER-counts). UI may flag "≈" / "部分模型无价" honestly.
	CostComplete bool `json:"cost_complete"`
}

// ProviderRow is one (vendor, caller, billing, endpoint) slice of a window's usage.
//
// NOTE FOR CONSUMERS: there can be SEVERAL rows per vendor, and there always could be — one vendor
// reached by two callers, or one caller through two endpoints (direct and relayed), which differ in
// whether their model ids are priceable at all. Group before you display; never index by vendor.
//
// It carries the two money questions on SEPARATE axes, because they have different answers:
//
//	Vendor  — WHO IS OWED. Derived from the model id (see pricing.VendorForModel).
//	Runtime — WHO SPENT IT. The CLI that made the request.
//
// They used to be one field named `Provider` holding runtimeDisplay(runtime), so a codex pane
// talking to DeepSeek produced a row labelled "OpenAI" that the UI then filed under an OpenAI
// subscription. That field is gone rather than redefined: a repurposed field breaks readers
// silently, and silence was the original bug.
//
// The grain is deliberately fine. One vendor can be reached by several callers, one caller can
// reach several vendors, and one pair can span an auth switch mid-window — so a window legitimately
// holds several rows per vendor. Roll-ups are the caller's business; splitting a total is
// impossible, summing rows is not.
type ProviderRow struct {
	// Vendor is the canonical billing subject id ("anthropic"|"openai"|"google"|"moonshot"|
	// "deepseek"|…). EMPTY means the model id named no vendor we know — an honest gap, never a
	// bucket to sweep strays into.
	Vendor string `json:"vendor"`
	// VendorDisplay is that vendor's name as it appears on a human's invoice ("Kimi", not
	// "Moonshot AI"). Empty exactly when Vendor is: naming the unknown is the surface's job,
	// since the surface also shows the raw model id beside it.
	VendorDisplay string `json:"vendor_display,omitempty"`
	// Runtime is the canonical caller kind ("claude"|"codex"|"gemini"|"other"). Data-driven, not
	// an enum: a new agent that produces facts shows up here without a code change.
	Runtime string `json:"runtime"`
	// RuntimeProvider is the endpoint the caller dialled, in the caller's own vocabulary
	// ("openai", "mimo2codex-kimi-coding"). Empty when the transcript records none — claude never
	// does. It is shown, not just used: when a relay's model id turns out to be a facade, the id
	// the user configured is the only handle they have on which traffic this row is.
	RuntimeProvider string `json:"runtime_provider,omitempty"`
	// AttributionBasis says HOW Vendor was decided — model | confirmed | endpoint | unverified
	// (see attribution.go). A row that merges several requests reports the weakest basis it
	// contains, so this is a bound rather than a best case. Empty on legacy payloads.
	AttributionBasis string `json:"attribution_basis,omitempty"`
	// BillingMode is request-time evidence (subscription|api|unknown). A runtime
	// may therefore produce multiple rows in one window after an auth switch.
	BillingMode       string             `json:"billing_mode"`
	BillingCoverage   string             `json:"billing_coverage"`
	InputTokens       int64              `json:"input_tokens"`
	OutputTokens      int64              `json:"output_tokens"`
	CacheReadTokens   int64              `json:"cache_read_tokens"`
	CacheCreateTokens int64              `json:"cache_create_tokens"`
	TotalTokens       int64              `json:"total_tokens"`
	Cost              *float64           `json:"cost"`
	Currency          string             `json:"currency,omitempty"`
	Costs             map[string]float64 `json:"costs,omitempty"`
	// Requests / PricedRequests make this row's own completeness checkable without consulting the
	// window summary. A row whose priced count is short says so — «≈» on this row — instead of
	// inheriting an approximation from some unrelated model in another tab.
	Requests       int `json:"requests"`
	PricedRequests int `json:"priced_requests"`
	// PriceVerifiedAt (YYYY-MM-DD) is the OLDEST price-rule verification date behind this row's
	// money — its staleness BOUND, not its best case. Empty when nothing here was priced.
	PriceVerifiedAt string `json:"price_verified_at,omitempty"`
	// TopModel is the highest-token model id under this row (主要消耗). For an unknown vendor this
	// is the only handle the user has on what they were actually running, so it is never omitted
	// there.
	TopModel string `json:"top_model,omitempty"`
	// UnitPrices are the published rate cards for TopModel, the one the money was computed with
	// first. It exists because a currency SYMBOL is not evidence: Moonshot sells kimi-k3 at both
	// ¥20/M and $3.00/M, and a bare「$85.93」gives the reader no way to tell which list produced
	// it — while being wrong by 6.67× still looks entirely plausible.
	//
	// Describes TopModel only. A row mixing several models states the unit price of its largest
	// contributor, which is a claim about that model, not about the row's total.
	UnitPrices []pricing.RateCard `json:"unit_prices,omitempty"`
	// Spark is the per-day total-token trend for this row (oldest-first).
	Spark []int64 `json:"spark"`
}

// UsageReport is the top-level response shape for GET /api/usage/report.
type UsageReport struct {
	// Window is the requested reporting window ("7d" or "30d").
	Window WindowKind `json:"window"`
	// StartDate is the ISO-8601 date of the first bucket.
	StartDate string `json:"start_date"`
	// EndDate is the ISO-8601 date of the last bucket (today).
	EndDate string `json:"end_date"`
	// Rows contains per-day buckets ordered oldest-first.
	// Rows are included even when zero so the frontend can render a full chart.
	Rows []ReportRow `json:"rows"`
	// Summary is the aggregate over the whole window.
	Summary ReportSummary `json:"summary"`
	// Providers is the per-provider cost breakdown (settings 报表). Empty when the
	// source can't attribute usage to models (e.g. legacy aggregate-only sources).
	Providers []ProviderRow `json:"providers"`
	// DataSource describes where the data came from.
	DataSource string `json:"data_source"`
	// Available is false when no usage data source is wired up.
	Available bool `json:"available"`
	// Reason explains why Available is false.
	Reason string `json:"reason,omitempty"`
}

// TokenSource is an optional interface that a persistence layer can implement
// to supply per-day token counts.  When nil, BuildReport returns a stub with
// Available=false so callers always receive a well-typed response.
type TokenSource interface {
	// DailyTokens returns (input, output, cacheRead) token totals for the
	// given UTC calendar date (YYYY-MM-DD).  It returns (0,0,0,nil) when the
	// date has no records.
	DailyTokens(date string) (inputTokens, outputTokens, cacheReadTokens int64, err error)
}

// DayTokens is one calendar day's deduplicated token totals (codex H4).
type DayTokens struct {
	InputTokens       int64
	OutputTokens      int64
	CacheReadTokens   int64
	CacheCreateTokens int64
}

// ModelTokens is one (date, endpoint, model) deduplicated token bundle (CHG-014 R3 cost dim).
type ModelTokens struct {
	Date  string
	Model string
	// Provider is the endpoint the caller dialled, when the source records one. It is the same
	// cross-check the request-grain path applies (see attribution.go): without it this path would
	// keep filing relayed traffic under the vendor whose model id the relay borrowed, and the two
	// report paths would disagree about the same corpus.
	Provider string
	// Runtime is the CLI that produced the bundle, when the source knows. Empty falls back to
	// guessing it from the vendor, which cannot be right for a vendor with no CLI of its own.
	Runtime           string
	InputTokens       int64
	OutputTokens      int64
	CacheReadTokens   int64
	CacheCreateTokens int64
}

// ModelScanSource is the richest (optional) source interface: it returns per-(date,
// model) token bundles for the window so BuildReport can price each model with its
// own rate (cost = Σ tokens_model × price_model) and build the per-provider table.
// When a source implements this, BuildReport prefers it over RangeTokenSource.
type ModelScanSource interface {
	ScanModelRange(startDate, endDate string) ([]ModelTokens, error)
}

// RangeTokenSource is an optional, MORE EFFICIENT interface (codex H4): it scans
// the data once for an inclusive [start, end] UTC date range and returns a
// date→totals map. A source implementing it lets BuildReport avoid the old
// O(days) full tree-walk (7 walks for 7d, 30 for 30d). When a TokenSource also
// implements RangeTokenSource, BuildReport prefers ScanRange.
type RangeTokenSource interface {
	// ScanRange returns per-day token totals for every date in the inclusive
	// [startDate, endDate] range (YYYY-MM-DD, UTC) that has records. Dates with
	// no data may be absent from the map (callers fill zeros).
	ScanRange(startDate, endDate string) (map[string]DayTokens, error)
}

// BuildReport assembles a UsageReport for the requested window.
// src may be nil; in that case the report is marked Available=false with
// Reason="no_data_source".
//
// Cost (CHG-014 R3): when src implements ModelScanSource, BuildReport prices each
// (date, model) bundle with its OWN rate (ComputeCost) and sums — so the report
// carries per-day cost, a window total, and a per-provider breakdown. Models with
// no price row are counted in tokens but skipped for cost, and CostComplete is set
// false so the UI can flag the under-count honestly (RED LINE: 缺价不蒙).
func BuildReport(window WindowKind, src TokenSource) UsageReport {
	if src == nil {
		return UsageReport{
			Window:     window,
			Available:  false,
			Reason:     "no_data_source",
			DataSource: "none",
			Rows:       []ReportRow{},
			Providers:  []ProviderRow{},
			Summary:    ReportSummary{},
		}
	}

	days := 7
	switch window {
	case Window24h:
		days = 1
	case Window14d:
		days = 14
	case Window30d:
		days = 30
	}

	now := time.Now().UTC()
	endDate := now.Format("2006-01-02")
	startDate := now.AddDate(0, 0, -(days - 1)).Format("2006-01-02")

	// Richest path: per-(date, model) bundles → real per-model cost + provider table.
	if ms, ok := src.(ModelScanSource); ok {
		if bundles, err := ms.ScanModelRange(startDate, endDate); err == nil {
			return buildFromModelBundles(window, now, days, startDate, endDate, bundles)
		}
	}

	rows := make([]ReportRow, 0, days)
	var summary ReportSummary

	// codex H4: prefer a single-scan ScanRange when the source supports it, so the
	// whole window is one tree-walk instead of `days` walks. Fall back to the
	// per-day DailyTokens path (lookup from a prebuilt bucket map) otherwise.
	var buckets map[string]DayTokens
	if rs, ok := src.(RangeTokenSource); ok {
		if b, err := rs.ScanRange(startDate, endDate); err == nil {
			buckets = b
		}
	}

	for i := days - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		var in, out, cacheRead int64
		if buckets != nil {
			bt := buckets[d] // zero-value when the day has no records
			in, out, cacheRead = bt.InputTokens, bt.OutputTokens, bt.CacheReadTokens
		} else {
			var err error
			in, out, cacheRead, err = src.DailyTokens(d)
			if err != nil {
				// On error we still emit the row with zeros rather than aborting.
				in, out, cacheRead = 0, 0, 0
			}
		}
		total := in + out + cacheRead
		rows = append(rows, ReportRow{
			Date:            d,
			InputTokens:     in,
			OutputTokens:    out,
			CacheReadTokens: cacheRead,
			TotalTokens:     total,
		})
		summary.InputTokens += in
		summary.OutputTokens += out
		summary.CacheReadTokens += cacheRead
		summary.TotalTokens += total
	}

	return UsageReport{
		Window:     window,
		StartDate:  startDate,
		EndDate:    endDate,
		Rows:       rows,
		Providers:  []ProviderRow{},
		Summary:    summary,
		DataSource: "token_source",
		Available:  true,
	}
}

// runtimeGuessForVendor infers WHICH CLI probably made a request from WHOSE model answered it.
//
// It is a guess and named as one. It exists for exactly one caller: the legacy token-source report,
// which reconstructs usage from per-model token bundles and has no runtime evidence at all. Every
// path that HAS the evidence (a transcript says which CLI wrote it) must use that instead — the
// inverse direction is not sound, since any CLI can now call any vendor.
func runtimeGuessForVendor(v pricing.Vendor) string {
	switch v.ID {
	case pricing.VendorAnthropic.ID:
		return "claude"
	case pricing.VendorOpenAI.ID:
		return "codex"
	case pricing.VendorGoogle.ID:
		return "gemini"
	default:
		return "other"
	}
}

// buildFromModelBundles prices each (date, model) bundle and assembles per-day rows,
// the window summary (with cost), and the per-provider breakdown table.
func buildFromModelBundles(window WindowKind, now time.Time, days int, startDate, endDate string, bundles []ModelTokens) UsageReport {
	// Per-day accumulator (token + cost) and per-provider accumulator.
	type dayAcc struct {
		in, out, cr, cc int64
		cost            float64
		hasCost         bool
		currency        string
	}
	dayMap := make(map[string]*dayAcc, days)

	type provAcc struct {
		vendor          pricing.Vendor
		runtime         string
		endpoint        string
		endpointKnown   bool
		conflict        bool
		in, out, cr, cc int64
		cost            float64
		hasCost         bool
		currency        string
		byModel         map[string]int64 // model → total tokens (主要消耗)
		byDay           map[string]int64 // date → total tokens (spark)
	}
	provMap := make(map[string]*provAcc)

	var summary ReportSummary
	anyMissingPrice := false
	anyPrice := false
	summaryCurrency := ""

	for _, b := range bundles {
		total := b.InputTokens + b.OutputTokens + b.CacheReadTokens + b.CacheCreateTokens
		// Skip zero-token bundles: synthetic/terminal transcript rows (model="<synthetic>")
		// carry no usage. Counting them would falsely trip CostComplete=false (no price row)
		// while adding nothing to tokens or cost.
		if total == 0 {
			continue
		}
		da := dayMap[b.Date]
		if da == nil {
			da = &dayAcc{}
			dayMap[b.Date] = da
		}
		da.in += b.InputTokens
		da.out += b.OutputTokens
		da.cr += b.CacheReadTokens
		da.cc += b.CacheCreateTokens

		summary.InputTokens += b.InputTokens
		summary.OutputTokens += b.OutputTokens
		summary.CacheReadTokens += b.CacheReadTokens
		summary.CacheCreateTokens += b.CacheCreateTokens
		summary.TotalTokens += total

		attributed := attributeUsage(b.Provider, b.Model)
		vendor := attributed.Vendor
		// Same identity grain as the request-grain path: two endpoints reaching one vendor are two
		// spends, and only one of them may be priceable.
		key := vendor.ID + "\x00" + b.Provider
		pa := provMap[key]
		if pa == nil {
			// The caller is EVIDENCE when the source recorded one; the vendor-shaped guess is only
			// for sources that cannot say. Without this a relayed codex bundle came out as
			// runtime "other", because Moonshot has no first-party CLI to guess back to.
			runtime := b.Runtime
			if runtime == "" {
				runtime = runtimeGuessForVendor(vendor)
			}
			pa = &provAcc{
				vendor: vendor, runtime: runtime, endpoint: b.Provider,
				endpointKnown: attributed.EndpointKnown,
				byModel:       map[string]int64{}, byDay: map[string]int64{},
			}
			provMap[key] = pa
		}
		pa.conflict = pa.conflict || attributed.Conflict
		pa.in += b.InputTokens
		pa.out += b.OutputTokens
		pa.cr += b.CacheReadTokens
		pa.cc += b.CacheCreateTokens
		pa.byModel[b.Model] += total
		pa.byDay[b.Date] += total

		cr := CostResult{}
		if attributed.Priceable {
			cr = ComputeCost(b.Model, b.InputTokens, b.OutputTokens, b.CacheReadTokens, b.CacheCreateTokens)
		}
		if cr.HasPrice {
			anyPrice = true
			da.cost += cr.TotalCost
			da.hasCost = true
			da.currency = cr.Currency
			pa.cost += cr.TotalCost
			pa.hasCost = true
			pa.currency = cr.Currency
			// Summary currency = first priced currency seen. Mixed USD+CNY setups
			// would sum across currencies (rare; real claude-only data is USD). The
			// per-provider rows keep their own currency, so the breakdown stays exact.
			if summaryCurrency == "" {
				summaryCurrency = cr.Currency
			}
		} else {
			anyMissingPrice = true
		}
	}

	// Per-day rows (oldest-first; zero days included for a full chart).
	rows := make([]ReportRow, 0, days)
	var windowCost float64
	for i := days - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		da := dayMap[d]
		row := ReportRow{Date: d}
		if da != nil {
			row.InputTokens = da.in
			row.OutputTokens = da.out
			row.CacheReadTokens = da.cr
			row.CacheCreateTokens = da.cc
			row.TotalTokens = da.in + da.out + da.cr + da.cc
			if da.hasCost {
				c := round4(da.cost)
				row.Cost = &c
				row.Currency = da.currency
				windowCost += da.cost
			}
		}
		rows = append(rows, row)
	}

	if anyPrice {
		c := round4(windowCost)
		summary.Cost = &c
		summary.Currency = summaryCurrency
	}
	summary.CostComplete = anyPrice && !anyMissingPrice

	// Per-provider rows (sorted by total tokens desc).
	// Skip zero-token providers: synthetic/terminal transcript rows (model="<synthetic>")
	// carry no usage and would otherwise render an empty 0-token provider line.
	providers := make([]ProviderRow, 0, len(provMap))
	for _, pa := range provMap {
		if pa.in+pa.out+pa.cr+pa.cc == 0 {
			continue
		}
		pr := ProviderRow{
			Vendor:            pa.vendor.ID,
			VendorDisplay:     pa.vendor.Display,
			Runtime:           pa.runtime,
			RuntimeProvider:   pa.endpoint,
			AttributionBasis:  derivedBasis(pa.endpoint, pa.endpointKnown, pa.conflict),
			InputTokens:       pa.in,
			OutputTokens:      pa.out,
			CacheReadTokens:   pa.cr,
			CacheCreateTokens: pa.cc,
			TotalTokens:       pa.in + pa.out + pa.cr + pa.cc,
			TopModel:          topKey(pa.byModel),
			Spark:             daySpark(now, days, pa.byDay),
		}
		if pa.hasCost {
			c := round4(pa.cost)
			pr.Cost = &c
			pr.Currency = pa.currency
		}
		providers = append(providers, pr)
	}
	sort.SliceStable(providers, func(i, j int) bool {
		return providers[i].TotalTokens > providers[j].TotalTokens
	})

	return UsageReport{
		Window:     window,
		StartDate:  startDate,
		EndDate:    endDate,
		Rows:       rows,
		Providers:  providers,
		Summary:    summary,
		DataSource: "claude_jsonl",
		Available:  true,
	}
}

// topKey returns the map key with the largest value ("" when empty).
func topKey(m map[string]int64) string {
	best, bestV := "", int64(-1)
	for k, v := range m {
		if v > bestV {
			best, bestV = k, v
		}
	}
	return best
}

// daySpark returns the per-day total-token trend (oldest-first) for a provider.
func daySpark(now time.Time, days int, byDay map[string]int64) []int64 {
	out := make([]int64, 0, days)
	for i := days - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		out = append(out, byDay[d])
	}
	return out
}
