// Package usage provides subscription quota inspection and usage reporting
// for supported CLI runtimes (claude, codex, gemini).
package usage

import (
	"bytes"
	"context"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// The four axes.
//
// A runtime's quota state is FOUR orthogonal facts, and collapsing them into one
// boolean is what made a logged-in provider vanish from the UI the moment its CLI
// binary lost its executable bit:
//
//	1. presence  — does this account exist on this host?  (a USER fact: credentials,
//	               session history, a quota reading we captured earlier)
//	2. billing   — subscription (5h/7d windows) or API-key (pay-per-token, no window)
//	3. snapshot  — the last-known reading: its windows, when it was taken, where it
//	               came from, and whether it can still be trusted
//	4. health    — can we execute the CLI *right now*?  (an ENVIRONMENT fact)
//
// Only axis 1 may hide a provider. A failing probe on axis 4 degrades the card; it
// never deletes it. Same for a stale or missing snapshot on axis 3.
// ─────────────────────────────────────────────────────────────────────────────

// Billing modes.
const (
	BillingSubscription = "subscription" // has 5h/7d subscription windows
	BillingAPI          = "api"          // pay-per-token; no subscription window exists by design
	BillingUnknown      = "unknown"      // not enough evidence — say so, never guess
)

// Presence evidence kinds (axis 1). Reported so the UI can distinguish "logged in"
// from "only residual history" — an explicit logout removes credentials, and we must
// not keep claiming the account is live just because a stale reading is on disk.
const (
	EvidenceCredentials = "credentials" // an auth/credentials file exists
	EvidenceSnapshot    = "snapshot"    // we hold a (possibly old) quota reading
	EvidenceSessions    = "sessions"    // local transcript/session history exists
)

// Health reasons (axis 4).
const (
	HealthNotInstalled       = "not_installed"        // nothing named like the CLI on PATH
	HealthNotExecutable      = "not_executable"       // found, but cannot be executed (lost +x, bad interpreter)
	HealthVersionCheckFailed = "version_check_failed" // executed, but `--version` errored
	HealthNotImplemented     = "not_implemented"      // this runtime is not supported at all
)

// Stale reasons (axis 3).
const (
	StaleWindowRolled = "window_rolled" // every window it describes has since reset
	StaleTooOld       = "too_old"       // no reset to check against, and it predates maxSnapshotAge
)

// QuotaWindow is one rolling rate-limit window (abtop model): 5h primary / 7d
// secondary. UsedPercent comes straight from the runtime's own accounting.
type QuotaWindow struct {
	// Kind is "5h" | "7d" (derived from WindowMinutes: ≤300 → 5h else 7d).
	Kind string `json:"kind"`
	// WindowMinutes is the window size the runtime reports (300 / 10080).
	WindowMinutes int `json:"window_minutes,omitempty"`
	// UsedPercent / RemainingPercent in [0,100].
	UsedPercent      float64 `json:"used_percent"`
	RemainingPercent float64 `json:"remaining_percent"`
	// ResetAt is the ISO-8601 time the window resets.
	ResetAt string `json:"reset_at,omitempty"`
	// Expired marks a window whose reset time had PASSED by the time we looked. The counter
	// rolled over, so the used% we captured is not merely old — it is wrong. Expiry is
	// per-window on purpose: a 5h window can roll while the 7d window stays perfectly valid,
	// and condemning both would throw away a number that is still true.
	Expired bool `json:"expired,omitempty"`
	// Inferred marks a value we DERIVED rather than observed.
	//
	// Only one inference is made, and only for an expired window: the counter rolled, and any
	// use since would have made the runtime report a fresher reading — none exists, so the
	// window has not been touched since it reset. Its usage is therefore zero. The reset time
	// is rolled forward by whole window lengths to the next real boundary.
	//
	// This is the ONE place the package computes a number it did not observe, and it is
	// labelled as such all the way to the UI.
	Inferred bool `json:"inferred,omitempty"`
}

// SnapshotMeta describes the reading behind the windows: when it was taken, WHERE it came
// from, and whether it can still be read as current (axis 3). A nil QuotaInfo.Snapshot means
// we have never captured a reading — render 「等待额度数据」, never a fabricated 0%/100%.
type SnapshotMeta struct {
	// CapturedAt is the ISO-8601 time the reading was taken.
	CapturedAt string `json:"captured_at,omitempty"`
	// AgeSeconds is how old the reading was at query time.
	AgeSeconds int64 `json:"age_seconds"`
	// Source is where the reading came from: SourceHook | SourceRollout | SourceProbe.
	// This is the field that answers "why did refreshing change nothing?" — a rollout reading
	// only moves when the runtime chooses to write one, whereas a probe is us going and asking.
	Source string `json:"source,omitempty"`
	// Stale marks a reading that can no longer be read as current AS A WHOLE (every window it
	// describes has rolled, or it is simply too old). A SINGLE rolled window does not set this —
	// that lives on QuotaWindow.Expired, so a valid 7d number survives an expired 5h one.
	Stale       bool   `json:"stale"`
	StaleReason string `json:"stale_reason,omitempty"`
}

// RuntimeHealth is the CLI-executability probe (axis 4). A failure here NEVER hides
// the provider — it only tells the user "the CLI is broken, these numbers are the last
// ones we saw".
type RuntimeHealth struct {
	OK      bool   `json:"ok"`
	Reason  string `json:"reason,omitempty"`
	Version string `json:"version,omitempty"`
}

// QuotaGroup is one independently-accounted limit family. An account can expose several
// families at once (the codex account pool plus a per-model metered feature); each group
// therefore owns its windows and provenance. They must never be spliced together or replace
// one another.
type QuotaGroup struct {
	Family string `json:"family,omitempty"`
	// FamilyLabel is what to render; Family stays the merge key.
	FamilyLabel string        `json:"family_label,omitempty"`
	Windows     []QuotaWindow `json:"windows,omitempty"`
	Snapshot    *SnapshotMeta `json:"snapshot,omitempty"`
}

// Credits is the vendor's OWN consumption unit for the current window — how much was actually
// spent, as opposed to what fraction is gone.
//
// It exists only for vendors that meter this way (OpenAI does; Kimi meters in window percentage
// and reports nil here). It is always READ from the vendor, never computed: measured against the
// account API across six days, a local rate-card computation came out exactly 2.5× low every
// day, because the Fast speed multiplier is not reliably recorded in transcripts. A number that
// is wrong by a constant factor is more dangerous than no number, because it looks right.
//
// # There is deliberately no "allowance" here
//
// This type used to publish a whole-window budget, reverse-derived as spend ÷ used-fraction, and
// it was WRONG — not imprecise, wrong, by a factor of nearly four. Two complete windows measured
// on one account:
//
//	prev window (figures redacted): reached 100%, ~63k credits consumed → ~630 per point
//	this window:               at 6%,               ~1k credits consumed  → ~167 per point
//
// The derivation assumes credits and the rate-limit percentage move together. Those two windows
// disagree by 3.78×, so they do not, and no ranking of "which window to divide" can rescue it.
// (A second defect compounded it: the daily ledger settles for the current day with a lag of tens
// of minutes — the same activity read ~0.3k credits at one instant and ~1k twenty minutes later — so an early
// division also divides a numerator that has not finished arriving.)
//
// What the vendor DOES state exactly is kept, and nothing else is invented:
//
//	the percentage window  — "how much of the budget is gone", the vendor's own meter
//	Used                   — "how much was spent", the vendor's own ledger
//	PriorWindow            — the same ledger over the previous window, as history
//
// "How much is left" already has an exact answer in the percentage. Restating it in credits
// required a budget that cannot be measured, and inventing one produced「剩 4,716」on an account
// whose previous week had run to 63,025.
type Credits struct {
	// Used is the spend inside the current window.
	Used float64 `json:"used"`
	// PriorWindow is what the span before this cycle consumed. A plain sum of the vendor's own
	// daily figures — no division, no inference — kept as the honest answer to "roughly how many
	// credits does a week of my work take?".
	//
	// It is HISTORY, not this window's budget, and the UI must not present it as one. See the
	// type comment for why no budget is published at all.
	PriorWindow float64 `json:"prior_window,omitempty"`
	// PriorIsCycle says whether PriorWindow covers an actual billing CYCLE, or merely the span
	// of one window length before this cycle began.
	//
	// They differ whenever a cycle ended EARLY — codex sells a "reset card" that restarts the
	// weekly window on the spot, and this account used one on 2026-09-05 after running to 100%.
	// A cycle cut short is shorter than a span, so counting one span backwards reaches into the
	// cycle before it: 08-29 and 08-30 alone were 42k and 38k credits, and whether they belong to
	// the previous cycle is precisely what nobody can tell from arithmetic. True only when the
	// boundary was OBSERVED and recorded (cycle.go).
	//
	// NO omitempty. False is the value that carries the warning, and omitempty deletes exactly
	// the false — the identical mistake this file already made with WholeDays, where the「≈」it
	// existed to raise never once reached the UI.
	PriorIsCycle bool `json:"prior_is_cycle"`
	// SettledThrough is the last day inside this window whose figure the vendor has finished
	// writing (empty ⟹ not one day in the window has settled).
	SettledThrough string `json:"settled_through,omitempty"`
	// UnsettledDays is how many days inside Used the vendor is still writing.
	//
	// Used mixes settled history with a day that is not finished, and without this number there
	// is no way to tell "spent almost nothing" from "the ledger has not caught up". Measured
	// 2026-09-05: a one-day-old window read 1,153.45 credits against 0 turns while the meter
	// climbed 65% → 74% — the figure was 100% unsettled and looked like a small bill.
	//
	// NO omitempty, same rule as PriorIsCycle: zero is the value that means "this number is
	// trustworthy", and it is the one omitempty removes.
	UnsettledDays int `json:"unsettled_days"`
	// PriorWindowStart is when that previous window opened (ISO-8601), so the figure can be
	// labelled with the dates it actually covers instead of floating free.
	PriorWindowStart string `json:"prior_window_start,omitempty"`
	// Source is CreditsSourceAPI — the field exists so a future estimated source can never be
	// mistaken for this one.
	Source string `json:"source"`
	// WindowStart is the instant the current window opened (ISO-8601).
	WindowStart string `json:"window_start,omitempty"`
	// Days is how many daily aggregates went into Used.
	Days int `json:"days,omitempty"`
	// WholeDays is false when the window opened mid-day: the vendor aggregates by calendar day,
	// so the first day's total also contains spend from BEFORE this window. Used then
	// over-counts, and says so rather than guessing an intra-day split.
	//
	// NO omitempty. The informative value here is FALSE, and omitempty deletes exactly that one —
	// so the caveat this field exists to raise never reached the UI at all (verified on a live
	// window that opened at 11:23: the wire carried no `whole_days` and the「≈」never appeared).
	// A boolean whose false is the warning must always be on the wire.
	WholeDays bool `json:"whole_days"`
}

// CreditsSourceAPI marks credits read from the vendor's own accounting.
const CreditsSourceAPI = "api"

// Attribution answers "is the traffic I am producing right now billed to THIS account?".
//
// It exists because one runtime can serve several vendors: point codex at a translating proxy
// and every session still lands in ~/.codex/sessions, but the bill goes elsewhere. Without this,
// the official row can only sit there looking stale while the user is, correctly, not spending
// anything on it. nil ⟹ the question does not apply, or we cannot answer it — and "unknown" is
// never rendered as "no".
type Attribution struct {
	// Active is true when the newest session on this host was billed to this account.
	Active bool `json:"active"`
	// Vendor names who IS being billed, when that is knowable.
	Vendor  string `json:"vendor,omitempty"`
	Display string `json:"display,omitempty"`
	// ProviderID is the raw identifier we could not map — a runtime-side provider id for codex,
	// a model id for claude. Showing the string the user would recognise beats showing 「其他」.
	ProviderID string `json:"provider_id,omitempty"`
}

// QuotaInfo describes the quota state for one ACCOUNT, along the four axes above.
type QuotaInfo struct {
	// Runtime is the CLI name: "claude", "codex", "gemini".
	Runtime string `json:"runtime"`
	// Vendor is who is billed. Runtime+Vendor is the account's identity — the list key, and the
	// reason two rows can share a runtime.
	Vendor string `json:"vendor,omitempty"`
	// Display is the vendor's invoice name, served from the domain so adding a vendor needs no
	// frontend change.
	Display string `json:"display,omitempty"`

	// ── axis 1: account presence (the only axis allowed to hide a provider)
	Present bool `json:"present"`
	// Evidence lists WHY we believe the account is present (credentials/snapshot/sessions).
	// Empty ⟹ Present is false ⟹ the runtime is not shown at all.
	Evidence []string `json:"evidence,omitempty"`

	// ── axis 2: billing mode
	Billing string `json:"billing,omitempty"`

	// ── axis 3: the reading
	Plan string `json:"plan,omitempty"`
	// Family names WHICH set of limits these compatibility-projection windows belong to.
	// Codex can expose independent account families with different window shapes ("codex" =
	// 5h+7d, "premium" = one 7-day window); QuotaGroups below is the lossless representation.
	Family  string        `json:"family,omitempty"`
	Windows []QuotaWindow `json:"windows,omitempty"`
	// Snapshot is nil when no reading has ever been captured.
	Snapshot *SnapshotMeta `json:"snapshot,omitempty"`
	// QuotaGroups is the lossless view: one latest whole reading per family. Family/Windows/
	// Snapshot above remain the globally-newest compatibility projection for old consumers.
	QuotaGroups []QuotaGroup `json:"quota_groups,omitempty"`
	// Credits is what was actually spent this window, for vendors that meter that way.
	Credits *Credits `json:"credits,omitempty"`

	// ── axis 4: runtime health
	Health RuntimeHealth `json:"health"`
	// CanProbe says whether this account can be asked for a live reading. It is served rather
	// than inferred because the UI's only alternative is to hardcode which runtimes answer —
	// which was already wrong the moment a second vendor appeared behind the same CLI.
	CanProbe bool `json:"can_probe"`

	// Endpoints lists the runtime-side endpoint ids this SUBSCRIPTION is reached through — the
	// values the host declared in Credential.RuntimeProviderIDs (e.g. "mimo2codex-kimi-coding").
	//
	// It is published so a surface can tell one vendor's SUBSCRIPTION traffic from that same
	// vendor's pay-per-token traffic. Holding both at once is ordinary — a Kimi plan and a
	// Moonshot API key — and without this the two are indistinguishable, because codex records a
	// billing mode only for Fast turns and claude records none at all, so nearly every row says
	// "unknown". Matching on the endpoint is evidence; matching on the vendor alone is a guess
	// that quietly files metered spend under a subscription.
	//
	// EMPTY means "not reached through any declared endpoint" — a first-party plan (the CLI's own
	// login), which covers that vendor's traffic wholesale. Absence is therefore NOT "covers
	// nothing"; consumers must read it as "no endpoint restriction".
	//
	// These are names the user chose in their own config and are already shown on the report row;
	// nothing secret is added here.
	Endpoints []string `json:"endpoints,omitempty"`

	// Attribution says whether this account is the one currently being billed.
	Attribution *Attribution `json:"attribution,omitempty"`

	// Note is a human-readable supplementary message.
	Note string `json:"note,omitempty"`
	// LastProbeError is the reason the most recent PROBE failed (within its freshness horizon),
	// persisted across restarts next to the reading it failed to refresh. It explains why a
	// stale number stopped moving — "the account returned no quota windows" (subscription
	// lapsed) is actionable; a bare "数据已过期" is not.
	LastProbeError string `json:"last_probe_error,omitempty"`
	// LastProbeAt is when that failing probe ran (RFC3339).
	LastProbeAt string `json:"last_probe_at,omitempty"`
}

// maxSnapshotAge bounds how long a reading with no checkable reset time stays
// presentable as current. Quota only moves when the CLI runs, so an idle day is not
// itself a problem — but past this we stop implying the number is live.
const maxSnapshotAge = 12 * time.Hour

// applyReading folds an observation into the info: windows, plan, billing, and the snapshot
// metadata that says how far it can be trusted. The single place a Reading becomes UI truth.
func (info *QuotaInfo) applyReading(r *Reading) {
	if r == nil {
		return
	}
	group := quotaGroupFromReading(r)
	info.Plan = r.Plan
	info.Family = group.Family
	if r.Billing != "" {
		info.Billing = r.Billing
	}
	info.Windows = group.Windows
	info.Snapshot = group.Snapshot
	info.QuotaGroups = []QuotaGroup{group}
}

// applyReadings publishes every independent family while retaining the old single-reading
// fields as a deterministic projection of the globally newest observation.
func (info *QuotaInfo) applyReadings(readings []*Reading) {
	if len(readings) == 0 {
		return
	}
	groups := make([]QuotaGroup, 0, len(readings))
	for _, r := range readings {
		if r != nil {
			groups = append(groups, quotaGroupFromReading(r))
		}
	}
	if len(groups) == 0 {
		return
	}

	newest := readings[0] // newestReadingsByFamily returns newest-first.
	info.Plan = newest.Plan
	info.Family = groups[0].Family
	if newest.Billing != "" {
		info.Billing = newest.Billing
	}
	info.Windows = groups[0].Windows
	info.Snapshot = groups[0].Snapshot
	info.QuotaGroups = groups
}

func quotaGroupFromReading(r *Reading) QuotaGroup {
	// Copy before stamping expiry/inference: callers may reuse a Reading in another projection,
	// and query-time metadata must not mutate the source observation.
	windows := append([]QuotaWindow(nil), r.Windows...)
	windows = orderWindows(dedupeWindows(windows))
	return QuotaGroup{
		Family:      r.Family,
		FamilyLabel: r.FamilyLabel,
		Windows:     windows,
		Snapshot:    newSnapshotMeta(r, windows),
	}
}

// dedupeWindows keeps one window per kind. Two windows of the same kind is a contradiction —
// a runtime has one 5h window, not two — and letting both through pushed the contradiction
// into the UI, where a keyed list silently rendered only one of them and a bar appeared to
// vanish. Drop the duplicate here, at the boundary, where it is a data fact rather than a
// rendering accident. The first wins: sources list the real window before any filler.
func dedupeWindows(windows []QuotaWindow) []QuotaWindow {
	if len(windows) < 2 {
		return windows
	}
	seen := make(map[string]bool, len(windows))
	out := make([]QuotaWindow, 0, len(windows))
	for _, w := range windows {
		if seen[w.Kind] {
			continue
		}
		seen[w.Kind] = true
		out = append(out, w)
	}
	return out
}

// windowKind labels a window by its LENGTH — the single definition, because three vendors now
// report windows and a label invented per-vendor is how "7天" ends up on a monthly budget.
// 0 minutes means the vendor never stated a length; there is then nothing to call it.
func windowKind(minutes int) string {
	switch {
	case minutes <= 0:
		return ""
	case minutes <= 300:
		return "5h"
	case minutes <= 7*24*60:
		return "7d"
	default:
		return "30d"
	}
}

// orderWindows puts the shortest window first, so every vendor's row reads in the same order
// (5小时 then 7天) no matter what order that vendor happened to list them in. Two rows in one
// panel disagreeing about which bar comes first makes the reader re-parse each one — a cost paid
// on every glance, to save a sort that happens once. A window with no stated length sorts last:
// it cannot claim a position in a sequence it has no place in.
func orderWindows(windows []QuotaWindow) []QuotaWindow {
	sort.SliceStable(windows, func(i, j int) bool {
		a, b := windows[i].WindowMinutes, windows[j].WindowMinutes
		if a == 0 || b == 0 {
			return b == 0 && a != 0
		}
		return a < b
	})
	return windows
}

// newSnapshotMeta stamps a reading with its age, its provenance, and whether it survives.
func newSnapshotMeta(r *Reading, windows []QuotaWindow) *SnapshotMeta {
	if r == nil || r.CapturedAt.IsZero() {
		return nil
	}
	meta := &SnapshotMeta{
		CapturedAt: r.CapturedAt.UTC().Format(time.RFC3339),
		AgeSeconds: int64(time.Since(r.CapturedAt).Seconds()),
		Source:     r.Source,
	}
	if meta.AgeSeconds < 0 {
		meta.AgeSeconds = 0
	}
	if allExpired := markExpiredWindows(windows); allExpired {
		meta.Stale = true
		meta.StaleReason = StaleWindowRolled
		return meta
	}
	if time.Since(r.CapturedAt) > maxSnapshotAge {
		meta.Stale = true
		meta.StaleReason = StaleTooOld
	}
	return meta
}

// markExpiredWindows flags every window whose reset has already passed and rolls it forward,
// reporting whether ALL of them had rolled (in which case the READING as a whole is old news).
//
// A rolled window is not simply "unknown": the runtime reports a fresh reading whenever it is
// used, so a window that rolled with no fresher reading behind it has not been touched since.
// Its usage is zero, and its next boundary is the old one advanced by whole window lengths.
// That derivation is marked Inferred so the UI never passes it off as an observation.
func markExpiredWindows(windows []QuotaWindow) (allExpired bool) {
	now := time.Now()
	withReset, expired := 0, 0
	for i := range windows {
		w := &windows[i]
		if w.ResetAt == "" {
			continue
		}
		reset, err := time.Parse(time.RFC3339, w.ResetAt)
		if err != nil {
			continue
		}
		withReset++
		if !reset.Before(now) {
			continue
		}
		expired++
		w.Expired = true
		w.Inferred = true
		w.UsedPercent = 0
		w.RemainingPercent = 100
		// Roll the boundary forward to the one actually ahead of us.
		if w.WindowMinutes > 0 {
			span := time.Duration(w.WindowMinutes) * time.Minute
			w.ResetAt = reset.Add((now.Sub(reset)/span + 1) * span).UTC().Format(time.RFC3339)
		} else {
			w.ResetAt = "" // no window length ⟹ no honest way to say when it next resets
		}
	}
	return withReset > 0 && expired == withReset
}

// probeCLI reports whether the named CLI can be executed right now (axis 4 ONLY).
// Its failure must never be read as "the account is gone" — that conflation is the
// defect this split exists to prevent.
func probeCLI(name string) RuntimeHealth {
	path, err := exec.LookPath(name)
	if err != nil {
		// LookPath fails both when nothing is on PATH and when the file IS there but
		// cannot be executed (this is exactly the 2026-07-12 case: a claude self-update
		// left claude.exe without its +x bit). Separate the two — "installed but broken"
		// is a different story to tell than "never installed" — and neither may hide the
		// account.
		if existsOnPath(name) {
			return RuntimeHealth{OK: false, Reason: HealthNotExecutable}
		}
		return RuntimeHealth{OK: false, Reason: HealthNotInstalled}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return RuntimeHealth{OK: false, Reason: HealthVersionCheckFailed}
	}
	return RuntimeHealth{OK: true, Version: strings.TrimSpace(out.String())}
}

// ── claude ───────────────────────────────────────────────────────────────────

type claudeProvider struct{}

func (claudeProvider) Account() Account {
	return Account{Runtime: "claude", Vendor: VendorAnthropic}
}

// ProbeCost is ProbeNone: claude exposes its 5h/7d usage ONLY as a field of the JSON it pipes to
// a statusLine command. There is no endpoint to ask — the reading arrives when claude renders.
func (claudeProvider) ProbeCost() string             { return ProbeNone }
func (claudeProvider) Probe(_ context.Context) error { return nil }

// Query assembles claude's four axes. Presence comes from account artifacts (credentials /
// captured reading / project history) — NOT from the CLI probe.
func (p claudeProvider) Query() QuotaInfo {
	account := p.Account()
	info := QuotaInfo{
		Runtime: account.Runtime, Vendor: account.Vendor, Display: account.Display(),
		Health: probeCLI(account.Runtime), CanProbe: p.ProbeCost() != ProbeNone,
	}

	reading := claudeHookReading()
	info.Evidence = claudePresenceEvidence(reading != nil)
	info.Present = len(info.Evidence) > 0
	if !info.Present {
		return info
	}

	if reading == nil {
		info.Billing = BillingUnknown
		info.Note = claudeNoSnapshotNote(info.Evidence)
		return info
	}

	info.Attribution = claudeAttribution(account)
	info.applyReading(reading)
	if info.Billing == BillingAPI {
		info.Note = "API 计费会话 · 按量付费（无订阅额度窗口）"
	} else {
		info.Note = "实时额度来自 statusLine hook"
	}
	return info
}

// ── codex ────────────────────────────────────────────────────────────────────

type codexProvider struct{}

func (codexProvider) Account() Account {
	return Account{Runtime: "codex", Vendor: VendorOpenAI}
}

// ProbeCost is ProbeFree when the account is OAuth-authenticated: only then is there a token to
// ask with, and asking is a plain read. An API-key account has no subscription quota at all.
func (codexProvider) ProbeCost() string {
	if _, _, err := codexCredentials(); err != nil {
		return ProbeNone
	}
	return ProbeFree
}

func (codexProvider) Probe(ctx context.Context) error { return probeCodexQuota(ctx) }

// Query assembles codex's four axes from the freshest available reading PER FAMILY: rollout
// observations codex writes as it works, plus the snapshot our probe stores when it asks.
func (p codexProvider) Query() QuotaInfo {
	account := p.Account()
	info := QuotaInfo{
		Runtime: account.Runtime, Vendor: account.Vendor, Display: account.Display(),
		Health: probeCLI(account.Runtime), CanProbe: p.ProbeCost() != ProbeNone,
	}

	rolloutReadings := codexRolloutScan()
	snapshotReadings, credits := readSnapshotReadings(account)
	readings := newestReadingsByFamily(append(rolloutReadings, snapshotReadings...)...)

	info.Evidence = codexPresenceEvidence(len(readings) > 0)
	info.Present = len(info.Evidence) > 0
	if !info.Present {
		return info
	}
	info.Attribution = codexAttribution(account)

	// Billing is knowable from the auth file's SHAPE (an API key vs an OAuth token set) — we
	// look at which field is populated, never at its value.
	info.Billing = codexBilling(len(readings) > 0)

	if len(readings) == 0 {
		if info.Billing == BillingAPI {
			info.Note = "API 计费会话 · 按量付费（无订阅额度窗口）"
		} else {
			info.Note = "暂无额度记录（codex 尚未上报）"
		}
		return info
	}

	info.applyReadings(readings)
	info.Credits = credits
	switch readings[0].Source {
	case SourceProbe:
		info.Note = "账号额度 · 官方接口"
	default:
		info.Note = "账号额度来自 rollout transcript"
	}
	return info
}

// codexAttribution reports whether the newest codex session on this host was billed to `account`.
// billedTo is the raw session_meta.model_provider of that session; empty means an older rollout
// that predates the field, which by definition talked to OpenAI itself.
// codexAttribution answers "is the traffic codex is producing right now billed to THIS account?"
// over the windowed set of active endpoints — see claudeAttribution for why a set rather than the
// single newest session, and attributionWindow for why "now" is bounded at all.
func codexAttribution(account Account) *Attribution {
	vendors := recentCodexVendors(time.Now())
	if len(vendors) == 0 {
		return nil
	}
	return attributionFromVendors(account, vendors)
}

// ── gemini ───────────────────────────────────────────────────────────────────

type geminiProvider struct{}

func (geminiProvider) Account() Account {
	return Account{Runtime: "gemini", Vendor: VendorGoogle}
}
func (geminiProvider) ProbeCost() string             { return ProbeNone }
func (geminiProvider) Probe(_ context.Context) error { return nil }

// Query always reports absent: Gemini quota parsing is not supported, and not_implemented must
// never masquerade as a supported-but-empty provider.
func (p geminiProvider) Query() QuotaInfo {
	account := p.Account()
	return QuotaInfo{
		Runtime: account.Runtime,
		Vendor:  account.Vendor,
		Display: account.Display(),
		Present: false,
		Health:  RuntimeHealth{OK: false, Reason: HealthNotImplemented},
		Note:    "Gemini quota inspection is not yet supported",
	}
}

// claudeNoSnapshotNote explains the empty-quota case in the user's terms: logged in but the
// opt-in hook has not produced a reading yet, versus only residual history on disk.
func claudeNoSnapshotNote(evidence []string) string {
	if hasEvidence(evidence, EvidenceCredentials) {
		return "已登录 · 等待额度数据（需启用 statusLine hook）"
	}
	return "未检出登录凭据 · 仅存历史记录"
}

// hasEvidence reports whether kind is in the evidence set.
func hasEvidence(evidence []string, kind string) bool {
	for _, e := range evidence {
		if e == kind {
			return true
		}
	}
	return false
}
