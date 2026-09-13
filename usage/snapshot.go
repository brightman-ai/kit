// Package usage — snapshot.go: the readings WE asked for, persisted per account.
//
// A probe is the only way to learn a quota that the runtime is not currently writing down, and
// its answer has to outlive the request that fetched it: otherwise the very next background
// poll re-reads the transcript, finds the older number, and quietly reverts the value the user
// just refreshed to. So every probe result lands here, and the offline path reads it back and
// competes with the transcript on age alone.
//
// One file per ACCOUNT (runtime × vendor), because two accounts are two independent truths and
// a shared file would make the newer one erase the other — the same mistake, one level up, that
// per-family merging exists to prevent inside a single account.
package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// snapshotFamily is one limit family's windows inside a persisted snapshot.
type snapshotFamily struct {
	Family string `json:"family,omitempty"`
	// Label is what a person should read instead of the family id. Vendors name a metered
	// feature twice — an id to merge on ("codex_bengalfox") and a name to show
	// ("GPT-5.3-Codex-Spark") — and showing the id is how an internal key ends up in a UI.
	Label   string        `json:"label,omitempty"`
	Windows []QuotaWindow `json:"windows"`
}

// quotaSnapshot is one probe result, whole. It stores every family the vendor reported in that
// single answer — an account limit plus, for codex, its per-model sub-limits — because they
// were true at the same instant and splitting them across writes would let one age out alone.
type quotaSnapshot struct {
	Account    Account          `json:"account"`
	CapturedAt int64            `json:"captured_at"`
	Source     string           `json:"source"`
	Plan       string           `json:"plan,omitempty"`
	Billing    string           `json:"billing,omitempty"`
	Families   []snapshotFamily `json:"families,omitempty"`
	// Credits is the vendor's own consumption unit for the current window, when the vendor has
	// one. nil ⟹ this vendor does not meter in credits (Kimi meters in window percentage), or
	// the vendor has not aggregated the current window yet.
	Credits *Credits `json:"credits,omitempty"`
	// LastProbeAt / LastProbeError remember the most recent PROBE outcome when that probe
	// FAILED (a successful probe's outcome is CapturedAt itself). A failed probe must not
	// erase the last-known windows, but its REASON must survive — "账号未返回可用额度窗口"
	// (subscription likely lapsed) is actionable information the stale badge was flattening
	// into a generic "数据已过期". Kept alongside the families precisely so the UI can show
	// "still showing the reading from X because the account said Y".
	LastProbeAt    int64  `json:"last_probe_at,omitempty"`
	LastProbeError string `json:"last_probe_error,omitempty"`
}

// recordProbeFailure persists a failed probe's reason WITHOUT touching the last-known windows:
// the failure is a fact about NOW, the windows are facts about THEN, and overwriting one with
// the other is how "8 天前的数字" loses its explanation. Absent file → a bare error record
// (the account existed long enough to be probed; that is worth remembering too).
func recordProbeFailure(a Account, at time.Time, msg string) {
	path := snapshotPath(a)
	var snap quotaSnapshot
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &snap)
	}
	snap.Account = a
	snap.LastProbeAt = at.Unix()
	snap.LastProbeError = msg
	_ = writeSnapshot(snap)
}

// lastProbeFailure reads the persisted probe-failure reason, if any and recent enough to be
// worth saying (a failure from last month explains nothing about today's number).
func lastProbeFailure(a Account, maxAge time.Duration) (at time.Time, msg string) {
	data, err := os.ReadFile(snapshotPath(a))
	if err != nil {
		return time.Time{}, ""
	}
	var snap quotaSnapshot
	if json.Unmarshal(data, &snap) != nil || snap.LastProbeError == "" || snap.LastProbeAt <= 0 {
		return time.Time{}, ""
	}
	t := time.Unix(snap.LastProbeAt, 0)
	if time.Since(t) > maxAge {
		return time.Time{}, ""
	}
	return t, snap.LastProbeError
}

// snapshotPath is where one account's probe result lives.
func snapshotPath(a Account) string {
	return deepworkFile(filepath.Join("quota", a.Runtime+"-"+a.Vendor+".json"))
}

// writeSnapshot persists a probe result so the OFFLINE path sees it too.
func writeSnapshot(snap quotaSnapshot) error {
	snap.LastProbeAt, snap.LastProbeError = 0, "" // a successful capture supersedes any older failure
	path := snapshotPath(snap.Account)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// readSnapshotReadings turns one account's persisted snapshot back into readings — one per
// family, all sharing the capture time, so they age and expire together as they were observed.
// Returns nil when we have never probed this account (or the file is unusable): "we never
// asked" must read as absence, never as a zeroed reading.
func readSnapshotReadings(a Account) ([]*Reading, *Credits) {
	data, err := os.ReadFile(snapshotPath(a)) //nolint:gosec — our own read-only cache
	if err != nil {
		return nil, nil
	}
	var snap quotaSnapshot
	if json.Unmarshal(data, &snap) != nil || snap.CapturedAt <= 0 {
		return nil, nil
	}
	at := time.Unix(snap.CapturedAt, 0)
	readings := make([]*Reading, 0, len(snap.Families))
	for _, family := range snap.Families {
		if len(family.Windows) == 0 {
			continue
		}
		readings = append(readings, &Reading{
			Account:     a,
			CapturedAt:  at,
			Source:      snap.Source,
			Plan:        snap.Plan,
			Family:      family.Family,
			FamilyLabel: family.Label,
			Billing:     orDefault(snap.Billing, BillingSubscription),
			Windows:     family.Windows,
		})
	}
	return readings, snap.Credits
}

// readSnapshotProbeFailure surfaces the persisted last-probe failure for one account.
func readSnapshotProbeFailure(a Account) (time.Time, string) {
	return lastProbeFailure(a, 48*time.Hour)
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
