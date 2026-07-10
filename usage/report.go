package usage

import (
	"sort"
	"strings"
	"time"
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
}

// ReportSummary holds aggregate totals for the window.
type ReportSummary struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CacheReadTokens   int64 `json:"cache_read_tokens"`
	CacheCreateTokens int64 `json:"cache_create_tokens"`
	TotalTokens       int64 `json:"total_tokens"`
	// Cost / Currency: window total estimated cost. Cost nil → no priced model.
	Cost     *float64 `json:"cost"`
	Currency string   `json:"currency,omitempty"`
	// CostComplete is false when ≥1 model in the window had NO price row (so the
	// shown cost UNDER-counts). UI may flag "≈" / "部分模型无价" honestly.
	CostComplete bool `json:"cost_complete"`
}

// ProviderRow is one provider/runtime cost-breakdown row (settings 报表 §Gap-3).
// Derived from per-model aggregation: each model id is mapped to a provider class
// + a runtime kind via simple model-name heuristics (claude/codex/gemini/...).
type ProviderRow struct {
	// Provider is the display key (e.g. "Claude" / "OpenAI" / "Gemini").
	Provider string `json:"provider"`
	// Runtime is the canonical runtime kind ("claude"|"codex"|"gemini"|"other").
	Runtime           string   `json:"runtime"`
	InputTokens       int64    `json:"input_tokens"`
	OutputTokens      int64    `json:"output_tokens"`
	CacheReadTokens   int64    `json:"cache_read_tokens"`
	CacheCreateTokens int64    `json:"cache_create_tokens"`
	TotalTokens       int64    `json:"total_tokens"`
	Cost              *float64 `json:"cost"`
	Currency          string   `json:"currency,omitempty"`
	// TopModel is the highest-token model id under this provider (主要消耗).
	TopModel string `json:"top_model,omitempty"`
	// Spark is the per-day total-token trend for this provider (oldest-first).
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

// ModelTokens is one (date, model) deduplicated token bundle (CHG-014 R3 cost dim).
type ModelTokens struct {
	Date              string
	Model             string
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

// providerForModel maps a model id onto a (display, runtime) provider key for the
// per-provider cost breakdown. Mirrors fleet_routes.runtimeFromModel heuristics.
func providerForModel(model string) (display, runtime string) {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude") || strings.Contains(m, "sonnet") ||
		strings.Contains(m, "opus") || strings.Contains(m, "haiku") || strings.Contains(m, "fable"):
		return "Claude", "claude"
	case strings.Contains(m, "gpt") || strings.Contains(m, "codex") ||
		strings.Contains(m, "o1") || strings.Contains(m, "o3"):
		return "OpenAI", "codex"
	case strings.Contains(m, "gemini"):
		return "Gemini", "gemini"
	case strings.Contains(m, "glm"):
		return "GLM", "other"
	case strings.Contains(m, "deepseek"):
		return "DeepSeek", "other"
	case strings.Contains(m, "qwen"):
		return "Qwen", "other"
	default:
		return "其他", "other"
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
		display, runtime string
		in, out, cr, cc  int64
		cost             float64
		hasCost          bool
		currency         string
		byModel          map[string]int64 // model → total tokens (主要消耗)
		byDay            map[string]int64 // date → total tokens (spark)
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

		display, runtime := providerForModel(b.Model)
		pa := provMap[display]
		if pa == nil {
			pa = &provAcc{display: display, runtime: runtime, byModel: map[string]int64{}, byDay: map[string]int64{}}
			provMap[display] = pa
		}
		pa.in += b.InputTokens
		pa.out += b.OutputTokens
		pa.cr += b.CacheReadTokens
		pa.cc += b.CacheCreateTokens
		pa.byModel[b.Model] += total
		pa.byDay[b.Date] += total

		cr := ComputeCost(b.Model, b.InputTokens, b.OutputTokens, b.CacheReadTokens, b.CacheCreateTokens)
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
			Provider:          pa.display,
			Runtime:           pa.runtime,
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
