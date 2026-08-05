package usage

import (
	"reflect"
	"testing"
	"time"

	"github.com/brightman-ai/kit/transcript"
)

// The reason day aggregates exist is so a caller can keep ONE per source (per transcript
// file) and re-fold only what moved. That is worth nothing unless the fold is exact: folding
// N separately-aggregated sources must equal aggregating the union in one pass, down to the
// last rounded cent, in every window.
func TestFoldingDailySourcesEqualsAggregatingThemTogether(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, loc)
	at := func(day, hour int) time.Time { return time.Date(2026, 7, day, hour, 0, 0, 0, loc) }

	// Three sources that overlap in days, vendors, callers and billing modes — the merge has
	// to be right on every axis of the (vendor, caller, billing) key, not just on totals.
	sources := [][]transcript.ModelRequestUsage{
		{
			{ID: "c1", Runtime: "codex", Model: "gpt-5.6-sol", ServiceTier: "priority", BillingMode: "subscription", At: at(14, 1), InputTokens: 2790, CachedInputTokens: 9000, OutputTokens: 210},
			{ID: "c2", Runtime: "codex", Model: "gpt-5.6-sol", ServiceTier: "standard", BillingMode: "api", At: at(18, 9), InputTokens: 500, OutputTokens: 60},
		},
		{
			{ID: "a1", Runtime: "claude", Model: "claude-sonnet-5", ServiceTier: "standard", At: at(14, 2), InputTokens: 52, CachedInputTokens: 1_710_200, CacheWrite1hTokens: 78_151, OutputTokens: 22_556},
			{ID: "a2", Runtime: "claude", Model: "claude-opus-5", ServiceTier: "standard", At: at(20, 3), InputTokens: 900, OutputTokens: 400},
			// Same day and same identity as a source-1 fact: the merge must ADD, not replace.
			{ID: "c3", Runtime: "codex", Model: "gpt-5.6-sol", ServiceTier: "priority", BillingMode: "subscription", At: at(14, 5), InputTokens: 100, OutputTokens: 10},
		},
		{
			{ID: "k1", Runtime: "codex", Model: "kimi-k3", ServiceTier: "standard", At: at(19, 11), InputTokens: 4000, OutputTokens: 800},
			// A model with no price rule: priced < requests must survive the fold.
			{ID: "z1", Runtime: "codex", Model: "no-such-model-9", At: at(19, 12), InputTokens: 10, OutputTokens: 1},
			// A zero-token row, which the report drops — the drop must survive too.
			{ID: "z2", Runtime: "claude", Model: "<synthetic>", At: at(19, 13)},
		},
	}

	var union []transcript.ModelRequestUsage
	folded := make([]map[string]DailyUsage, 0, len(sources))
	for _, source := range sources {
		union = append(union, source...)
		folded = append(folded, AggregateDaily("Asia/Shanghai", source))
	}

	for _, window := range []WindowKind{Window24h, Window7d, Window14d, Window30d} {
		want := BuildRequestReport(window, "Asia/Shanghai", now, union)
		got := BuildRequestReportFromDaily(window, "Asia/Shanghai", now, folded)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("window %s: folded sources disagree with a single pass\n got=%+v\nwant=%+v", window, got, want)
		}
	}
}

// A day is a timezone's notion. Aggregates keyed by one zone's calendar cannot be folded
// into another zone's report — the dates mean different instants — so they are refused
// rather than mis-dated.
func TestDailyAggregatesAreRefusedByAnotherTimezone(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, loc)
	facts := []transcript.ModelRequestUsage{
		{ID: "c1", Runtime: "codex", Model: "gpt-5.6-sol", ServiceTier: "standard", At: time.Date(2026, 7, 20, 1, 0, 0, 0, loc), InputTokens: 500, OutputTokens: 60},
	}
	shanghai := AggregateDaily("Asia/Shanghai", facts)
	if got := BuildRequestReportFromDaily(Window7d, "Asia/Shanghai", now, []map[string]DailyUsage{shanghai}); got.Summary.TotalTokens != 560 {
		t.Fatalf("own timezone lost tokens: %+v", got.Summary)
	}
	if got := BuildRequestReportFromDaily(Window7d, "America/New_York", now, []map[string]DailyUsage{shanghai}); got.Summary.TotalTokens != 0 {
		t.Fatalf("another timezone's days were folded in anyway: %+v", got.Summary)
	}
}
