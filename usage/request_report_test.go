package usage

import (
	"testing"
	"time"

	"github.com/brightman-ai/kit/transcript"
)

func TestBuildRequestReportRequestPricingAndPhysicalTotals(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, loc)
	facts := []transcript.ModelRequestUsage{
		{ID: "c1", Runtime: "codex", Model: "gpt-5.6-sol", ServiceTier: "priority", At: time.Date(2026, 7, 14, 1, 0, 0, 0, loc), InputTokens: 2790, CachedInputTokens: 9000, OutputTokens: 210, ReasoningOutputTokens: 40},
		{ID: "a1", Runtime: "claude", Model: "claude-sonnet-5", ServiceTier: "standard", At: time.Date(2026, 7, 14, 2, 0, 0, 0, loc), InputTokens: 52, CachedInputTokens: 1_710_200, CacheWrite1hTokens: 78_151, OutputTokens: 22_556},
	}
	report := BuildRequestReport(Window24h, "Asia/Shanghai", now, facts)
	if report.Summary.InputTokens != 2842 || report.Summary.CacheReadTokens != 1_719_200 || report.Summary.OutputTokens != 22_766 || report.Summary.CacheCreateTokens != 78_151 {
		t.Fatalf("summary=%+v", report.Summary)
	}
	wantCost := round4(.0495 + .880308)
	if report.Summary.Cost == nil || *report.Summary.Cost != wantCost || !report.Summary.CostComplete {
		t.Fatalf("cost=%v complete=%v, want %v complete", report.Summary.Cost, report.Summary.CostComplete, wantCost)
	}
	// Reasoning and cached tokens are breakdowns, not additions beyond physical buckets.
	wantTotal := int64(2842 + 1_719_200 + 22_766 + 78_151)
	if report.Summary.TotalTokens != wantTotal {
		t.Fatalf("physical total=%d want=%d", report.Summary.TotalTokens, wantTotal)
	}
}

func TestBuildRequestReportUsesLocalCalendar(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 7, 14, 1, 0, 0, 0, loc)
	inside := time.Date(2026, 7, 13, 16, 30, 0, 0, time.UTC)  // Jul 14 00:30 Shanghai
	outside := time.Date(2026, 7, 13, 15, 30, 0, 0, time.UTC) // Jul 13 23:30 Shanghai
	facts := []transcript.ModelRequestUsage{
		{ID: "in", Runtime: "codex", Model: "gpt-5.6-luna", At: inside, InputTokens: 10},
		{ID: "out", Runtime: "codex", Model: "gpt-5.6-luna", At: outside, InputTokens: 100},
	}
	report := BuildRequestReport(Window24h, "Asia/Shanghai", now, facts)
	if report.Summary.InputTokens != 10 || report.StartDate != "2026-07-14" {
		t.Fatalf("local calendar report=%+v", report)
	}
}

func TestBuildRequestReportKeepsRequestTimeBillingSwitchSeparate(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, loc)
	facts := []transcript.ModelRequestUsage{
		{ID: "sub", Runtime: "codex", Model: "gpt-5.6-luna", ServiceTier: "standard", BillingMode: "subscription", At: now.Add(-2 * time.Hour), InputTokens: 100},
		{ID: "api", Runtime: "codex", Model: "gpt-5.6-luna", ServiceTier: "standard", BillingMode: "api", At: now.Add(-time.Hour), InputTokens: 200},
	}
	report := BuildRequestReport(Window24h, "Asia/Shanghai", now, facts)
	if len(report.Providers) != 2 {
		t.Fatalf("billing switch collapsed into current mode: %+v", report.Providers)
	}
	byMode := make(map[string]ProviderRow)
	for _, provider := range report.Providers {
		byMode[provider.BillingMode] = provider
	}
	if byMode["subscription"].InputTokens != 100 || byMode["api"].InputTokens != 200 || byMode["api"].BillingCoverage != "complete" {
		t.Fatalf("request-time billing rows=%+v", report.Providers)
	}

	facts[0].BillingMode = ""
	report = BuildRequestReport(Window24h, "Asia/Shanghai", now, facts[:1])
	if len(report.Providers) != 1 || report.Providers[0].BillingMode != "unknown" || report.Providers[0].BillingCoverage != "missing" {
		t.Fatalf("missing billing evidence was backfilled: %+v", report.Providers)
	}
}
