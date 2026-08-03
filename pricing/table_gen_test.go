package pricing

import "testing"

// The generated table's whole reason to exist: models nobody hand-typed now have a price instead
// of rendering 「—」 and reading as a broken panel. These four were unpriced before it landed.
func TestGeneratedTableCoversModelsTheHandTableNeverHad(t *testing.T) {
	for _, model := range []string{"o4-mini", "chatgpt-4o-latest", "grok-4", "mistral-large-latest"} {
		if _, ok := lookupLegacy(model); !ok {
			t.Errorf("%s: still unpriced — the generated table should carry it", model)
		}
		if v := VendorForModel(model); !v.Known() {
			t.Errorf("%s: priced but unattributed", model)
		}
	}
}

// The load-bearing half of the layering, and the one a refactor would quietly break: an EXACT id
// must outrank a FAMILY key, no matter which table each lives in.
//
// table.go carries a generic "gemini" fallback. Before the generated table was consulted between
// exact and family matching, any gemini id without its own hand entry took that fallback's rate —
// so a model with a published price of its own would be billed at a neighbour's, silently and
// forever. The assertion is deliberately "not the family rate" rather than a pinned number: the
// point is precedence, and pinning upstream's number here would make a routine price refresh look
// like a regression.
func TestExactPriceBeatsFamilyFallback(t *testing.T) {
	family, ok := lookupLegacy("gemini-there-is-no-such-model")
	if !ok {
		t.Fatal("expected the generic gemini family key to answer an unknown gemini id")
	}
	exact, ok := lookupLegacy("gemini-2.0-flash")
	if !ok {
		t.Fatal("gemini-2.0-flash should be priced by the generated table")
	}
	if exact.InputPerM == family.InputPerM && exact.OutputPerM == family.OutputPerM {
		t.Errorf("gemini-2.0-flash took the family rate %+v instead of its own published price", family)
	}
	if _, direct := generatedByModel["gemini-2.0-flash"]; !direct {
		t.Error("precondition broken: gemini-2.0-flash is no longer in the generated table")
	}
}

// Breadth must not become permission to guess. The generated table is matched by exact id only, so
// an id that merely LOOKS like one of its keys stays unpriced — the same rule the hand table's
// OpenAI and Anthropic entries have always followed, for the same reason: an unknown future model
// inheriting a price it was never sold at is invisible until the invoice arrives.
func TestGeneratedTableNeverMatchesByPrefix(t *testing.T) {
	if _, ok := generatedByModel["o4-mini"]; !ok {
		t.Fatal("precondition broken: o4-mini is not in the generated table")
	}
	// A hand FAMILY key must not rescue it either: "o1"/"o3" are exact hand keys, not families
	// covering the whole o-series, so nothing may answer for this id.
	if price, ok := lookupLegacy("o4-mini-imaginary-successor"); ok {
		t.Errorf("o4-mini-imaginary-successor was priced at %+v; unknown ids must stay unpriced", price)
	}
}

// A curated entry must keep beating the bulk import. table.go is where Anthropic's 1-hour
// cache-write tier and the long-context premium live, and upstream publishes neither — so a
// generated row winning would not just change a number, it would drop a whole billing dimension.
func TestHandTableOutranksGenerated(t *testing.T) {
	price, ok := lookupLegacy("claude-opus-5")
	if !ok {
		t.Fatal("claude-opus-5 must be priced")
	}
	if price.CacheWrite1hPerM == 0 {
		t.Error("claude-opus-5 lost its 1h cache-write tier — a generated row outranked the hand table")
	}
}
