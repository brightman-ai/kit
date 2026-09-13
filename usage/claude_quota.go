// Package usage — claude_quota.go: real claude.ai subscription quota from the
// statusLine hook drop file.
//
// Unlike codex (which persists rate_limits into its rollout jsonl), the claude
// CLI exposes subscription 5h/7d usage ONLY as the `rate_limits` field of the
// JSON it pipes to a user-configured statusLine command. We capture that field
// via an opt-in passthrough hook (scripts/claude-statusline-hook.sh) which
// writes ~/.deepwork/claude-rate-limits.json. This reader consumes that file.
//
// Honest degradation: if the file is absent (hook not installed, or no API
// response yet this session), we return nil windows — the report renders 「—」
// rather than a fabricated number.
package usage

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brightman-ai/kit/pricing"
	"github.com/brightman-ai/kit/transcript"
)

// claudeRateWindow mirrors one window of the hook drop file (five_hour/seven_day).
// Field names match the Claude Code statusLine `rate_limits` contract verbatim:
// used_percentage (0-100) + resets_at (unix epoch seconds).
type claudeRateWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

// claudeRateLimits is the shape written by the statusline hook / statusline.sh.
// Source is the billing mode the capture inferred: "subscription" (rate_limits
// present) or "api" (active session with NO rate_limits ⟹ API-key billing, which
// has no subscription 5h/7d window by design). Empty on legacy files (treated as
// subscription for back-compat).
type claudeRateLimits struct {
	CapturedAt int64             `json:"captured_at"`
	Source     string            `json:"source"`
	FiveHour   *claudeRateWindow `json:"five_hour"`
	SevenDay   *claudeRateWindow `json:"seven_day"`
}

// claudeRateLimitsPath returns ~/.deepwork/claude-rate-limits.json.
// DEEPWORK_HOME overrides the dir (matches the hook script + tests).
func claudeRateLimitsPath() string { return deepworkFile("claude-rate-limits.json") }

// readClaudeRateLimits loads the hook drop file. ok=false when the file is
// missing/unreadable/unparseable, or when it carries NO usable signal — i.e. no
// windows AND no billing source (the pre-first-response case where the hook wrote
// bare nulls). A file with source="api" but null windows IS usable (it tells us
// this is an API-key session with no subscription window), so it yields ok=true.
func readClaudeRateLimits(path string) (claudeRateLimits, bool) {
	data, err := os.ReadFile(path) //nolint:gosec — read-only quota probe
	if err != nil {
		return claudeRateLimits{}, false
	}
	var rl claudeRateLimits
	if json.Unmarshal(data, &rl) != nil {
		return claudeRateLimits{}, false
	}
	if rl.FiveHour == nil && rl.SevenDay == nil && rl.Source == "" {
		return claudeRateLimits{}, false
	}
	return rl, true
}

// claudeHookReading turns the statusline-hook drop file into a Reading. nil when the hook has
// never produced anything usable.
//
// An "api" reading carries no windows BY DESIGN (API-key billing has no subscription window),
// and is still a perfectly good reading — it is how we know which kind of money is being spent.
func claudeHookReading() *Reading {
	path := claudeRateLimitsPath()
	rl, ok := readClaudeRateLimits(path)
	if !ok {
		return nil
	}
	at := snapshotTime(rl.CapturedAt, path)
	if at.IsZero() {
		return nil
	}

	r := &Reading{CapturedAt: at, Source: SourceHook, Billing: BillingSubscription}
	if rl.Source == "api" {
		r.Billing = BillingAPI
		return r
	}
	if w, ok := claudeQuotaWindow("5h", rl.FiveHour); ok {
		r.Windows = append(r.Windows, w)
	}
	if w, ok := claudeQuotaWindow("7d", rl.SevenDay); ok {
		r.Windows = append(r.Windows, w)
	}
	return r
}

// ── per-profile API sessions (claude-switch dual-account, 2026-09-13) ────────────────────────
// The statusline now writes one file PER account: the official OAuth session keeps the bare
// claude-rate-limits.json (subscription windows), and every provider profile (claude-switch's
// ~/.claude-profiles/<name>/) gets claude-rate-limits-<name>.json. This reads those profile
// files so the panel can say "official subscription: X% left · API 会话也在跑: glm, kimi" —
// both accounts exist at once and both deserve to be seen.

// ClaudeAPISession is one provider profile with a live-ish API-billing reading.
type ClaudeAPISession struct {
	Name string `json:"name"`
	// CapturedAt is RFC3339, the profile's last statusline beat.
	CapturedAt string `json:"captured_at"`
	// AgeSeconds is how long ago that beat was.
	AgeSeconds int64 `json:"age_seconds"`
}

// claudeAPISessionMaxAge bounds how long a profile file still counts as a live session —
// statuslines beat every few seconds, so a day-old file is a session that has since closed.
const claudeAPISessionMaxAge = 24 * time.Hour

func claudeAPISessions(now time.Time) []ClaudeAPISession {
	matches, err := filepath.Glob(deepworkFile("claude-rate-limits-*.json"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	out := make([]ClaudeAPISession, 0, len(matches))
	for _, path := range matches {
		var f struct {
			CapturedAt int64  `json:"captured_at"`
			Source     string `json:"source"`
			Profile    string `json:"profile"`
		}
		data, err := os.ReadFile(path) //nolint:gosec — our own statusline output
		if err != nil || json.Unmarshal(data, &f) != nil || f.CapturedAt <= 0 {
			continue
		}
		at := time.Unix(f.CapturedAt, 0)
		if now.Sub(at) > claudeAPISessionMaxAge {
			continue
		}
		name := f.Profile
		if name == "" {
			base := filepath.Base(path)
			name = strings.TrimSuffix(strings.TrimPrefix(base, "claude-rate-limits-"), ".json")
		}
		out = append(out, ClaudeAPISession{
			Name:       name,
			CapturedAt: at.UTC().Format(time.RFC3339),
			AgeSeconds: int64(now.Sub(at).Seconds()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgeSeconds < out[j].AgeSeconds })
	return out
}

// claudeQuotaWindow maps a claude window onto the unified QuotaWindow (5h/7d).
func claudeQuotaWindow(kind string, w *claudeRateWindow) (QuotaWindow, bool) {
	if w == nil {
		return QuotaWindow{}, false
	}
	remaining := 100 - w.UsedPercentage
	if remaining < 0 {
		remaining = 0
	}
	windowMinutes := 300 // 5h
	if kind == "7d" {
		windowMinutes = 10080
	}
	q := QuotaWindow{
		Kind:             kind,
		WindowMinutes:    windowMinutes,
		UsedPercent:      w.UsedPercentage,
		RemainingPercent: remaining,
	}
	if w.ResetsAt > 0 {
		q.ResetAt = time.Unix(w.ResetsAt, 0).UTC().Format(time.RFC3339)
	}
	return q, true
}

// ── attribution: which vendor is Claude Code pointed at right now? ────────────
//
// The model NAME is authoritative here, and that is worth stating because the codex side had to
// learn the opposite. Switching ANTHROPIC_BASE_URL to GLM Coding Plan makes claude record
// `glm-5.3` in its transcript — no proxy rewrites the id back into a `claude-*` one. So unlike a
// codex session behind a translating proxy, there is nothing to disbelieve.

// claudeAttributionFiles bounds the scan. The newest file is usually the live session, but a
// sidechain or a just-closed session can sort above it, and — the case that broke the old
// single-newest logic — several panes can be live at once. A host with many terminal sessions
// easily has more than four recently-touched transcripts, so the bound is generous; the window
// below is what actually limits the work.
const claudeAttributionFiles = 16

// attributionWindow is how recent traffic must be to count as "now".
//
// The badge answers「当前计费」, and "current" without a time bound is a claim the data cannot
// back: the old logic took the single newest assistant row REGARDLESS of age, so a GLM session
// from three days ago kept asserting「记在 GLM 名下」over an idle weekend. Outside the window
// there is no "now" to describe, and the honest display is no badge at all.
const attributionWindow = 30 * time.Minute

// recentClaudeVendors returns the set of vendors with assistant traffic inside the window, keyed
// by vendor id, plus the ids of models no vendor table claims (rare, but the user recognises the
// raw id and it must not be swallowed).
//
// It reads the newest transcripts' tails and keeps only rows whose timestamp falls inside the
// window — mtime alone would lie, since a compaction or a metadata append touches the file
// without producing billable traffic.
func recentClaudeVendors(now time.Time) map[string]string {
	vendors := map[string]string{}
	cutoff := now.Add(-attributionWindow).UTC().Format(time.RFC3339)
	for _, path := range transcript.NewestFiles(claudeProjectsDir(), "", transcript.JSONLSuffix, claudeAttributionFiles) {
		needle := []byte(`"assistant"`)
		_ = transcript.ScanTail(path, transcript.DefaultTailBytes, func(line []byte) bool {
			if !bytes.Contains(line, needle) {
				return true
			}
			var row struct {
				Type      string `json:"type"`
				Timestamp string `json:"timestamp"`
				Message   struct {
					Model string `json:"model"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &row) != nil || row.Type != "assistant" || row.Message.Model == "" {
				return true
			}
			if row.Timestamp < cutoff {
				return true // real traffic, but not current — see attributionWindow
			}
			if vendor := pricing.VendorForModel(row.Message.Model); vendor.ID != "" {
				vendors[vendor.ID] = row.Message.Model
			} else {
				vendors[""] = row.Message.Model
			}
			return true
		})
	}
	return vendors
}

// claudeAttribution answers "is the traffic Claude Code is producing right now billed to THIS
// account?" for any claude-runtime account (Anthropic's own, or a Coding Plan behind it).
//
// nil when there has been no claude traffic inside the window — unknown is never rendered as no,
// and stale is never rendered as current.
//
// CONCURRENCY is the case this used to get wrong: several panes can be live at once, split across
// vendors (measured on this host: 131 GLM rows and 2 Anthropic rows in one hour). Taking the
// single newest row picked one of them and called it THE biller, a claim that flipped by the
// second and was false the whole time. Now each account whose vendor has traffic in the window
// says 当前计费 for itself, and the「记在 X 名下」cross-badge is claimed only when exactly one
// vendor is active — the one unambiguous case where naming it is information rather than a guess.
func claudeAttribution(account Account) *Attribution {
	vendors := recentClaudeVendors(time.Now())
	if len(vendors) == 0 {
		return nil
	}
	return attributionFromVendors(account, vendors)
}

// attributionFromVendors is the shared set→badge rule for both runtimes. See claudeAttribution
// for why the cross-vendor name is published only when the set has exactly one member.
func attributionFromVendors(account Account, vendors map[string]string) *Attribution {
	attribution := &Attribution{Active: false}
	if _, ok := vendors[account.Vendor]; ok && account.Vendor != "" {
		attribution.Active = true
	}
	if len(vendors) == 1 {
		for vendor, model := range vendors {
			if vendor == "" {
				// Nobody's model table claims this id. Name the id — it is what the user will
				// recognise.
				attribution.ProviderID = model
				continue
			}
			attribution.Vendor = vendor
			attribution.Display = Account{Runtime: account.Runtime, Vendor: vendor}.Display()
		}
	}
	return attribution
}
