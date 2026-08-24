// Package usage — kimi_quota.go: the Kimi For Coding subscription.
//
// # Why this is a separate account rather than a codex detail
//
// Kimi For Coding is reached THROUGH codex (a local proxy translates the Responses API into
// Kimi's chat completions), so every session lands in ~/.codex/sessions looking exactly like an
// OpenAI one. It is nonetheless a different subscription, with a different key, a different
// quota API and a different unit — and the transcripts prove they cannot be merged: a proxied
// session records {"limit_id":"codex","primary":null,…}, a well-formed rate-limit object with
// nothing in it, because the upstream returns no rate-limit headers for the proxy to pass on.
//
// So there is NO offline source here. Unlike codex, whose account limits are written to disk as
// it works, Kimi's quota exists only at the vendor. That is affordable because asking is a plain
// read: GET /coding/v1/usages costs nothing, which is why this account is kept warm by the same
// timer that refreshes codex instead of waiting for someone to press a button.
//
// # Units
//
// The response reports limit/used/remaining as STRINGS with no unit. One vendor document calls
// them request counts; this account returns limit "100" for both its weekly and its 5-hour
// window while the transcripts show 2,189 requests in that same week — so on this account they
// are a normalised scale, not requests. The only portable reading is therefore the RATIO, and
// that is all this file produces. Absolute counts are deliberately not surfaced: a number whose
// unit we cannot name is not information.
//
// # Not credits
//
// Kimi meters in window percentage, not credits. The only currency in the response is
// boosterWallet — a top-up wallet for overage, disabled on this account — which is not
// consumption. Manufacturing a "credits" figure for Kimi would be exactly the API-billing
// confusion this account exists to prevent, so Credits stays nil here, by design.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	kimiDefaultBaseURL = "https://api.kimi.com/coding/v1"
	kimiUsagesPath     = "/usages"
	kimiAPITimeout     = 15 * time.Second
	// kimiPlanFamily names the subscription-wide window. Kimi exposes exactly one such pool,
	// so the name is fixed rather than read from the payload.
	kimiPlanFamily = "plan"
	// kimiPlanWindowMinutes is the subscription window's length. The vendor documents a weekly
	// quota and this account's reset times land exactly 7 days apart (08-03 / 08-10 / 08-17 /
	// 08-24, anchored to the subscription's start), but the payload never states it. The value
	// only affects the 5h/7d label and the post-expiry roll-forward; the reset instant itself is
	// always shown as the vendor reported it.
	kimiPlanWindowMinutes = 7 * 24 * 60
)

// kimiUsagesJSON is the subset of GET /usages we read. Everything about identity, wallets and
// concurrency is deliberately left on the floor: this package reports quota, not billing.
type kimiUsagesJSON struct {
	User struct {
		Membership struct {
			Level string `json:"level"`
		} `json:"membership"`
	} `json:"user"`
	Usage  kimiWindowJSON `json:"usage"`
	Limits []struct {
		Window struct {
			Duration int    `json:"duration"`
			TimeUnit string `json:"timeUnit"`
		} `json:"window"`
		Detail kimiWindowJSON `json:"detail"`
	} `json:"limits"`
	// Authentication.Scope is the account's own statement that this key is a Coding-plan
	// subscription rather than an open-platform pay-per-token key.
	Authentication struct {
		Scope string `json:"scope"`
	} `json:"authentication"`
}

// kimiWindowJSON carries the numbers as strings, which is how the vendor sends them.
type kimiWindowJSON struct {
	Limit     string `json:"limit"`
	Used      string `json:"used"`
	Remaining string `json:"remaining"`
	ResetTime string `json:"resetTime"`
}

// window converts one slot into a unified window. ok=false when the payload does not describe a
// usable ratio — a zero or unparseable limit is not "0% used", it is no reading at all.
func (w kimiWindowJSON) window(minutes int) (QuotaWindow, bool) {
	limit, err1 := strconv.ParseFloat(strings.TrimSpace(w.Limit), 64)
	used, err2 := strconv.ParseFloat(strings.TrimSpace(w.Used), 64)
	if err1 != nil || err2 != nil || limit <= 0 {
		return QuotaWindow{}, false
	}
	percent := used / limit * 100
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	q := QuotaWindow{
		Kind:             windowKind(minutes),
		WindowMinutes:    minutes,
		UsedPercent:      percent,
		RemainingPercent: 100 - percent,
	}
	if t, err := time.Parse(time.RFC3339, w.ResetTime); err == nil {
		q.ResetAt = t.UTC().Format(time.RFC3339)
	}
	return q, true
}

// kimiMinutes normalises the vendor's {duration, timeUnit} pair into minutes. Unknown units
// yield 0 — which downstream reads as "length unstated", not as "zero-length window".
func kimiMinutes(duration int, unit string) int {
	switch strings.ToUpper(unit) {
	case "TIME_UNIT_MINUTE":
		return duration
	case "TIME_UNIT_HOUR":
		return duration * 60
	case "TIME_UNIT_DAY":
		return duration * 24 * 60
	case "TIME_UNIT_SECOND":
		return duration / 60
	default:
		return 0
	}
}

// ── provider ─────────────────────────────────────────────────────────────────

type kimiProvider struct{}

func (kimiProvider) Account() Account {
	return Account{Runtime: "codex", Vendor: VendorMoonshot}
}

// ProbeCost is ProbeFree: /usages is a plain read, so this account can be kept warm on a timer
// instead of only when someone presses 刷新.
func (kimiProvider) ProbeCost() string {
	if _, ok := credentialFor(VendorMoonshot); !ok {
		return ProbeNone
	}
	return ProbeFree
}

func (p kimiProvider) Probe(ctx context.Context) error {
	cred, ok := credentialFor(VendorMoonshot)
	if !ok {
		return errors.New("kimi: 未配置订阅 key")
	}
	ctx, cancel := context.WithTimeout(ctx, kimiAPITimeout)
	defer cancel()

	var payload kimiUsagesJSON
	if err := kimiGet(ctx, cred, kimiUsagesPath, &payload); err != nil {
		return err
	}

	snap := quotaSnapshot{
		Account:    p.Account(),
		CapturedAt: time.Now().Unix(),
		Source:     SourceProbe,
		Plan:       kimiPlanLabel(payload.User.Membership.Level),
		Billing:    BillingSubscription,
	}
	var windows []QuotaWindow
	if w, ok := payload.Usage.window(kimiPlanWindowMinutes); ok {
		windows = append(windows, w)
	}
	for _, limit := range payload.Limits {
		if w, ok := limit.Detail.window(kimiMinutes(limit.Window.Duration, limit.Window.TimeUnit)); ok {
			windows = append(windows, w)
		}
	}
	// One family: Kimi's windows are two views of ONE pool (a 5-hour rate limit inside a weekly
	// allowance), unlike codex's independent per-model sub-limits. Splitting them into families
	// would invite the UI to present them as separate budgets.
	if len(windows) == 0 {
		return errors.New("kimi: 账号未返回可用额度窗口")
	}
	snap.Families = []snapshotFamily{{Family: kimiPlanFamily, Windows: dedupeWindows(windows)}}
	return writeSnapshot(snap)
}

// Query reports what the last probe stored. There is no offline source to fall back to — see
// the file header — so an account that has never been probed reports presence without windows,
// which the UI renders as 「等待额度数据」 rather than as a fabricated full bar.
func (p kimiProvider) Query() QuotaInfo {
	account := p.Account()
	info := QuotaInfo{
		Runtime:  account.Runtime,
		Vendor:   account.Vendor,
		Display:  account.Display(),
		Health:   probeCLI(account.Runtime),
		CanProbe: p.ProbeCost() != ProbeNone,
	}
	cred, ok := credentialFor(VendorMoonshot)
	if !ok {
		return info // not configured on this host ⟹ not present ⟹ not shown
	}
	info.Present = true
	info.Evidence = []string{EvidenceCredentials}
	info.Billing = BillingSubscription
	// Which endpoints this plan is spent through — the surface needs it to keep this
	// subscription apart from the same vendor's pay-per-token traffic.
	info.Endpoints = cred.RuntimeProviderIDs
	// Same question, same single source as the OpenAI account asks: whose bill is the traffic
	// being produced right now. Answering it per-account is what lets exactly one row say
	// 「当前计费」 instead of both rows looking equally plausible.
	info.Attribution = codexAttribution(account, codexBilledToProvider())

	readings, _ := readSnapshotReadings(account)
	if len(readings) == 0 {
		info.Note = "已配置订阅 key · 等待首次查询"
		return info
	}
	info.Evidence = append(info.Evidence, EvidenceSnapshot)
	info.applyReadings(readings)
	info.Note = "账号额度 · 官方接口"
	return info
}

// kimiPlanLabel turns the vendor's enum into something a person reads. Unknown levels pass
// through: an unfamiliar plan name is still the plan's name.
func kimiPlanLabel(level string) string {
	return strings.ToLower(strings.TrimPrefix(level, "LEVEL_"))
}

func kimiGet(ctx context.Context, cred Credential, path string, out any) error {
	base := strings.TrimSuffix(orDefault(cred.BaseURL, kimiDefaultBaseURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cred.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return errors.New("kimi: 订阅 key 无效或已过期")
	case resp.StatusCode == http.StatusNotFound:
		// The open-platform (pay-per-token) key hits the same host and has no /usages. Naming
		// that specifically is the difference between a user fixing it and a user guessing.
		return errors.New("kimi: 该 key 没有订阅额度接口（开放平台按量 key 不是订阅 key）")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("kimi: 账号接口返回 HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
