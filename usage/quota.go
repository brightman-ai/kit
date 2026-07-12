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
//	               session history, a quota snapshot we captured earlier)
//	2. billing   — subscription (5h/7d windows) or API-key (pay-per-token, no window)
//	3. snapshot  — the last-known quota reading + how fresh it is
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
// not keep claiming the account is live just because a stale snapshot is on disk.
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
}

// SnapshotMeta describes WHEN the quota reading was taken and whether it can still
// be trusted as current (axis 3). Absent (nil QuotaInfo.Snapshot) means we have never
// captured a reading — render 「等待额度数据」, never a fabricated 0%/100%.
type SnapshotMeta struct {
	// CapturedAt is the ISO-8601 time the reading was taken.
	CapturedAt string `json:"captured_at,omitempty"`
	// AgeSeconds is how old the reading is at query time.
	AgeSeconds int64 `json:"age_seconds"`
	// Stale marks a reading that can no longer be read as current.
	Stale bool `json:"stale"`
	// StaleReason is "window_rolled" (the window it describes has since reset, so the
	// used% is certainly wrong) or "too_old" (no reset time to check against, and the
	// reading predates maxSnapshotAge).
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

	// ── axis 3: quota snapshot
	// Plan is the subscription plan name (e.g. "Max5", "prolite") when the runtime reports it.
	Plan string `json:"plan,omitempty"`
	// Windows carries the last-known rolling-window usage (5h/7d). May come from a STALE
	// snapshot — check Snapshot.Stale before presenting it as live.
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

// QueryAllQuotas returns quota state for claude, codex, and gemini.
func QueryAllQuotas() []QuotaInfo {
	return []QuotaInfo{
		queryClaudeQuota(),
		queryCodexQuota(),
		queryGeminiQuota(),
	}
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

// queryClaudeQuota assembles claude's four axes.
//
// Presence comes from account artifacts (credentials / captured snapshot / project
// history) — NOT from the CLI probe. Real quota numbers come from the statusLine hook
// drop file (the claude CLI exposes 5h/7d used% nowhere else); absent file → honest
// 「等待额度数据」, never a fabricated number.
func queryClaudeQuota() QuotaInfo {
	info := QuotaInfo{Runtime: "claude", Health: probeCLI("claude")}

	rl, hasSnapshot := readClaudeRateLimits(claudeRateLimitsPath())
	info.Evidence = claudePresenceEvidence(hasSnapshot)
	info.Present = len(info.Evidence) > 0
	if !info.Present {
		return info
	}

	if !hasSnapshot {
		info.Billing = BillingUnknown
		info.Note = claudeNoSnapshotNote(info.Evidence)
		return info
	}

	capturedAt := snapshotTime(rl.CapturedAt, claudeRateLimitsPath())

	// API-key billing: no subscription 5h/7d window exists by design. Report it
	// explicitly so the chip files it under 「API 计费」 instead of an "expired" bar.
	if rl.Source == "api" {
		info.Billing = BillingAPI
		info.Snapshot = newSnapshotMeta(capturedAt, nil)
		info.Note = "API 计费会话 · 按量付费（无订阅额度窗口）"
		return info
	}

	info.Billing = BillingSubscription
	if q, ok := claudeQuotaWindow("5h", rl.FiveHour); ok {
		info.Windows = append(info.Windows, q)
	}
	if q, ok := claudeQuotaWindow("7d", rl.SevenDay); ok {
		info.Windows = append(info.Windows, q)
	}
	info.Snapshot = newSnapshotMeta(capturedAt, info.Windows)
	info.Note = "实时额度来自 statusLine hook"
	return info
}

// queryCodexQuota assembles codex's four axes. Real quota comes from the newest
// rollout transcript's last rate_limits object (same source abtop reads).
func queryCodexQuota() QuotaInfo {
	info := QuotaInfo{Runtime: "codex", Health: probeCLI("codex")}

	rl, capturedAt, hasSnapshot := latestCodexRateLimits(codexSessionsDir())
	info.Evidence = codexPresenceEvidence(hasSnapshot)
	info.Present = len(info.Evidence) > 0
	if !info.Present {
		return info
	}

	// Billing is knowable from the auth file's SHAPE (an API key vs an OAuth token set)
	// — we look at which field is populated, never at its value.
	info.Billing = codexBilling(hasSnapshot)

	if !hasSnapshot {
		if info.Billing == BillingAPI {
			info.Note = "API 计费会话 · 按量付费（无订阅额度窗口）"
		} else {
			info.Note = "暂无 rollout 额度记录"
		}
		return info
	}

	info.Plan = rl.PlanType
	if q, ok := codexQuotaWindow(rl.Primary); ok {
		info.Windows = append(info.Windows, q)
	}
	if q, ok := codexQuotaWindow(rl.Secondary); ok {
		info.Windows = append(info.Windows, q)
	}
	info.Snapshot = newSnapshotMeta(capturedAt, info.Windows)
	info.Note = "账号额度来自 rollout transcript"
	return info
}

// queryGeminiQuota always reports absent; Gemini quota parsing is not supported, and
// not_implemented must never masquerade as a supported-but-empty provider.
func queryGeminiQuota() QuotaInfo {
	return QuotaInfo{
		Runtime: "gemini",
		Present: false,
		Health:  RuntimeHealth{OK: false, Reason: "not_implemented"},
		Note:    "Gemini quota inspection is not yet supported",
	}
}

// newSnapshotMeta stamps a reading with its age and decides whether it may still be
// presented as current. A window whose reset time has already passed is definitively
// stale — the runtime has since rolled it over, so the used% we hold is wrong.
func newSnapshotMeta(capturedAt time.Time, windows []QuotaWindow) *SnapshotMeta {
	if capturedAt.IsZero() {
		return nil
	}
	meta := &SnapshotMeta{
		CapturedAt: capturedAt.UTC().Format(time.RFC3339),
		AgeSeconds: int64(time.Since(capturedAt).Seconds()),
	}
	if meta.AgeSeconds < 0 {
		meta.AgeSeconds = 0
	}
	now := time.Now()
	for _, w := range windows {
		if w.ResetAt == "" {
			continue
		}
		if reset, err := time.Parse(time.RFC3339, w.ResetAt); err == nil && reset.Before(now) {
			meta.Stale = true
			meta.StaleReason = "window_rolled"
			return meta
		}
	}
	if time.Since(capturedAt) > maxSnapshotAge {
		meta.Stale = true
		meta.StaleReason = "too_old"
	}
	return meta
}

// claudeNoSnapshotNote explains the empty-quota case in the user's terms: logged in but
// the opt-in hook has not produced a reading yet, versus only residual history on disk.
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
