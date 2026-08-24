// Package usage — provider.go: the quota domain's three abstractions.
//
// An account's quota is assembled from READINGS, served by a PROVIDER, addressed by an ACCOUNT.
//
//	Account  — WHOSE quota (runtime × vendor). See account.go: one CLI can bill two vendors.
//	Reading  — one observation of one account family's windows, with a time and a provenance.
//	           Several sources can produce one (a statusline hook, a rollout transcript, a live
//	           probe); the freshest wins inside that family, and the winner remembers where it
//	           came from.
//	Provider — one account's quota domain: how to observe it offline, and what asking costs.
//
// All three exist because the alternatives kept costing us. QueryAllQuotas used to hardcode a
// call per runtime, so adding one meant editing two places; the refresh endpoint hardcoded which
// runtimes could be probed, so the domain leaked into the transport; a reading carried no
// provenance, so "why does refresh change nothing?" had no answer without reading rollout files
// by hand; and keying by runtime left a second subscription on the same CLI with nowhere to live.
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
	SourceProbe   = "probe"   // we asked the vendor directly
)

// What asking an account costs. This is domain knowledge, not transport policy: it decides
// whether a reading may be kept warm automatically or must wait for a person to ask.
const (
	// ProbeFree — a plain read against the vendor's API. Safe on a timer.
	ProbeFree = "free"
	// ProbeNone — nothing to ask (no endpoint, or no credential on this host).
	ProbeNone = "none"
)

// Reading is one observation of an account's quota windows.
type Reading struct {
	Account    Account
	CapturedAt time.Time
	Source     string // SourceHook | SourceRollout | SourceProbe
	Plan       string
	Billing    string // BillingSubscription | BillingAPI — an api reading has no windows by design
	// Family is WHICH set of limits this reading describes. Codex accounts have several
	// ("codex" = the account pool, plus per-model features metered separately), and the vendor
	// can expose more than one at a time. Two families are not two views of one truth — they
	// have different windows entirely — so readings from different families must never be
	// merged or overwrite each other. Freshness only chooses a winner inside one family.
	Family string
	// FamilyLabel is the family's human name, when the vendor gives one distinct from its id.
	FamilyLabel string
	Windows     []QuotaWindow
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
// A family is part of a reading's identity, not decoration. An account pool and a per-model
// feature pool coexist with different windows, so a newer observation of one must never erase
// the other. Time only resolves competing SOURCES within one family — which is why the probe
// and the transcript must agree on family names; when they did not, the same window arrived
// under two names and the two halves could never refresh each other. The returned slice is
// newest-first so callers have a deterministic compatibility projection.
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

// QuotaProvider is one account's quota domain.
//
// Offline observation is mandatory: every account can at least report what is already on disk
// (which for some is "nothing yet", stated plainly). Probing is optional — claude exposes its
// usage ONLY through the statusLine hook, so there is nothing to ask; codex and kimi both answer
// a plain GET. ProbeCost states that rather than making the caller know it.
type QuotaProvider interface {
	// Account identifies whose quota this provider serves.
	Account() Account
	// Query assembles the account's axes from what is already on disk. Never reaches out.
	Query() QuotaInfo
	// ProbeCost reports what asking this vendor costs: ProbeFree | ProbeNone.
	ProbeCost() string
	// Probe asks the vendor directly and persists the answer so the offline path sees it too.
	Probe(ctx context.Context) error
}

// providers is the registry. Adding an account means adding it HERE, and nowhere else.
func providers() []QuotaProvider {
	return []QuotaProvider{
		claudeProvider{}, zhipuProvider{}, // runtime "claude" → Anthropic 或 智谱
		codexProvider{}, kimiProvider{}, //  runtime "codex"  → OpenAI 或 Kimi
		geminiProvider{},
	}
}

// QueryAllQuotas returns the quota state of every known account. Pure and instant: it reads
// files, never the network, so the UI paints immediately and a slow vendor can never stall it.
func QueryAllQuotas() []QuotaInfo {
	all := providers()
	out := make([]QuotaInfo, 0, len(all))
	for _, p := range all {
		out = append(out, p.Query())
	}
	return out
}

// ProbeResult reports what one account's probe did. An account that cannot be asked says so
// rather than being silently absent — "we did not ask" and "we asked and it failed" are
// different facts, and the UI phrases them differently.
type ProbeResult struct {
	Runtime string `json:"runtime"`
	Vendor  string `json:"vendor,omitempty"`
	Display string `json:"display,omitempty"`
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

// ProbeAll asks every account that can be asked for its current quota, and reports what each
// one did. It never returns an error: a failed probe degrades to the last-known reading, which
// the caller re-queries afterwards.
//
// Every probe is now a plain read (see codex_api.go for why the chat-request version had to
// go), so this is safe to call on a timer as well as from the refresh button.
func ProbeAll(ctx context.Context) []ProbeResult {
	return probeMatching(ctx, func(QuotaProvider) bool { return true })
}

// RefreshStale probes only the accounts whose stored reading is older than maxAge. It is the
// background warmer: reads stay instant because someone else already fetched, and a vendor is
// never asked more often than the data can plausibly have changed.
func RefreshStale(ctx context.Context, maxAge time.Duration) []ProbeResult {
	return probeMatching(ctx, func(p QuotaProvider) bool {
		return snapshotAge(p.Account()) >= maxAge
	})
}

func probeMatching(ctx context.Context, want func(QuotaProvider) bool) []ProbeResult {
	all := providers()
	out := make([]ProbeResult, 0, len(all))
	for _, p := range all {
		account := p.Account()
		result := ProbeResult{Runtime: account.Runtime, Vendor: account.Vendor, Display: account.Display()}
		if p.ProbeCost() == ProbeNone {
			result.Status = ProbeNotSupported
			out = append(out, result)
			continue
		}
		if !want(p) {
			continue // fresh enough — not asking is not a result worth reporting
		}
		if err := p.Probe(ctx); err != nil {
			result.Status, result.Reason = ProbeFailed, err.Error()
			out = append(out, result)
			continue
		}
		result.Status = ProbeOK
		out = append(out, result)
	}
	return out
}

// snapshotAge is how long ago we last stored a reading for an account. A never-probed account
// reports an effectively infinite age so the warmer picks it up on its first pass.
func snapshotAge(a Account) time.Duration {
	readings, _ := readSnapshotReadings(a)
	newest := newestReading(readings...)
	if newest == nil {
		return time.Duration(1<<62 - 1)
	}
	return time.Since(newest.CapturedAt)
}
