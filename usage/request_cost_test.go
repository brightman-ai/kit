package usage

import (
	"testing"
	"time"

	"github.com/brightman-ai/kit/transcript"
)

func requestAt(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		panic(err)
	}
	return t
}

func TestProjectRequestCostCodexMutuallyExclusiveBuckets(t *testing.T) {
	f := transcript.ModelRequestUsage{
		ID: "codex:s:1", Runtime: "codex", Model: "gpt-5.6-sol", ServiceTier: "priority",
		At: requestAt("2026-07-14T01:00:00Z"), InputTokens: 2790, CachedInputTokens: 9000,
		OutputTokens: 210, ReasoningOutputTokens: 40,
	}
	p := ProjectRequestCost(f)
	if !p.Complete || p.APIEquivalent == nil || *p.APIEquivalent != .0495 {
		t.Fatalf("projection=%+v, want priority API equivalent .0495", p)
	}
	if p.Credits == nil || *p.Credits != .61875 {
		t.Fatalf("credits=%v, want .61875", p.Credits)
	}
	if p.FastMultiplier != nil {
		t.Fatalf("priority tier was incorrectly treated as Fast: %+v", p)
	}
}

func TestProjectRequestCostClaudePromoAndTTL(t *testing.T) {
	f := transcript.ModelRequestUsage{
		ID: "claude:s:m1", Runtime: "claude", Model: "claude-sonnet-5", ServiceTier: "standard",
		At: requestAt("2026-07-14T02:00:00Z"), InputTokens: 52, CachedInputTokens: 1_710_200,
		CacheWrite1hTokens: 78_151, OutputTokens: 22_556,
	}
	p := ProjectRequestCost(f)
	if !p.Complete || p.APIEquivalent == nil || *p.APIEquivalent != .880308 {
		t.Fatalf("projection=%+v, want promo cost .880308", p)
	}
	f.CacheWrite1hTokens = 0
	f.CacheWriteUnknownTokens = 78_151
	p = ProjectRequestCost(f)
	if p.Complete || p.APIEquivalent != nil || len(p.Diagnostics) != 1 || p.Diagnostics[0] != "cache_write_ttl_unknown" {
		t.Fatalf("unknown TTL must remain unpriced: %+v", p)
	}
}

func TestProjectRequestCostDoesNotAssumeMissingServiceTier(t *testing.T) {
	f := transcript.ModelRequestUsage{
		ID: "codex:s:missing-tier", Runtime: "codex", Model: "gpt-5.6-sol",
		At: requestAt("2026-07-14T01:00:00Z"), InputTokens: 100, OutputTokens: 20,
	}
	p := ProjectRequestCost(f)
	if p.Complete || p.APIEquivalent != nil || len(p.Diagnostics) != 1 || p.Diagnostics[0] != "service_tier_missing" {
		t.Fatalf("missing tier was silently priced as standard: %+v", p)
	}
}

func TestProjectRequestCostAppliesOnlyExplicitSupportedFastCredits(t *testing.T) {
	f := transcript.ModelRequestUsage{
		ID: "codex:s:fast", Runtime: "codex", Model: "gpt-5.5", ServiceTier: "fast", Speed: "fast",
		At: requestAt("2026-07-14T01:00:00Z"), InputTokens: 100_000,
	}
	p := ProjectRequestCost(f)
	if !p.Complete || p.APIEquivalent == nil || *p.APIEquivalent != .5 {
		t.Fatalf("fast API-equivalent must remain standard price: %+v", p)
	}
	if p.Credits == nil || *p.Credits != 31.25 || p.FastMultiplier == nil || *p.FastMultiplier != 2.5 {
		t.Fatalf("explicit Fast credits=%+v, want 31.25 at 2.5x", p)
	}
	f.Model = "gpt-5.6-sol"
	p = ProjectRequestCost(f)
	if p.FastMultiplier != nil || p.Credits == nil || *p.Credits != 12.5 {
		t.Fatalf("unsupported GPT-5.6 Fast was guessed: %+v", p)
	}
}
