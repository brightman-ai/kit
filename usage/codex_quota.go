// Package usage — codex_quota.go: the codex ACCOUNT rate limit, read from the rollout
// transcripts codex writes to ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl.
//
// Three things make this harder than "read the last rate_limits object", and getting any of
// them wrong produces numbers that look plausible and are simply false:
//
//  1. A rollout carries SEVERAL limit families. Alongside the account limit
//     (limit_id "codex", no limit_name) codex emits per-model sub-limits — e.g.
//     {"limit_id":"codex_bengalfox","limit_name":"GPT-5.3-Codex-Spark"} — which have their
//     OWN 5h/7d windows and their own, much lower, usage. The sub-limit heartbeats more
//     often, so the LAST rate_limits in a file is usually a sub-limit. Reading it reported
//     "92% left" while `codex /status` said 26% — the account was nearly out of quota and
//     the UI said it was fine. We therefore select the UNNAMED (account) family here and let
//     the probe publish sub-limits explicitly, where they arrive already labelled.
//
//  2. The newest file's newest ACCOUNT entry for a family is not its last line, and may not
//     even be in the newest file. We take the greatest EVENT timestamp per account family
//     across recent rollouts, and use it as each reading's capture time — so "更新于 …"
//     tells the truth instead of tracking a file's mtime.
//
//  3. NOT EVERY ROLLOUT IS THIS ACCOUNT'S. Point codex at a translating proxy and it writes
//     rollouts exactly as before — same shape, same model names — but the traffic is billed
//     to another vendor, and the upstream returns no rate limits, so codex records
//     {"limit_id":"codex","primary":null,"secondary":null,…}: a well-formed object with
//     nothing in it. Scanning a fixed handful of newest files therefore went blind after a
//     few days of proxied work: 2026-08-22 the four newest rollouts were all proxied, while
//     the account's real reading (used 100%) sat in the 21st file. The UI said "7 天无数据"
//     about data that was on disk. We now read each rollout's session_meta.model_provider
//     first and scan only the ones this account actually paid for.
//
// Read-only, no API call, no auth: the same data `codex /status` shows is already on disk.
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"time"

	"github.com/brightman-ai/kit/transcript"
)

// codexScanFiles bounds how far back a scan walks. It is a BACKSTOP, not a policy: the scan
// stops as soon as it has an account reading (see codexRolloutScan), so the bound only costs
// work in the pathological case of an account that has not run for months. It must stay
// comfortably larger than a burst of proxied sessions — the whole point of item 3 above.
const codexScanFiles = 80

// codexOwnProvider is the value session_meta.model_provider carries when codex is talking to
// OpenAI itself. Older rollouts omit the field entirely, which means the same thing.
const codexOwnProvider = "openai"

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
// A named family is per-model; an unnamed family with no windows at all is what a PROXIED
// session records, and it tells us nothing.
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

// codexSessionMeta is the first line of every rollout. Only the provider is read: it decides
// WHOSE account the session's traffic was billed to.
type codexSessionMeta struct {
	Type    string `json:"type"`
	Payload struct {
		ModelProvider string `json:"model_provider"`
	} `json:"payload"`
}

// codexBilledToProvider returns the runtime-side provider id of the NEWEST codex session on this
// host — i.e. who the traffic being produced right now is billed to. "" when there are no
// sessions, or when the rollout predates the field (which by definition means OpenAI itself).
//
// This is one line of one file, so every account may ask it freely; it is the single source for
// the "is this account the one currently being billed?" question that both codex accounts need.
func codexBilledToProvider() string {
	files := transcript.NewestFiles(codexSessionsDir(), transcript.RolloutPrefix, transcript.JSONLSuffix, 1)
	if len(files) == 0 {
		return ""
	}
	return rolloutProvider(files[0])
}

// codexRolloutScan walks recent rollouts newest-first and returns the newest ACCOUNT reading per
// limit family, considering only the sessions THIS account actually paid for.
func codexRolloutScan() []*Reading {
	root := codexSessionsDir()
	byFamily := make(map[string]codexRateLimitObservation)

	for _, path := range transcript.NewestFiles(root, transcript.RolloutPrefix, transcript.JSONLSuffix, codexScanFiles) {
		if provider := rolloutProvider(path); provider != "" && provider != codexOwnProvider {
			continue // somebody else's bill — its rate_limits are empty by construction
		}
		for family, candidate := range scanCodexRateLimitsByFamily(path) {
			current, exists := byFamily[family]
			if !exists || candidate.CapturedAt.After(current.CapturedAt) {
				byFamily[family] = candidate
			}
		}
		// One account reading is enough: files are newest-first, so anything older can only
		// lose the freshness comparison. Bailing here is what keeps a months-idle account from
		// costing a full-tree walk.
		if len(byFamily) > 0 {
			break
		}
	}

	account := Account{Runtime: "codex", Vendor: VendorOpenAI}
	readings := make([]*Reading, 0, len(byFamily))
	for _, observation := range byFamily {
		rl := observation.RateLimits
		readings = append(readings, &Reading{
			Account:    account,
			CapturedAt: observation.CapturedAt,
			Source:     SourceRollout,
			Plan:       rl.PlanType,
			Family:     rl.LimitID,
			Billing:    BillingSubscription,
			Windows:    codexWindows(rl.Primary, rl.Secondary),
		})
	}
	return newestReadingsByFamily(readings...)
}

// rolloutProvider reads session_meta (always the FIRST line) and returns the runtime-side
// provider id the session ran against. "" when the field is absent — older rollouts predate it
// and were, by definition, talking to OpenAI.
func rolloutProvider(path string) string {
	f, err := os.Open(path) //nolint:gosec — read-only transcript scan
	if err != nil {
		return ""
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 64*1024)
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return ""
	}
	var meta codexSessionMeta
	if json.Unmarshal(bytes.TrimRight(line, "\r\n"), &meta) != nil {
		return ""
	}
	return meta.Payload.ModelProvider
}

// codexWindows maps codex's primary/secondary slots onto the unified windows.
func codexWindows(primary, secondary *codexRateWindow) []QuotaWindow {
	var out []QuotaWindow
	if w, ok := codexQuotaWindow(primary); ok {
		out = append(out, w)
	}
	if w, ok := codexQuotaWindow(secondary); ok {
		out = append(out, w)
	}
	return out
}

type codexRateLimitObservation struct {
	RateLimits codexRateLimits
	CapturedAt time.Time
}

// scanCodexRateLimitsByFamily reads the tail of one rollout and returns its newest ACCOUNT
// reading per family (by event timestamp). Per-model sub-limits are skipped — see the file
// header for why reading them silently reports the wrong quota.
func scanCodexRateLimitsByFamily(path string) map[string]codexRateLimitObservation {
	byFamily := make(map[string]codexRateLimitObservation)
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
			return true // a per-model sub-limit, or a proxied session's empty shell
		}
		at, _ := time.Parse(time.RFC3339, row.Timestamp)
		family := candidate.LimitID
		current, exists := byFamily[family]
		if !exists || at.After(current.CapturedAt) {
			byFamily[family] = codexRateLimitObservation{RateLimits: *candidate, CapturedAt: at}
		}
		return true
	})
	// A reading with no parseable timestamp still beats none; fall back to the file's mtime.
	if fi, err := os.Stat(path); err == nil {
		for family, observation := range byFamily {
			if observation.CapturedAt.IsZero() {
				observation.CapturedAt = fi.ModTime()
				byFamily[family] = observation
			}
		}
	}
	return byFamily
}

// codexQuotaWindow maps a codex slot onto the unified QuotaWindow (5h/7d).
//
// A slot with no window length is not a window — the label 5h/7d is DERIVED from that length,
// so without it there is nothing to call the thing. (The account reports its unused secondary
// slot exactly this way, and treating it as a window invented a phantom that collided with the
// real one.)
func codexQuotaWindow(w *codexRateWindow) (QuotaWindow, bool) {
	if w == nil || w.WindowMinutes <= 0 {
		return QuotaWindow{}, false
	}
	remaining := 100 - w.UsedPercent
	if remaining < 0 {
		remaining = 0
	}
	q := QuotaWindow{
		Kind:             windowKind(w.WindowMinutes),
		WindowMinutes:    w.WindowMinutes,
		UsedPercent:      w.UsedPercent,
		RemainingPercent: remaining,
	}
	if w.ResetsAt > 0 {
		q.ResetAt = time.Unix(w.ResetsAt, 0).UTC().Format(time.RFC3339)
	}
	return q, true
}
