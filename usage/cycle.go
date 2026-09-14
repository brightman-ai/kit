// Package usage — cycle.go: when a billing cycle actually began.
//
// ── The gap this closes ─────────────────────────────────────────────────────────────────────────
// A vendor states when a window ENDS (`reset_at`) and how long a window IS. It never states when
// the CURRENT cycle began, and it certainly never states when the PREVIOUS one did. Every caller
// that needed a start therefore computed one:
//
//	start      := reset_at −   span
//	priorStart := reset_at − 2·span
//
// Both rest on "every cycle is exactly one span long". The first happens to hold, because a cycle
// — however it starts — runs a full span from that moment. The second does NOT: a cycle that ends
// EARLY is shorter than a span, so counting a full span backwards from the current cycle's start
// lands inside the cycle before the previous one.
//
// That is not hypothetical. Codex sells a "reset card": you exhaust the weekly limit, redeem one,
// and the window restarts on the spot. Measured on this account 2026-09-05 — previous cycle ran to
// 100%, was reset early at 13:29 — the derived "previous cycle" reached back to 08-29 13:29, which
// is necessarily BEFORE the previous cycle began (it began later, or it could not have been cut
// short). The two heaviest days in that span, 42k and 38k credits, may well belong to the cycle
// before it. The number was not double-counted; it was MIS-ATTRIBUTED, which is worse, because it
// looks like an answer to a question nobody can check.
//
// ── The fix: observe instead of derive ──────────────────────────────────────────────────────────
// The start of a cycle is unknowable arithmetic — but it is a plain OBSERVATION. We already probe
// this account on a timer and already persist what we see. Every time `reset_at` MOVES, a cycle
// boundary happened: natural expiry and a redeemed reset card are the same event seen from here,
// which is exactly why this needs no vendor-specific handling and will absorb whatever mechanism
// the vendor adds next (plan upgrades, top-ups, admin resets).
//
// So: record boundaries as they pass, and let "when did the previous cycle begin?" be answered
// from the record — or answered "unknown", which is the honest answer for every cycle that ended
// before this file existed. What must never happen again is a THIRD kind of answer: a derived
// number presented as an observed one.
package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// cycleLedgerKeep is how many boundaries one account retains.
//
// Two answer every question this package asks ("when did the current cycle begin", "when did the
// previous one"); the rest are headroom so a burst of resets — which is exactly when the ledger
// matters — cannot evict the boundary a caller is about to ask about. Bounded because this is a
// cache of observations, not an audit log: losing old boundaries costs a label, not correctness.
const cycleLedgerKeep = 8

// CycleBoundary is one observed transition between billing cycles.
//
// ObservedAt is when WE saw it, EndsAt is the vendor's reset_at for the cycle that started here.
// The two differ by however long we were not looking, and that gap is the reason a boundary is
// recorded rather than reconstructed: the next probe cannot tell how long ago the change happened.
type CycleBoundary struct {
	// EndsAt is the reset_at the vendor reported for the cycle that STARTS at this boundary.
	EndsAt time.Time `json:"ends_at"`
	// ObservedAt is when a probe first saw this reset_at. It is an upper bound on the true
	// boundary instant: the cycle began at or before this moment.
	ObservedAt time.Time `json:"observed_at"`
}

// cycleLedger is one account's append-only record of boundaries, oldest first.
type cycleLedger struct {
	Boundaries []CycleBoundary `json:"boundaries"`
}

func cycleLedgerPath(a Account) string {
	return deepworkFile(filepath.Join("quota", a.Runtime+"-"+a.Vendor+".cycles.json"))
}

func readCycleLedger(a Account) cycleLedger {
	var led cycleLedger
	data, err := os.ReadFile(cycleLedgerPath(a)) //nolint:gosec — our own cache
	if err != nil {
		return led
	}
	if json.Unmarshal(data, &led) != nil {
		return cycleLedger{} // unreadable ⟹ we have observed nothing, which is a true statement
	}
	sort.Slice(led.Boundaries, func(i, j int) bool {
		return led.Boundaries[i].EndsAt.Before(led.Boundaries[j].EndsAt)
	})
	return led
}

func writeCycleLedger(a Account, led cycleLedger) error {
	path := cycleLedgerPath(a)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(led)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// observeCycleBoundary records that `endsAt` is the current cycle's reset time, and reports the
// ledger as it now stands.
//
// Idempotent by construction: a reset_at we already hold is not a new boundary, so the timer may
// call this as often as it likes. Only a CHANGE appends — which is the one-line definition of
// "a cycle boundary happened", and the reason natural expiry and a redeemed reset card need no
// telling apart.
func observeCycleBoundary(a Account, endsAt time.Time, now time.Time) cycleLedger {
	if endsAt.IsZero() {
		return readCycleLedger(a)
	}
	led := readCycleLedger(a)
	for _, b := range led.Boundaries {
		if b.EndsAt.Equal(endsAt) {
			return led // already known — not a boundary, just another look at the same cycle
		}
	}
	led.Boundaries = append(led.Boundaries, CycleBoundary{EndsAt: endsAt.UTC(), ObservedAt: now.UTC()})
	sort.Slice(led.Boundaries, func(i, j int) bool {
		return led.Boundaries[i].EndsAt.Before(led.Boundaries[j].EndsAt)
	})
	if n := len(led.Boundaries); n > cycleLedgerKeep {
		led.Boundaries = led.Boundaries[n-cycleLedgerKeep:]
	}
	_ = writeCycleLedger(a, led) // best effort: a ledger we failed to persist costs a label, not a number
	return led
}

// priorCycle returns the observed span of the cycle that ran immediately before the one ending at
// `endsAt`, and whether it is an OBSERVED cycle rather than a guess.
//
// ok=false is the normal answer for a long time after this ships — every boundary that passed
// before the ledger existed is simply not in it. Callers must render that as "unknown", never
// substitute the old `endsAt − 2·span` arithmetic: substituting is how a rolling window came to be
// labelled "上一周期" in the first place.
func (l cycleLedger) priorCycle(endsAt time.Time) (start, end time.Time, ok bool) {
	// The boundary that STARTED the current cycle is the one whose EndsAt is the current reset —
	// its ObservedAt is an upper bound on when the current cycle began.
	idx := -1
	for i, b := range l.Boundaries {
		if b.EndsAt.Equal(endsAt.UTC()) {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return time.Time{}, time.Time{}, false // never observed, or nothing before it
	}
	return l.Boundaries[idx-1].ObservedAt, l.Boundaries[idx].ObservedAt, true
}
