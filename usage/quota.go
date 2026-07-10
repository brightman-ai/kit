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

// QuotaInfo describes the subscription quota state for one runtime.
type QuotaInfo struct {
	// Runtime is the CLI name: "claude", "codex", "gemini".
	Runtime string `json:"runtime"`
	// Available indicates whether quota information could be parsed.
	Available bool `json:"available"`
	// Reason explains why Available is false (empty when Available is true).
	Reason string `json:"reason,omitempty"`
	// Plan is the subscription plan name (e.g. "Pro", "Max5", "Free", "prolite").
	Plan string `json:"plan,omitempty"`
	// Billing is the billing mode: "subscription" (has 5h/7d subscription windows)
	// or "api" (API-key / usage-based — no subscription window exists by design, so
	// the chip shows 「API 计费·按量」 rather than a misleading "expired" quota).
	Billing string `json:"billing,omitempty"`
	// ResetAt is the ISO-8601 time when the quota window resets (best-effort).
	ResetAt string `json:"reset_at,omitempty"`
	// WindowHours is the quota window size in hours (5 or 168 depending on plan).
	WindowHours int `json:"window_hours,omitempty"`
	// Windows carries the real rolling-window usage (5h/7d) when the runtime
	// exposes it (codex rollout jsonl). Empty when unavailable (honest: render「—」).
	Windows []QuotaWindow `json:"windows,omitempty"`
	// Note is a human-readable supplementary message.
	Note string `json:"note,omitempty"`
}

// QueryAllQuotas returns quota state for claude, codex, and gemini.
func QueryAllQuotas() []QuotaInfo {
	return []QuotaInfo{
		queryClaudeQuota(),
		queryCodexQuota(),
		queryGeminiQuota(),
	}
}

// queryClaudeQuota probes `claude` for subscription quota info.
// Claude CLI does not expose a machine-readable quota command; we detect the
// plan from `claude --version` output and return a best-effort result.
func queryClaudeQuota() QuotaInfo {
	info := QuotaInfo{Runtime: "claude"}
	path, err := exec.LookPath("claude")
	if err != nil {
		info.Available = false
		info.Reason = "not_installed"
		return info
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		info.Available = false
		info.Reason = "version_check_failed"
		return info
	}

	ver := strings.TrimSpace(out.String())
	// Claude CLI --version returns something like:
	//   Claude Code 1.x.x
	info.Available = true
	// Standard claude Pro plan has a ~5h rolling window.
	info.WindowHours = 5

	// Real quota: the claude CLI exposes subscription 5h/7d used% ONLY via the
	// `rate_limits` field of its statusLine hook input. Our opt-in passthrough
	// hook persists that to ~/.deepwork/claude-rate-limits.json; read it here.
	// Absent file → honest 「—」 (no fabricated numbers), same as codex.
	if rl, ok := readClaudeRateLimits(claudeRateLimitsPath()); ok {
		// API-key billing: no subscription 5h/7d window exists by design. Report it
		// explicitly so the chip labels it 「API 计费·按量」 instead of an "expired" bar.
		if rl.Source == "api" {
			info.Billing = "api"
			info.WindowHours = 0
			info.Note = "claude CLI (" + ver + "); API 计费会话 · 无订阅额度窗口（按量付费）"
			return info
		}
		info.Billing = "subscription"
		if q, ok := claudeQuotaWindow("5h", rl.FiveHour); ok {
			info.Windows = append(info.Windows, q)
		}
		if q, ok := claudeQuotaWindow("7d", rl.SevenDay); ok {
			info.Windows = append(info.Windows, q)
		}
		// Surface the soonest reset on the top-level field too (back-compat).
		for _, q := range info.Windows {
			if q.ResetAt != "" && (info.ResetAt == "" || q.ResetAt < info.ResetAt) {
				info.ResetAt = q.ResetAt
			}
		}
		info.Note = "claude CLI (" + ver + "); 实时额度来自 statusLine hook"
		return info
	}

	// No hook drop file: quota not exposed via CLI without the opt-in hook.
	info.Plan = "unknown"
	info.Note = "claude CLI detected (" + ver + "); 额度需启用 statusLine hook"
	return info
}

// queryCodexQuota probes `codex` for subscription quota info.
// Codex CLI does not expose a quota subcommand; we detect installation only.
func queryCodexQuota() QuotaInfo {
	info := QuotaInfo{Runtime: "codex"}
	path, err := exec.LookPath("codex")
	if err != nil {
		info.Available = false
		info.Reason = "not_installed"
		return info
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		info.Available = false
		info.Reason = "version_check_failed"
		return info
	}

	ver := strings.TrimSpace(out.String())
	info.Available = true
	info.WindowHours = 168 // 7-day window typical for Codex

	// Real quota: codex persists account rate-limits in the rollout transcript.
	// Read the newest rollout's last rate_limits → 5h/7d usage windows (abtop source).
	if rl, ok := latestCodexRateLimits(codexSessionsDir()); ok {
		info.Plan = rl.PlanType
		if q, ok := codexQuotaWindow(rl.Primary); ok {
			info.Windows = append(info.Windows, q)
		}
		if q, ok := codexQuotaWindow(rl.Secondary); ok {
			info.Windows = append(info.Windows, q)
		}
		// Surface the soonest reset on the top-level field too (back-compat).
		for _, q := range info.Windows {
			if q.ResetAt != "" && (info.ResetAt == "" || q.ResetAt < info.ResetAt) {
				info.ResetAt = q.ResetAt
			}
		}
		info.Note = "codex CLI (" + ver + "); 实时额度来自 rollout transcript"
		return info
	}

	info.Plan = "unknown"
	info.Note = "codex CLI detected (" + ver + "); 暂无 rollout 额度记录"
	return info
}

// queryGeminiQuota always returns not-implemented; Gemini CLI quota parsing
// is not yet supported.
func queryGeminiQuota() QuotaInfo {
	return QuotaInfo{
		Runtime:   "gemini",
		Available: false,
		Reason:    "not_implemented",
		Note:      "Gemini quota inspection is not yet supported",
	}
}
