// Package usage — zhipu_quota.go: the GLM Coding Plan subscription (智谱).
//
// # Why it is a claude-runtime account
//
// GLM Coding Plan is consumed through Claude Code by pointing ANTHROPIC_BASE_URL at
// open.bigmodel.cn — so the runtime is "claude" and the vendor is "zhipu". That is the same
// shape as Kimi behind codex, mirrored: proof that runtime and vendor really are independent
// axes rather than two names for one thing.
//
// Unlike the codex/Kimi case, the model NAME is trustworthy here: GLM sessions land in the
// claude transcripts as `glm-5.3`, never as `claude-*` (verified: 72 glm rows alongside
// claude-opus-5/sonnet-5/fable-5 in the same tree). No proxy is rewriting ids, so attribution
// can read the model directly — see claudeBilledToVendor.
//
// # What the vendor reports
//
// GET /api/monitor/usage/quota/limit returns `data.limits[]`, three entries on this account:
//
//	TOKENS_LIMIT unit=3 number=5   → the 5-hour token cycle
//	TOKENS_LIMIT unit=6 number=1   → the weekly token budget
//	TIME_LIMIT   unit=5 number=1   → a MONTHLY allowance of MCP tool calls (1000, with a
//	                                 per-tool usageDetails breakdown)
//
// Only the TOKENS_LIMIT entries become quota windows. The TIME_LIMIT one is a different
// resource measured in a different unit — tool invocations, not tokens — and rendering it as a
// third bar beside them would invite exactly the reading it does not support ("I have used 1%
// of my plan"). It is left out deliberately rather than forgotten.
//
// Since 2026-02 the vendor reports only `percentage`, not token counts. That costs nothing
// here: this package renders ratios and never absolute counts anyway.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	zhipuDefaultBaseURL = "https://open.bigmodel.cn/api/monitor"
	zhipuQuotaPath      = "/usage/quota/limit"
	zhipuAPITimeout     = 15 * time.Second
	// zhipuPlanFamily names the subscription-wide pool. Like Kimi's, the 5-hour and weekly
	// windows are two views of ONE budget, not two budgets.
	zhipuPlanFamily = "plan"
)

// zhipuQuotaJSON is the subset of the quota response we read.
type zhipuQuotaJSON struct {
	Code    int  `json:"code"`
	Success bool `json:"success"`
	Msg     string
	Data    struct {
		Level  string `json:"level"` // plan tier: "lite" | "pro" | "max"
		Limits []struct {
			Type string `json:"type"` // TOKENS_LIMIT | TIME_LIMIT
			// Unit/Number describe the window as {count, unit-code}. The codes are the vendor's
			// own enum and are not documented; these three are the ones this account returns, and
			// each was confirmed against its own nextResetTime (week → reset in 5.9d, month →
			// reset in 21.9d). An unrecognised code yields no length rather than a guess.
			Unit          int     `json:"unit"`
			Number        int     `json:"number"`
			Percentage    float64 `json:"percentage"`
			NextResetTime int64   `json:"nextResetTime"` // epoch MILLIseconds; absent on an untouched window
		} `json:"limits"`
	} `json:"data"`
}

// Vendor unit codes, each confirmed against the reset time it came with.
const (
	zhipuUnitHour  = 3
	zhipuUnitMonth = 5
	zhipuUnitWeek  = 6
)

// zhipuWindowMinutes converts the vendor's {number, unit} pair into minutes. 0 means "the vendor
// used a code we have never seen" — which downstream reads as length-unstated, never as zero.
func zhipuWindowMinutes(number, unit int) int {
	switch unit {
	case zhipuUnitHour:
		return number * 60
	case zhipuUnitWeek:
		return number * 7 * 24 * 60
	case zhipuUnitMonth:
		return number * 30 * 24 * 60
	default:
		return 0
	}
}

// ── provider ─────────────────────────────────────────────────────────────────

type zhipuProvider struct{}

func (zhipuProvider) Account() Account {
	return Account{Runtime: "claude", Vendor: VendorZhipu}
}

func (zhipuProvider) ProbeCost() string {
	if _, ok := credentialFor(VendorZhipu); !ok {
		return ProbeNone
	}
	return ProbeFree
}

func (p zhipuProvider) Probe(ctx context.Context) error {
	cred, ok := credentialFor(VendorZhipu)
	if !ok {
		return errors.New("zhipu: 未配置订阅 key")
	}
	ctx, cancel := context.WithTimeout(ctx, zhipuAPITimeout)
	defer cancel()

	var payload zhipuQuotaJSON
	if err := zhipuGet(ctx, cred, &payload); err != nil {
		return err
	}
	if !payload.Success {
		return fmt.Errorf("zhipu: 额度接口返回 code %d", payload.Code)
	}

	var windows []QuotaWindow
	for _, limit := range payload.Data.Limits {
		if limit.Type != "TOKENS_LIMIT" {
			continue // the MCP tool-call allowance is a different resource — see the file header
		}
		used := limit.Percentage
		if used < 0 {
			used = 0
		}
		if used > 100 {
			used = 100
		}
		minutes := zhipuWindowMinutes(limit.Number, limit.Unit)
		w := QuotaWindow{
			Kind: windowKind(minutes), WindowMinutes: minutes,
			UsedPercent: used, RemainingPercent: 100 - used,
		}
		if limit.NextResetTime > 0 {
			w.ResetAt = time.UnixMilli(limit.NextResetTime).UTC().Format(time.RFC3339)
		}
		windows = append(windows, w)
	}
	if len(windows) == 0 {
		return errors.New("zhipu: 账号未返回 token 额度窗口")
	}

	return writeSnapshot(quotaSnapshot{
		Account:    p.Account(),
		CapturedAt: time.Now().Unix(),
		Source:     SourceProbe,
		Plan:       strings.ToLower(payload.Data.Level),
		Billing:    BillingSubscription,
		Families:   []snapshotFamily{{Family: zhipuPlanFamily, Windows: dedupeWindows(windows)}},
	})
}

// Query reports the last probe result. Like Kimi, there is no offline source: the plan's quota
// lives at the vendor and nowhere on this disk.
func (p zhipuProvider) Query() QuotaInfo {
	account := p.Account()
	info := QuotaInfo{
		Runtime:  account.Runtime,
		Vendor:   account.Vendor,
		Display:  account.Display(),
		Health:   probeCLI(account.Runtime),
		CanProbe: p.ProbeCost() != ProbeNone,
	}
	cred, ok := credentialFor(VendorZhipu)
	if !ok {
		return info
	}
	info.Present = true
	info.Evidence = []string{EvidenceCredentials}
	info.Billing = BillingSubscription
	// Which endpoints this plan is spent through — the surface needs it to keep this
	// subscription apart from the same vendor's pay-per-token traffic.
	info.Endpoints = cred.RuntimeProviderIDs
	info.Attribution = claudeAttribution(account)

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

func zhipuGet(ctx context.Context, cred Credential, out any) error {
	base := strings.TrimSuffix(orDefault(cred.BaseURL, zhipuDefaultBaseURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+zhipuQuotaPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cred.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return errors.New("zhipu: 订阅 key 无效或已过期")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("zhipu: 额度接口返回 HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
