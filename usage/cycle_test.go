package usage

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// The two facts this file defends, both learned from one account on 2026-09-05:
//
//   - a day the vendor has not finished writing is not spend, and must not be summed as if it were;
//   - a span of one window length before the current cycle is not "the previous cycle" unless we
//     watched the boundary go past.

func TestDailyRow_TodayIsNeverSettled(t *testing.T) {
	// The vendor is still writing it. No threshold, no timer — the date alone decides.
	row := codexDailyRow{Date: "2026-09-05"}
	row.Totals.Credits, row.Totals.Turns = 5000, 40
	if row.settled("2026-09-05") {
		t.Fatal("today's row reported settled; the vendor has not finished writing it")
	}
	if !row.settled("2026-09-06") {
		t.Fatal("yesterday's complete row should settle once the day is over")
	}
}

// The measured shape of an unsettled row: spend with no turns. Every settled day on that account
// sat between 179 and 218 credits per turn; the unsettled one read 1,153.45 against zero.
func TestDailyRow_SpendWithoutTurnsIsTheLedgerMidWrite(t *testing.T) {
	row := codexDailyRow{Date: "2026-09-04"}
	row.Totals.Credits, row.Totals.Turns = 1153.447236, 0
	if row.settled("2026-09-05") {
		t.Fatal("a row with credits but zero turns is self-contradictory and cannot be final")
	}
}

func TestDailyRow_AGenuinelyIdleDayIsSettled(t *testing.T) {
	// Zero and zero is not a contradiction — it is a day off, and it is final.
	row := codexDailyRow{Date: "2026-09-04"}
	if !row.settled("2026-09-05") {
		t.Fatal("an idle past day must settle; otherwise every quiet day poisons the window")
	}
}

// ── the cycle ledger ────────────────────────────────────────────────────────────────────────────

func withTempHome(t *testing.T) {
	t.Helper()
	t.Setenv("DEEPWORK_HOME", t.TempDir())
}

func TestCycleLedger_OnlyAChangedResetIsABoundary(t *testing.T) {
	withTempHome(t)
	acct := Account{Runtime: "codex", Vendor: VendorOpenAI}
	end := time.Date(2026, 9, 12, 5, 29, 16, 0, time.UTC)

	// The timer calls this every few minutes. Seeing the same reset again is not a new cycle,
	// and an idempotent observer is what lets the caller stay dumb.
	for i := 0; i < 5; i++ {
		observeCycleBoundary(acct, end, time.Date(2026, 9, 5, 13, 0, i, 0, time.UTC))
	}
	if n := len(readCycleLedger(acct).Boundaries); n != 1 {
		t.Fatalf("five looks at one cycle recorded %d boundaries, want 1", n)
	}

	next := end.AddDate(0, 0, 7)
	observeCycleBoundary(acct, next, time.Date(2026, 9, 12, 6, 0, 0, 0, time.UTC))
	if n := len(readCycleLedger(acct).Boundaries); n != 2 {
		t.Fatalf("a moved reset recorded %d boundaries, want 2", n)
	}
}

// A reset card and a natural rollover are the same event from here: reset_at moved. That is the
// whole reason this needs no vendor-specific handling, and why the next mechanism the vendor
// invents will be absorbed without a code change.
func TestCycleLedger_AnEarlyResetIsJustAMovedBoundary(t *testing.T) {
	withTempHome(t)
	acct := Account{Runtime: "codex", Vendor: VendorOpenAI}
	natural := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	observeCycleBoundary(acct, natural, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))

	// Redeemed mid-cycle on 09-05: the window restarts, so reset_at jumps to a NEW time that is
	// not on the old 7-day grid.
	early := time.Date(2026, 9, 12, 5, 29, 16, 0, time.UTC)
	led := observeCycleBoundary(acct, early, time.Date(2026, 9, 5, 5, 29, 20, 0, time.UTC))

	start, end, ok := led.priorCycle(early)
	if !ok {
		t.Fatal("the previous cycle was observed end to end and should be reported as a cycle")
	}
	// The span is bounded by the two OBSERVATIONS, not by the nominal window length: the cycle
	// ran from when we first saw its reset until when we saw that reset move. Here that is a bit
	// over four days — decisively NOT seven.
	span := 7 * 24 * time.Hour
	if got := end.Sub(start); got >= span {
		t.Fatalf("previous cycle measured %v, which is not shorter than the nominal %v — "+
			"an early reset must produce a SHORT cycle or this whole file buys nothing", got, span)
	}
	// The point of the exercise: the derived start is three days earlier than the observed one,
	// and those three days belong to the cycle before. That gap is the mis-attribution.
	derived := early.Add(-2 * span)
	if !start.After(derived) {
		t.Fatalf("observed start %v is not after the derived %v — the test proves nothing", start, derived)
	}
	// Guard against a degenerate fixture, expressed against the window rather than as a bare
	// number of hours: a quarter of a window of mis-attributed days is, on the measured account,
	// tens of thousands of credits.
	if gap := start.Sub(derived); gap < span/4 {
		t.Fatalf("derived start is only %v early; this fixture no longer exercises the defect", gap)
	}
}

func TestCycleLedger_UnobservedPriorIsUnknownNotGuessed(t *testing.T) {
	withTempHome(t)
	acct := Account{Runtime: "codex", Vendor: VendorOpenAI}
	end := time.Date(2026, 9, 12, 5, 29, 16, 0, time.UTC)
	led := observeCycleBoundary(acct, end, time.Date(2026, 9, 5, 5, 30, 0, 0, time.UTC))

	// First boundary ever recorded: everything before it happened while nobody was watching.
	// "Unknown" is the honest answer, and substituting arithmetic here is the original defect.
	if _, _, ok := led.priorCycle(end); ok {
		t.Fatal("a prior cycle was claimed from a single observed boundary")
	}
}

func TestCycleLedger_IsBoundedAndKeepsTheNewest(t *testing.T) {
	withTempHome(t)
	acct := Account{Runtime: "codex", Vendor: VendorOpenAI}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < cycleLedgerKeep*3; i++ {
		observeCycleBoundary(acct, base.AddDate(0, 0, 7*i), base.AddDate(0, 0, 7*i-7))
	}
	led := readCycleLedger(acct)
	if len(led.Boundaries) != cycleLedgerKeep {
		t.Fatalf("ledger holds %d boundaries, cap is %d", len(led.Boundaries), cycleLedgerKeep)
	}
	// Newest kept: this is a cache for answering "the previous cycle", not an audit log.
	newest := base.AddDate(0, 0, 7*(cycleLedgerKeep*3-1))
	if !led.Boundaries[len(led.Boundaries)-1].EndsAt.Equal(newest) {
		t.Fatal("eviction dropped the newest boundary instead of the oldest")
	}
}

func TestCycleLedger_UnreadableFileReadsAsNothingObserved(t *testing.T) {
	withTempHome(t)
	acct := Account{Runtime: "codex", Vendor: VendorOpenAI}
	observeCycleBoundary(acct, time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), time.Now())
	if err := writeCorrupt(cycleLedgerPath(acct)); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	// A cache that cannot be read means we have observed nothing — which is TRUE, and degrades to
	// "unknown". It must never become an error that costs the caller its quota windows.
	if n := len(readCycleLedger(acct).Boundaries); n != 0 {
		t.Fatalf("a corrupt ledger produced %d boundaries", n)
	}
}

// ── the wire ────────────────────────────────────────────────────────────────────────────────────

// This package has now twice shipped a boolean whose FALSE was the warning, behind `omitempty`,
// which deletes exactly the false. WholeDays did it and the「≈」never once reached the UI. These
// two fields carry the same shape of warning, so the wire is asserted rather than trusted.
func TestCredits_TheWarningValuesSurviveJSON(t *testing.T) {
	data, err := json.Marshal(Credits{Source: CreditsSourceAPI})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"prior_is_cycle", "unsettled_days", "whole_days"} {
		if _, present := raw[key]; !present {
			t.Fatalf("%q is missing from the wire at its zero value — that zero IS the warning, "+
				"and a consumer cannot tell it from a field the sender does not have", key)
		}
	}
}

func writeCorrupt(path string) error {
	return os.WriteFile(path, []byte("{not json"), 0o600)
}
