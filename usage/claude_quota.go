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
	"encoding/json"
	"os"
	"time"
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
