// Package usage — codex_quota.go: real codex subscription quota from the rollout
// transcript. Codex writes its account rate-limit state into
// ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl as `token_count` events carrying a
// `rate_limits` object (primary=5h, secondary=7d). We read the newest rollout's
// LAST such object — that is the most recent account-level snapshot.
//
// Same source abtop reads (no API, no auth; read-only). The data is persisted on
// disk so the quota endpoint can answer WITHOUT a live run.
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// codexRateWindow mirrors one slot of codex's rate_limits (primary/secondary).
type codexRateWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

// codexRateLimits is the rate_limits object embedded in a token_count event.
type codexRateLimits struct {
	Primary   *codexRateWindow `json:"primary"`
	Secondary *codexRateWindow `json:"secondary"`
	PlanType  string           `json:"plan_type"`
}

// codexSessionsDir returns ~/.codex/sessions (overridable for tests via the arg).
func codexSessionsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "sessions")
}

// latestCodexRateLimits walks the rollout tree, opens the newest rollout-*.jsonl,
// and returns the LAST account-level rate_limits object in it. ok=false when no
// rollout / no rate_limits exists.
func latestCodexRateLimits(root string) (codexRateLimits, bool) {
	var newest string
	var newestMod time.Time
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		if info, e := d.Info(); e == nil && info.ModTime().After(newestMod) {
			newestMod = info.ModTime()
			newest = path
		}
		return nil
	})
	if newest == "" {
		return codexRateLimits{}, false
	}
	return scanCodexRateLimits(newest)
}

// scanCodexRateLimits reads one rollout file and returns its LAST rate_limits.
// rate_limits sits either at the line top level or under `payload` (codex wraps
// events as {type, payload}); we check both, keeping the most recent non-empty.
func scanCodexRateLimits(path string) (codexRateLimits, bool) {
	f, err := os.Open(path) //nolint:gosec — read-only quota probe
	if err != nil {
		return codexRateLimits{}, false
	}
	defer f.Close()

	var last codexRateLimits
	found := false
	br := bufio.NewReaderSize(f, 256*1024)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, []byte("rate_limits")) {
			var row struct {
				RateLimits *codexRateLimits `json:"rate_limits"`
				Payload    *struct {
					RateLimits *codexRateLimits `json:"rate_limits"`
				} `json:"payload"`
			}
			if json.Unmarshal(bytes.TrimRight(line, "\r\n"), &row) == nil {
				rl := row.RateLimits
				if rl == nil && row.Payload != nil {
					rl = row.Payload.RateLimits
				}
				if rl != nil && (rl.Primary != nil || rl.Secondary != nil) {
					last = *rl
					found = true
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	return last, found
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
