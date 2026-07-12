// Package usage provides subscription quota inspection and usage reporting
// for supported CLI runtimes (claude, codex, gemini).
package usage

import (
	"bytes"
	"context"
	"os/exec"
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
	// Expired marks a window whose reset time has PASSED since this reading was taken. The
	// counter has since rolled over, so UsedPercent/RemainingPercent are not merely old —
	// they are certainly WRONG, and a UI must not paint them as a quantity. Expiry is
	// per-window on purpose: a runtime's 5h window can roll while its 7d window stays
	// perfectly valid, and condemning both would throw away a number that is still true.
	Expired bool `json:"expired,omitempty"`
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

// QuotaInfo describes the quota state for one runtime, along the four axes above.
type QuotaInfo struct {
	// Runtime is the CLI name: "claude", "codex", "gemini".
	Runtime string `json:"runtime"`

	// ── axis 1: account presence (the only axis allowed to hide a provider)
	Present bool `json:"present"`
	// Evidence lists WHY we believe the account is present (credentials/snapshot/sessions).
	// Empty ⟹ Present is false ⟹ the runtime is not shown at all.
	Evidence []string `json:"evidence,omitempty"`

	// ── axis 2: billing mode
	Billing string `json:"billing,omitempty"`

	// ── axis 3: the reading
	Plan    string        `json:"plan,omitempty"`
	Windows []QuotaWindow `json:"windows,omitempty"`
	// Snapshot is nil when no reading has ever been captured.
	Snapshot *SnapshotMeta `json:"snapshot,omitempty"`

	// ── axis 4: runtime health
	Health RuntimeHealth `json:"health"`

	// Note is a human-readable supplementary message.
	Note string `json:"note,omitempty"`
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
	info.Plan = r.Plan
	if r.Billing != "" {
		info.Billing = r.Billing
	}
	info.Windows = r.Windows
	info.Snapshot = newSnapshotMeta(r)
}

// newSnapshotMeta stamps a reading with its age, its provenance, and whether it survives.
func newSnapshotMeta(r *Reading) *SnapshotMeta {
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
	if allExpired := markExpiredWindows(r.Windows); allExpired {
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

// markExpiredWindows flags every window whose reset has already passed, and reports whether
// ALL of them have (in which case the reading as a whole is worthless).
func markExpiredWindows(windows []QuotaWindow) (allExpired bool) {
	now := time.Now()
	checked, expired := 0, 0
	for i := range windows {
		if windows[i].ResetAt == "" {
			continue
		}
		checked++
		if reset, err := time.Parse(time.RFC3339, windows[i].ResetAt); err == nil && reset.Before(now) {
			windows[i].Expired = true
			expired++
		}
	}
	return checked > 0 && expired == checked
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

func (claudeProvider) Runtime() string { return "claude" }

// CanProbe is false: claude exposes its 5h/7d usage ONLY as a field of the JSON it pipes to a
// statusLine command. There is no endpoint to ask — the reading arrives when claude renders.
func (claudeProvider) CanProbe() bool                { return false }
func (claudeProvider) Probe(_ context.Context) error { return nil }

// Query assembles claude's four axes. Presence comes from account artifacts (credentials /
// captured reading / project history) — NOT from the CLI probe.
func (claudeProvider) Query() QuotaInfo {
	info := QuotaInfo{Runtime: "claude", Health: probeCLI("claude")}

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

func (codexProvider) Runtime() string { return "codex" }

// CanProbe is true when the account is OAuth-authenticated: only then is there a token to ask
// with. An API-key account has no subscription quota to report in the first place.
func (codexProvider) CanProbe() bool {
	_, err := codexAccessToken()
	return err == nil
}

func (codexProvider) Probe(ctx context.Context) error { return probeCodexQuota(ctx) }

// Query assembles codex's four axes from the freshest available reading: the rollout codex
// writes as it works, or the drop file our probe writes when the user asks.
func (codexProvider) Query() QuotaInfo {
	info := QuotaInfo{Runtime: "codex", Health: probeCLI("codex")}

	reading := newestReading(codexRolloutReading(), codexProbeReading())
	info.Evidence = codexPresenceEvidence(reading != nil)
	info.Present = len(info.Evidence) > 0
	if !info.Present {
		return info
	}

	// Billing is knowable from the auth file's SHAPE (an API key vs an OAuth token set) — we
	// look at which field is populated, never at its value.
	info.Billing = codexBilling(reading != nil)

	if reading == nil {
		if info.Billing == BillingAPI {
			info.Note = "API 计费会话 · 按量付费（无订阅额度窗口）"
		} else {
			info.Note = "暂无额度记录（codex 尚未上报）"
		}
		return info
	}

	info.applyReading(reading)
	switch reading.Source {
	case SourceProbe:
		info.Note = "账号额度 · 实时查询"
	default:
		info.Note = "账号额度来自 rollout transcript"
	}
	return info
}

// ── gemini ───────────────────────────────────────────────────────────────────

type geminiProvider struct{}

func (geminiProvider) Runtime() string               { return "gemini" }
func (geminiProvider) CanProbe() bool                { return false }
func (geminiProvider) Probe(_ context.Context) error { return nil }

// Query always reports absent: Gemini quota parsing is not supported, and not_implemented must
// never masquerade as a supported-but-empty provider.
func (geminiProvider) Query() QuotaInfo {
	return QuotaInfo{
		Runtime: "gemini",
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
