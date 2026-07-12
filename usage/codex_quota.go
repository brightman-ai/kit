// Package usage — codex_quota.go: the codex ACCOUNT rate limit, read from the rollout
// transcripts codex writes to ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl.
//
// Two things make this harder than "read the last rate_limits object", and getting either
// wrong produces numbers that look plausible and are simply false:
//
//  1. A rollout carries SEVERAL limit families. Alongside the account limit
//     (limit_id "codex", no limit_name) codex emits per-model sub-limits — e.g.
//     {"limit_id":"codex_bengalfox","limit_name":"GPT-5.3-Codex-Spark"} — which have their
//     OWN 5h/7d windows and their own, much lower, usage. The sub-limit heartbeats more
//     often, so the LAST rate_limits in a file is usually a sub-limit. Reading it reported
//     "92% left" while `codex /status` said 26% — the account was nearly out of quota and
//     the UI said it was fine. We therefore select the UNNAMED (account) family and ignore
//     named per-model limits.
//
//  2. The newest file's newest ACCOUNT entry is not its last line, and may not even be in
//     the newest file. We take the entry with the greatest EVENT timestamp across the few
//     most recent rollouts, and use that timestamp as the reading's capture time — so
//     "更新于 …" tells the truth instead of tracking a file's mtime.
//
// Read-only, no API call, no auth: the same data `codex /status` shows is already on disk.
package usage

import (
	"bytes"
	"encoding/json"
	"os"
	"time"

	"github.com/brightman-ai/kit/transcript"
)

// codexScanFiles bounds how many recent rollouts we look at. Rollouts reach tens of MB and
// rate-limit events land at the end, so scanning the tail (transcript.ScanTail) of the few
// newest files finds the current reading while reading far less than the whole tree.
const codexScanFiles = 4

// codexRateWindow mirrors one slot of codex's rate_limits (primary=5h / secondary=7d).
type codexRateWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

// codexRateLimits is the rate_limits object embedded in a token_count event.
type codexRateLimits struct {
	// LimitID is "codex" for the account limit; per-model sub-limits carry their own id.
	LimitID string `json:"limit_id"`
	// LimitName is set ONLY on per-model sub-limits ("GPT-5.3-Codex-Spark"). The account
	// limit leaves it null — that absence is how we tell the two apart.
	LimitName string           `json:"limit_name"`
	Primary   *codexRateWindow `json:"primary"`
	Secondary *codexRateWindow `json:"secondary"`
	PlanType  string           `json:"plan_type"`
}

// isAccountLimit reports whether this object describes the ACCOUNT's quota (the number
// `codex /status` prints as "5h limit / Weekly limit") rather than one model's sub-limit.
// A named family is per-model; an unnamed family with no windows at all (the "premium"
// credits stub) tells us nothing and is equally useless.
func (rl codexRateLimits) isAccountLimit() bool {
	return rl.LimitName == "" && (rl.Primary != nil || rl.Secondary != nil)
}

// codexRolloutLine is the shape we need off one transcript line: when it happened, and the
// rate_limits it carries (codex nests events as {timestamp, type, payload}).
type codexRolloutLine struct {
	Timestamp  string           `json:"timestamp"`
	RateLimits *codexRateLimits `json:"rate_limits"`
	Payload    *struct {
		RateLimits *codexRateLimits `json:"rate_limits"`
	} `json:"payload"`
}

// latestCodexRateLimits returns the most recent ACCOUNT rate-limit reading across the few
// newest rollouts, along with when it was captured. ok=false when no such reading exists.
func latestCodexRateLimits(root string) (rl codexRateLimits, capturedAt time.Time, ok bool) {
	for _, path := range transcript.NewestFiles(root, transcript.RolloutPrefix, transcript.JSONLSuffix, codexScanFiles) {
		candidate, at, found := scanCodexRateLimits(path)
		if !found {
			continue
		}
		if !ok || at.After(capturedAt) {
			rl, capturedAt, ok = candidate, at, true
		}
	}
	return rl, capturedAt, ok
}

// scanCodexRateLimits reads the tail of one rollout and returns its newest ACCOUNT reading
// (by event timestamp) plus that timestamp. Per-model sub-limits are skipped — see the file
// header for why reading them silently reports the wrong quota.
func scanCodexRateLimits(path string) (rl codexRateLimits, capturedAt time.Time, ok bool) {
	needle := []byte("rate_limits")
	_ = transcript.ScanTail(path, transcript.DefaultTailBytes, func(line []byte) bool {
		if !bytes.Contains(line, needle) {
			return true
		}
		var row codexRolloutLine
		if json.Unmarshal(line, &row) != nil {
			return true
		}
		candidate := row.RateLimits
		if candidate == nil && row.Payload != nil {
			candidate = row.Payload.RateLimits
		}
		if candidate == nil || !candidate.isAccountLimit() {
			return true // a per-model sub-limit — reading it would report the wrong quota
		}
		at, _ := time.Parse(time.RFC3339, row.Timestamp)
		if !ok || at.After(capturedAt) {
			rl, capturedAt, ok = *candidate, at, true
		}
		return true
	})
	if ok && capturedAt.IsZero() {
		// A reading with no parseable timestamp still beats none; fall back to the file's mtime.
		if fi, err := os.Stat(path); err == nil {
			capturedAt = fi.ModTime()
		}
	}
	return rl, capturedAt, ok
}

// codexQuotaWindow maps a codex slot onto the unified QuotaWindow (5h/7d).
func codexQuotaWindow(w *codexRateWindow) (QuotaWindow, bool) {
	if w == nil {
		return QuotaWindow{}, false
	}
	kind := "7d"
	if w.WindowMinutes > 0 && w.WindowMinutes <= 300 {
		kind = "5h"
	}
	remaining := 100 - w.UsedPercent
	if remaining < 0 {
		remaining = 0
	}
	q := QuotaWindow{
		Kind:             kind,
		WindowMinutes:    w.WindowMinutes,
		UsedPercent:      w.UsedPercent,
		RemainingPercent: remaining,
	}
	if w.ResetsAt > 0 {
		q.ResetAt = time.Unix(w.ResetsAt, 0).UTC().Format(time.RFC3339)
	}
	return q, true
}
