package usage

import (
	"testing"
	"time"

	"github.com/brightman-ai/kit/transcript"
)

// The load-bearing invariants of the (vendor, caller, billing) axis. Each one is a specific way
// the old single-axis report was wrong, pinned so it cannot come back quietly.

func factAt(id, runtime, model, billing string, at time.Time, in, out int64) transcript.ModelRequestUsage {
	return transcript.ModelRequestUsage{
		ID: id, Runtime: runtime, Model: model, BillingMode: billing, At: at,
		// A tier is mandatory evidence for pricing (request_cost.go treats a missing one as
		// unpriced), and every real rollout carries one. "default" is what codex writes.
		ServiceTier:  "default",
		InputTokens:  in,
		OutputTokens: out,
	}
}

func rowFor(t *testing.T, rep UsageReport, vendor, runtime string) *ProviderRow {
	t.Helper()
	for i := range rep.Providers {
		if rep.Providers[i].Vendor == vendor && rep.Providers[i].Runtime == runtime {
			return &rep.Providers[i]
		}
	}
	return nil
}

// I1 — a third-party model reached through a first-party CLI is attributed to the third party.
//
// This is the original defect, at its source: codex talking to DeepSeek used to produce a row
// labelled OpenAI, which the UI then filed under an OpenAI subscription. DeepSeek tokens cannot
// consume OpenAI quota, so the row was not merely mislabelled — it was arithmetically impossible.
func TestRequestReport_ThirdPartyModelIsNotAttributedToTheCLIsVendor(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	rep := BuildRequestReport(Window7d, "UTC", now, []transcript.ModelRequestUsage{
		factAt("a", "codex", "deepseek-v4-flash", "unknown", now.Add(-time.Hour), 1_000_000, 100_000),
		factAt("b", "codex", "gpt-5.6-sol", "unknown", now.Add(-time.Hour), 1_000_000, 100_000),
		factAt("c", "codex", "k3", "unknown", now.Add(-time.Hour), 1_000_000, 100_000),
	})
	for _, want := range []string{"deepseek", "openai", "moonshot"} {
		row := rowFor(t, rep, want, "codex")
		if row == nil {
			t.Fatalf("no row for vendor %q via codex; got %+v", want, rep.Providers)
		}
		if row.Runtime != "codex" {
			t.Errorf("vendor %s: runtime = %q, want codex (the caller is still codex)", want, row.Runtime)
		}
	}
	// One CLI, three vendors, three rows — the merge that produced the bug is gone.
	if len(rep.Providers) != 3 {
		t.Fatalf("providers = %d, want 3 (one per vendor); got %+v", len(rep.Providers), rep.Providers)
	}
}

// A2 — one vendor reached by several callers keeps the callers apart, so the UI can show "who
// spent it" beneath the vendor without the backend having to know about that layout.
func TestRequestReport_OneVendorManyCallers(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	rep := BuildRequestReport(Window7d, "UTC", now, []transcript.ModelRequestUsage{
		factAt("a", "codex", "k3", "unknown", now.Add(-time.Hour), 1_000_000, 0),
		factAt("b", "claude", "k3", "unknown", now.Add(-time.Hour), 2_000_000, 0),
		factAt("c", "whale", "k3", "unknown", now.Add(-time.Hour), 3_000_000, 0),
	})
	if len(rep.Providers) != 3 {
		t.Fatalf("providers = %d, want 3 callers under one vendor; got %+v", len(rep.Providers), rep.Providers)
	}
	for _, caller := range []string{"codex", "claude", "whale"} {
		row := rowFor(t, rep, "moonshot", caller)
		if row == nil {
			t.Fatalf("no moonshot row for caller %q — the caller axis must be data-driven, not an enum", caller)
		}
	}
}

// An auth switch mid-window is two different bills for the same vendor+caller, and must stay two
// rows. Merging them would put subscription tokens into an API total.
func TestRequestReport_AuthSwitchStaysTwoRows(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	rep := BuildRequestReport(Window7d, "UTC", now, []transcript.ModelRequestUsage{
		factAt("a", "codex", "gpt-5.6-sol", "subscription", now.Add(-3*time.Hour), 1_000_000, 0),
		factAt("b", "codex", "gpt-5.6-sol", "api", now.Add(-time.Hour), 1_000_000, 0),
	})
	if len(rep.Providers) != 2 {
		t.Fatalf("providers = %d, want 2 (one per billing mode); got %+v", len(rep.Providers), rep.Providers)
	}
}

// I4 — every token lands in exactly one row. A report whose rows do not add up to its own summary
// is one where some usage is invisible in every tab, which is worse than a wrong label.
func TestRequestReport_NoTokenIsLostOrDoubleCounted(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	rep := BuildRequestReport(Window7d, "UTC", now, []transcript.ModelRequestUsage{
		factAt("a", "claude", "claude-opus-5", "subscription", now.Add(-time.Hour), 1_000_000, 500_000),
		factAt("b", "codex", "gpt-5.6-sol", "unknown", now.Add(-time.Hour), 700_000, 200_000),
		factAt("c", "codex", "deepseek-v4-flash", "api", now.Add(-time.Hour), 300_000, 100_000),
		factAt("d", "codex", "zzz-unknown-1", "unknown", now.Add(-time.Hour), 11, 13),
	})
	var sum int64
	var requests int
	for _, row := range rep.Providers {
		sum += row.TotalTokens
		requests += row.Requests
	}
	if sum != rep.Summary.TotalTokens {
		t.Errorf("provider rows total %d tokens, summary says %d — usage is being dropped or duplicated", sum, rep.Summary.TotalTokens)
	}
	if requests != 4 {
		t.Errorf("provider rows account for %d requests, want 4", requests)
	}
}

// I5 — an unrecognised model gets an honestly empty vendor and no price. It must not be swept into
// a known vendor's row: a wrong vendor is a wrong currency is a wrong number, and unlike a blank it
// never prompts anyone to go look.
func TestRequestReport_UnknownModelIsNotAbsorbed(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	rep := BuildRequestReport(Window7d, "UTC", now, []transcript.ModelRequestUsage{
		factAt("a", "codex", "gpt-5.6-sol", "unknown", now.Add(-time.Hour), 1_000_000, 0),
		factAt("b", "codex", "zzz-unknown-1", "unknown", now.Add(-time.Hour), 2_000_000, 0),
	})
	unknown := rowFor(t, rep, "", "codex")
	if unknown == nil {
		t.Fatalf("unknown model produced no row of its own; got %+v", rep.Providers)
	}
	if unknown.VendorDisplay != "" {
		t.Errorf("unknown vendor display = %q, want empty (the surface names the unknown, not kit)", unknown.VendorDisplay)
	}
	if unknown.Cost != nil || unknown.PricedRequests != 0 {
		t.Errorf("unknown model was priced: cost=%v priced=%d — a near-miss price is a fabricated bill",
			unknown.Cost, unknown.PricedRequests)
	}
	// The raw model id is the ONLY handle the user has here, so it must survive.
	if unknown.TopModel != "zzz-unknown-1" {
		t.Errorf("unknown row TopModel = %q, want the raw model id", unknown.TopModel)
	}
	if known := rowFor(t, rep, "openai", "codex"); known == nil || known.TotalTokens != 1_000_000 {
		t.Errorf("the openai row absorbed the unknown model's tokens: %+v", known)
	}
}

// A bucket with no tokens is not usage. claude writes `model:"<synthetic>"` placeholder rows into
// every transcript; shown, they become a「未知厂商 · 0 tok · —」line that is three unknowns and no
// fact. Dropping them cannot disturb the sum-to-summary invariant, because the sum they contribute
// is zero.
func TestRequestReport_ZeroTokenRowsAreNotShown(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	rep := BuildRequestReport(Window7d, "UTC", now, []transcript.ModelRequestUsage{
		factAt("a", "claude", "claude-opus-5", "subscription", now.Add(-time.Hour), 1_000_000, 0),
		factAt("b", "claude", "<synthetic>", "unknown", now.Add(-time.Hour), 0, 0),
		factAt("c", "claude", "<synthetic>", "unknown", now.Add(-time.Hour), 0, 0),
	})
	if len(rep.Providers) != 1 {
		t.Fatalf("providers = %d, want 1 (the synthetic rows carry no usage); got %+v", len(rep.Providers), rep.Providers)
	}
	var sum int64
	for _, row := range rep.Providers {
		sum += row.TotalTokens
	}
	if sum != rep.Summary.TotalTokens {
		t.Errorf("dropping zero-token rows broke the sum: rows=%d summary=%d", sum, rep.Summary.TotalTokens)
	}
}

// Per-row completeness. A row that could not price everything says so on ITSELF, so the「≈」on one
// tab is never caused by an unpriced model sitting in the other one.
func TestRequestReport_RowCarriesItsOwnPriceCompleteness(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	rep := BuildRequestReport(Window7d, "UTC", now, []transcript.ModelRequestUsage{
		factAt("a", "claude", "claude-opus-5", "subscription", now.Add(-time.Hour), 1_000_000, 0),
		factAt("b", "codex", "gpt-5.6-sol", "unknown", now.Add(-time.Hour), 1_000_000, 0),
		factAt("c", "codex", "zzz-unknown-1", "unknown", now.Add(-time.Hour), 1_000_000, 0),
	})
	claude := rowFor(t, rep, "anthropic", "claude")
	if claude == nil || claude.Requests != 1 || claude.PricedRequests != 1 {
		t.Fatalf("anthropic row should be fully priced, got %+v", claude)
	}
	// The window as a whole is incomplete because of the unknown model — but that must not make
	// the anthropic row claim incompleteness it does not have.
	if rep.Summary.CostComplete {
		t.Error("summary claims complete pricing while an unpriced model is present")
	}
}

// Every priced row publishes how old its prices are. An embedded table cannot detect that a vendor
// changed its rates; saying when we last checked is the only defence the reader gets.
func TestRequestReport_PricedRowPublishesItsPriceAge(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	rep := BuildRequestReport(Window7d, "UTC", now, []transcript.ModelRequestUsage{
		factAt("a", "codex", "k3", "api", now.Add(-time.Hour), 1_000_000, 0),
		factAt("b", "codex", "zzz-unknown-1", "api", now.Add(-time.Hour), 1_000_000, 0),
	})
	kimi := rowFor(t, rep, "moonshot", "codex")
	if kimi == nil || kimi.PriceVerifiedAt == "" {
		t.Fatalf("priced row carries no price age: %+v", kimi)
	}
	if _, err := time.Parse("2006-01-02", kimi.PriceVerifiedAt); err != nil {
		t.Errorf("PriceVerifiedAt = %q, want YYYY-MM-DD: %v", kimi.PriceVerifiedAt, err)
	}
	// Nothing was priced here, so there is no price age to state. An empty string, not a date.
	if unknown := rowFor(t, rep, "", "codex"); unknown == nil || unknown.PriceVerifiedAt != "" {
		t.Errorf("unpriced row invented a price age: %+v", unknown)
	}
}

// Request pricing reaches the generated table, not just the hand-written catalog.
//
// This is the half of the two-hop design that is easy to build and forget to connect: gen-table can
// pull 289 exact ids and every one of them still renders「—」if ProjectRequestCost stops at a
// catalog miss. o4-mini is a real, thoroughly public model that no human entered into catalog.go.
func TestRequestReport_PricesModelsOnlyTheGeneratedTableKnows(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	rep := BuildRequestReport(Window7d, "UTC", now, []transcript.ModelRequestUsage{
		factAt("a", "codex", "o4-mini", "api", now.Add(-time.Hour), 1_000_000, 100_000),
	})
	row := rowFor(t, rep, "openai", "codex")
	if row == nil {
		t.Fatalf("no openai row; got %+v", rep.Providers)
	}
	if row.PricedRequests != 1 || row.Cost == nil {
		t.Fatalf("o4-mini went unpriced: priced=%d cost=%v — the generated table is not wired into request pricing",
			row.PricedRequests, row.Cost)
	}
	if row.PriceVerifiedAt == "" {
		t.Error("a generated price shipped without stating its age")
	}
}

// ...and reaching it must not turn on family guessing. An unknown gemini model can be priced by
// Lookup's "gemini" fallback, which is the right answer for an estimate and the wrong one for a
// row the user reads as money owed.
func TestRequestReport_StillRefusesToGuessUnknownModels(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	rep := BuildRequestReport(Window7d, "UTC", now, []transcript.ModelRequestUsage{
		factAt("a", "gemini", "gemini-9-ultra", "api", now.Add(-time.Hour), 1_000_000, 0),
	})
	row := rowFor(t, rep, "google", "gemini")
	if row == nil {
		t.Fatalf("no google row; got %+v", rep.Providers)
	}
	if row.PricedRequests != 0 || row.Cost != nil {
		t.Errorf("an unpublished model was priced off a family fallback: priced=%d cost=%v", row.PricedRequests, row.Cost)
	}
}

// I6 — money in different currencies is never collapsed into one number.
//
// Tested on the collapse itself rather than through a fixture, because every vendor the request
// path can currently price bills in USD (the CNY entries in the legacy table are not reachable from
// ProjectRequestCost). Waiting for a CNY vendor to appear before testing this would mean the rule
// is unguarded exactly when it first matters.
func TestScalarCost_NeverSumsAcrossCurrencies(t *testing.T) {
	cost, currency := scalarCost(map[string]float64{"USD": 1.5, "CNY": 10})
	if cost != nil || currency != "" {
		t.Errorf("mixed currencies collapsed to %v %s — 「—」 is the only honest answer", *cost, currency)
	}
	cost, currency = scalarCost(map[string]float64{"USD": 1.5})
	if cost == nil || *cost != 1.5 || currency != "USD" {
		t.Errorf("single currency should stay scalar, got %v %q", cost, currency)
	}
	if cost, currency := scalarCost(nil); cost != nil || currency != "" {
		t.Errorf("no money should stay nil, got %v %q", *cost, currency)
	}
}

// A subagent's spend must land under the vendor its PARENT was configured for, not under a
// default. This is the whole accuracy requirement for forks in one assertion: not double-counted,
// not dropped, and not filed under the wrong biller.
//
// The trap it guards is specific. A forked rollout does not declare its own model, so the model —
// and therefore the vendor — comes from the settings inherited across the fork boundary. Lose that
// inheritance and every subagent request resolves to no vendor at all; stamp a default instead and
// a kimi subagent is silently billed to OpenAI. Verified against the live machine when this was
// written: 48 of 48 subagent rollouts agreed with their parent's model, 0 disagreed, and 0 facts
// carried an empty model.
func TestRequestReport_SubagentSpendLandsUnderTheParentsVendor(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	// A subagent of a kimi-configured parent: the model is inherited, so the vendor must be
	// Moonshot even though codex is the caller and OpenAI is the caller's usual home.
	rep := BuildRequestReport(Window7d, "UTC", now, []transcript.ModelRequestUsage{
		factAt("child-1", "codex", "k3", "unknown", now.Add(-time.Hour), 1_000_000, 50_000),
		factAt("child-2", "codex", "k3", "unknown", now.Add(-time.Hour), 500_000, 20_000),
	})
	row := rowFor(t, rep, "moonshot", "codex")
	if row == nil {
		t.Fatalf("subagent spend did not land under moonshot; got %+v", rep.Providers)
	}
	if rowFor(t, rep, "openai", "codex") != nil {
		t.Error("subagent spend also appeared under openai — a caller's usual vendor is not its bill")
	}
	if row.Requests != 2 || row.TotalTokens != 1_570_000 {
		t.Errorf("row = %d requests / %d tokens, want 2 / 1,570,000", row.Requests, row.TotalTokens)
	}
	// And it is money, not just tokens: an inherited tier is what makes the request priceable.
	if row.PricedRequests != 2 || row.Cost == nil {
		t.Errorf("subagent spend went unpriced: priced=%d cost=%v", row.PricedRequests, row.Cost)
	}
}
