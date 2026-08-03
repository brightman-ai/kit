package pricing

import (
	"testing"
	"time"
)

// The second pricing hop must widen REACH without widening GUESSING. Each test below is one half
// of that bargain.

// A model upstream publishes and no human ever typed is now priceable. Before this hop, 289
// generated ids were unreachable from request pricing and every one of them rendered「—」.
func TestQuoteFromSnapshot_ReachesTheGeneratedTable(t *testing.T) {
	for _, model := range []string{"o4-mini", "chatgpt-4o-latest", "kimi-k2.6"} {
		if _, ok := DefaultCatalog().Quote(RequestQuery{Model: model, At: time.Now(), ServiceTier: "standard"}); ok {
			t.Fatalf("%s is now in the hand catalog — pick a generated-only model for this test", model)
		}
		quote, ok := QuoteFromSnapshot(model, "default")
		if !ok {
			t.Errorf("%s: no snapshot quote", model)
			continue
		}
		if quote.Price.InputPerM <= 0 || quote.Price.OutputPerM <= 0 {
			t.Errorf("%s: snapshot quote has no rate: %+v", model, quote.Price)
		}
		if quote.SourceURL != GeneratedSource {
			t.Errorf("%s: SourceURL = %q, want the upstream url — a generated price must not imply a human read the vendor's page", model, quote.SourceURL)
		}
		if quote.VerifiedAt.Format(time.DateOnly) != GeneratedSnapshot {
			t.Errorf("%s: VerifiedAt = %s, want the snapshot date %s", model, quote.VerifiedAt.Format(time.DateOnly), GeneratedSnapshot)
		}
	}
}

// The family fallback stays OUT of request pricing. lookupLegacy lets "gemini" answer for an
// unknown gemini model, which is right for "roughly what does this cost" and wrong for a bill.
func TestQuoteFromSnapshot_RefusesFamilyFallbacks(t *testing.T) {
	// A plausible future id that only a family rule could match. Lookup finds it; this must not.
	const future = "gemini-9-ultra"
	if _, ok := Lookup(future); !ok {
		t.Fatalf("%s no longer matches any family rule — pick another id, this test needs one that does", future)
	}
	if quote, ok := QuoteFromSnapshot(future, "standard"); ok {
		t.Errorf("%s was priced by a family fallback (%s) — an unpriced model must stay unpriced", future, quote.RuleID)
	}
}

// A tier we have no published price for stays unpriced. Charging a priority request at the standard
// rate does not fail loudly, it under-reports — and an under-report reads exactly like a real
// number.
func TestQuoteFromSnapshot_RefusesNonStandardTiers(t *testing.T) {
	// o4-mini is generated-only, so no catalog rule can be covering for the tier check.
	if _, ok := QuoteFromSnapshot("o4-mini", "priority"); ok {
		t.Error("priority request priced off a standard-tier table entry")
	}
	if _, ok := QuoteFromSnapshot("o4-mini", "default"); !ok {
		t.Error("standard tier should still resolve — the tier guard has over-reached")
	}
}

// Curated entries keep their own provenance rather than borrowing upstream's. They are a different
// claim: a human transcribed them, on a different day, from a different place.
func TestQuoteFromSnapshot_CuratedEntriesCarryTheHandSnapshotDate(t *testing.T) {
	// claude-3-5-haiku is an exact curated entry with no effective-dated rule behind it.
	quote, ok := QuoteFromSnapshot("claude-3-5-haiku", "standard")
	if !ok {
		t.Fatal("curated exact entry did not resolve")
	}
	if quote.SourceURL != "" {
		t.Errorf("curated quote claims SourceURL %q; the hand table has no per-entry source", quote.SourceURL)
	}
	if !quote.VerifiedAt.Equal(catalogSnapshotDate) {
		t.Errorf("curated quote VerifiedAt = %s, want the hand snapshot date %s",
			quote.VerifiedAt.Format(time.DateOnly), catalogSnapshotDate.Format(time.DateOnly))
	}
	// Curated rows are the only place split cache-write TTLs exist; the hop must not flatten them.
	if quote.Price.CacheWrite1hPerM == 0 {
		t.Error("curated quote lost its 1h cache-write rate — the second hop must preserve the detail the hand table exists for")
	}
}

// The hand table wins on an exact id it shares with upstream: it is the reviewed one, and it is the
// only one carrying cache-TTL and long-context detail.
func TestQuoteFromSnapshot_CuratedBeatsGeneratedOnTheSameID(t *testing.T) {
	const shared = "claude-opus-5" // present in both tables
	if _, ok := generatedByModel[normalize(shared)]; !ok {
		t.Skip("upstream no longer carries " + shared + "; the precedence it tested is untestable here")
	}
	quote, ok := QuoteFromSnapshot(shared, "standard")
	if !ok {
		t.Fatal("shared id did not resolve")
	}
	if quote.SourceURL == GeneratedSource {
		t.Error("generated entry outranked the curated one on a shared exact id")
	}
}

// An id in neither table is still the honest miss. Breadth was never permission to guess.
func TestQuoteFromSnapshot_UnknownStaysUnpriced(t *testing.T) {
	for _, model := range []string{"zzz-unknown-1", "", "<synthetic>"} {
		if _, ok := QuoteFromSnapshot(model, "standard"); ok {
			t.Errorf("%q was priced", model)
		}
	}
}
