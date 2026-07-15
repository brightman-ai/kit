// Package usage — provider.go: the quota domain's two abstractions.
//
// A runtime's quota is assembled from READINGS, and served by a PROVIDER.
//
//	Reading  — one observation of one account quota family's windows, with a time and a
//	           provenance. Several sources can produce one (a statusline hook, a rollout
//	           transcript, a live probe); the freshest wins inside that family, and the winner
//	           remembers where it came from.
//	Provider — one runtime's quota domain: how to observe it offline, and whether it can be
//	           asked directly.
//
// Both exist because the alternative kept costing us. QueryAllQuotas used to hardcode a call
// per runtime, so adding one meant editing two places; the refresh endpoint hardcoded which
// runtimes could be probed, so the domain leaked into the transport; and a reading carried no
// provenance, so when a user asked "why does refresh change nothing?" nobody could answer
// without going and reading rollout files by hand. (The answer: codex only records the
// rate-limit family of the model it is currently running, so a session on a per-model plan
// stops refreshing the ACCOUNT limit entirely.) Snapshot.Source now says that out loud.
package usage

import (
	"context"
	"sort"
	"time"
)

// Reading provenance — WHERE a quota observation came from. Surfaced to the UI, because
// "which source is this number from, and can refreshing it help?" is a question users
// actually ask.
const (
	SourceHook    = "hook"    // claude's statusLine hook drop file (claude reports as it renders)
	SourceRollout = "rollout" // codex's rollout transcript (codex reports as it works)
	SourceProbe   = "probe"   // we asked the provider directly (user-initiated refresh)
)

// Reading is one observation of a runtime's quota windows.
type Reading struct {
	CapturedAt time.Time
	Source     string // SourceHook | SourceRollout | SourceProbe
	Plan       string
	Billing    string // BillingSubscription | BillingAPI — an api reading has no windows by design
	// Family is WHICH set of limits this reading describes. Codex accounts have several
	// ("codex" = 5h+7d, "premium" = a single 7-day window, plus per-model families), and the
	// provider can expose more than one over time. Two families are not two views of one truth —
	// they have different windows entirely — so readings from different families must never
	// be merged or overwrite each other. Freshness only chooses a winner inside one family.
	Family  string
	Windows []QuotaWindow
}

// newestReading picks the freshest of several observations, ignoring the ones that do not
// exist. This is the whole merge rule: a probe result must not be reverted by the next poll
// re-reading an older transcript, and a transcript that has moved on must not be held back by
// an older probe.
func newestReading(readings ...*Reading) *Reading {
	var best *Reading
	for _, r := range readings {
		if r == nil {
			continue
		}
		if best == nil || r.CapturedAt.After(best.CapturedAt) {
			best = r
		}
	}
	return best
}

// newestReadingsByFamily keeps one independently-current reading per quota family.
//
// A family is part of a reading's identity, not decoration. "premium" and "codex" can
// coexist for the same account (different models are governed by different limit sets), so a
// newer observation of one must never erase the other. Time only resolves competing sources
// WITHIN one family. The returned slice is newest-first so callers have a deterministic
// compatibility projection for consumers that still understand only one reading.
func newestReadingsByFamily(readings ...*Reading) []*Reading {
	byFamily := make(map[string]*Reading)
	for _, r := range readings {
		if r == nil {
			continue
		}
		current := byFamily[r.Family]
		if current == nil || r.CapturedAt.After(current.CapturedAt) {
			byFamily[r.Family] = r
		}
	}

	out := make([]*Reading, 0, len(byFamily))
	for _, r := range byFamily {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CapturedAt.Equal(out[j].CapturedAt) {
			return out[i].Family < out[j].Family
		}
		return out[i].CapturedAt.After(out[j].CapturedAt)
	})
	return out
}

// QuotaProvider is one runtime's quota domain.
//
// Offline observation is mandatory: every runtime can at least be looked at on disk. Probing
// is optional — claude exposes its 5h/7d usage ONLY through the statusLine hook, so there is
// nothing to ask; codex answers directly. CanProbe() states that rather than making the
// caller know it.
type QuotaProvider interface {
	Runtime() string
	// Query assembles the runtime's four axes from what is already on disk. Never reaches out.
	Query() QuotaInfo
	// CanProbe reports whether this runtime can be asked for its current quota.
	CanProbe() bool
	// Probe asks the provider directly and persists the answer so the offline path sees it too.
	// Only ever called on an explicit user request — it costs a real provider round-trip.
	Probe(ctx context.Context) error
}

// providers is the registry. Adding a runtime means adding it HERE, and nowhere else.
func providers() []QuotaProvider {
	return []QuotaProvider{claudeProvider{}, codexProvider{}, geminiProvider{}}
}

// QueryAllQuotas returns the quota state of every known runtime.
func QueryAllQuotas() []QuotaInfo {
	all := providers()
	out := make([]QuotaInfo, 0, len(all))
	for _, p := range all {
		out = append(out, p.Query())
	}
	return out
}

// ProbeResult reports what one runtime's probe did. A runtime that cannot be probed says so
// rather than being silently absent — "we did not ask" and "we asked and it failed" are
// different facts, and the UI phrases them differently.
type ProbeResult struct {
	Runtime string `json:"runtime"`
	// Status is "ok" | "failed" | "not_supported".
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Probe statuses.
const (
	ProbeOK           = "ok"
	ProbeFailed       = "failed"
	ProbeNotSupported = "not_supported"
)

// ProbeAll asks every runtime that can be asked for its current quota, and reports what each
// one did. It never returns an error: a failed probe degrades to the last-known reading, which
// the caller re-queries afterwards.
//
// USER-INITIATED ONLY. Each probe is a real provider request; the background poll must never
// call this.
func ProbeAll(ctx context.Context) []ProbeResult {
	all := providers()
	out := make([]ProbeResult, 0, len(all))
	for _, p := range all {
		if !p.CanProbe() {
			out = append(out, ProbeResult{Runtime: p.Runtime(), Status: ProbeNotSupported})
			continue
		}
		if err := p.Probe(ctx); err != nil {
			out = append(out, ProbeResult{Runtime: p.Runtime(), Status: ProbeFailed, Reason: err.Error()})
			continue
		}
		out = append(out, ProbeResult{Runtime: p.Runtime(), Status: ProbeOK})
	}
	return out
}
