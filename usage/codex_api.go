// Package usage — codex_api.go: asking the OpenAI account directly.
//
// # Why this replaced a chat request
//
// The previous probe learned the account's rate limits by POSTing a real inference request to
// /backend-api/codex/responses and reading the x-codex-* response headers. It worked, and it
// cost the user quota every time they pressed 刷新 — which is why the code had to forbid the
// background poll from ever calling it, and why "refresh" was a button you had to think twice
// about. That was a workaround for not knowing the read-only endpoint existed.
//
// GET /backend-api/wham/usage returns the same rate limits as a plain read, and MORE than the
// headers ever carried: the per-model sub-limits (additional_rate_limits) arrive already
// labelled instead of having to be reverse-engineered out of transcripts. It costs nothing.
//
// A second consequence matters as much: the headers named the account's limit family with
// x-codex-active-limit ("premium") while the transcript names the same window limit_id
// ("codex"). Two names for one window meant the per-family merge treated them as independent
// truths that could never refresh each other, and the UI showed a week-old "premium" row while
// a current "codex" reading sat unread. Reading ONE contract instead of two removes the
// mismatch by construction rather than by a mapping table that would have to be maintained.
//
// # Credits
//
// The windows say what FRACTION is gone; credits say how much was actually spent. Only the
// account can answer that: a rollout records tokens but never a price, and pricing them locally
// is provably wrong — measured against this API across six days, a local rate-card computation
// came out 2.5× low every single day, because the Fast (priority) speed multiplier is not
// recorded in the transcript in any reliable way. So credits are read, never computed.
//
// They are also never DIVIDED. The two numbers are separate meters, not two views of one: across
// two complete windows on the same account the credits-per-percentage-point came out 630.2 and
// 166.9. See the Credits type comment for the full measurement and what replaced the derivation.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	codexAPIHost    = "https://chatgpt.com"
	codexUsagePath  = "/backend-api/wham/usage"
	codexDailyPath  = "/backend-api/wham/analytics/daily-workspace-usage-counts"
	codexAPITimeout = 20 * time.Second
	// codexAccountFamily is the family name for the account-wide limit. It matches the
	// limit_id codex itself writes into rollouts, which is what lets a probe and a transcript
	// reading refresh each other instead of coexisting as two stale halves.
	codexAccountFamily = "codex"
)

// codexAuth is the OAuth half of ~/.codex/auth.json. Only what authorizes one read is taken.
type codexAuth struct {
	Tokens *struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

// codexCredentials reads the OAuth token and account id. The token is used for exactly the
// calls below and goes nowhere else — never logged, never persisted, never returned upward.
func codexCredentials() (token, accountID string, err error) {
	data, err := os.ReadFile(codexAuthPath()) //nolint:gosec — the auth file is the point
	if err != nil {
		return "", "", err
	}
	var auth codexAuth
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", "", err
	}
	if auth.Tokens == nil || strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		return "", "", errors.New("codex: 未登录订阅账号（API key 账号没有订阅额度）")
	}
	return auth.Tokens.AccessToken, auth.Tokens.AccountID, nil
}

// codexWindowJSON is one window slot as /wham/usage reports it. Note the units differ from the
// transcript's: seconds here, minutes there.
type codexWindowJSON struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int     `json:"limit_window_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type codexRateLimitJSON struct {
	PrimaryWindow   *codexWindowJSON `json:"primary_window"`
	SecondaryWindow *codexWindowJSON `json:"secondary_window"`
}

type codexUsageJSON struct {
	PlanType  string             `json:"plan_type"`
	RateLimit codexRateLimitJSON `json:"rate_limit"`
	// AdditionalRateLimits are the per-model sub-limits, each metering a named feature out of
	// its own pool. They are NOT a view of the account limit and must never overwrite it.
	AdditionalRateLimits []struct {
		LimitName      string             `json:"limit_name"`
		MeteredFeature string             `json:"metered_feature"`
		RateLimit      codexRateLimitJSON `json:"rate_limit"`
	} `json:"additional_rate_limits"`
}

// probeCodexQuota asks the account for its current limits and spend, and persists the answer.
func probeCodexQuota(ctx context.Context) error {
	token, accountID, err := codexCredentials()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, codexAPITimeout)
	defer cancel()

	var usage codexUsageJSON
	if err := codexGet(ctx, token, accountID, codexUsagePath, &usage); err != nil {
		return err
	}

	snap := quotaSnapshot{
		Account:    Account{Runtime: "codex", Vendor: VendorOpenAI},
		CapturedAt: time.Now().Unix(),
		Source:     SourceProbe,
		Plan:       usage.PlanType,
		Billing:    BillingSubscription,
	}
	if windows := codexJSONWindows(usage.RateLimit); len(windows) > 0 {
		snap.Families = append(snap.Families, snapshotFamily{Family: codexAccountFamily, Windows: windows})
	}
	for _, extra := range usage.AdditionalRateLimits {
		windows := codexJSONWindows(extra.RateLimit)
		if len(windows) == 0 {
			continue
		}
		snap.Families = append(snap.Families, snapshotFamily{
			// Merge on the metered-feature id — the same string codex writes as limit_id — and
			// show the vendor's own name for it.
			Family:  orDefault(extra.MeteredFeature, extra.LimitName),
			Label:   extra.LimitName,
			Windows: windows,
		})
	}
	if len(snap.Families) == 0 {
		return errors.New("codex: 账号未返回任何额度窗口")
	}

	// Credits are a bonus, not a precondition: the daily aggregate lags the live window by
	// minutes to hours, and a fresh window legitimately has nothing yet. Never let that failure
	// discard the windows we just fetched.
	_, prior := readSnapshotReadings(snap.Account)
	if credits, err := codexCredits(ctx, token, accountID, usage.RateLimit.PrimaryWindow, prior); err == nil {
		snap.Credits = credits
	} else if prior != nil {
		// The daily ledger is unavailable, but what the previous window spent is a settled fact
		// that a transient backend hiccup cannot unmake. Keeping it stops a working panel from
		// going blank; the current window's spend is simply absent, which is true.
		snap.Credits = &Credits{
			Source: prior.Source, PriorWindow: prior.PriorWindow, PriorWindowStart: prior.PriorWindowStart,
		}
	}
	return writeSnapshot(snap)
}

// codexJSONWindows maps the API's primary/secondary slots onto unified windows. A slot with no
// length is not a window — see codexQuotaWindow for why fabricating one is worse than omitting.
func codexJSONWindows(rl codexRateLimitJSON) []QuotaWindow {
	var out []QuotaWindow
	for _, w := range []*codexWindowJSON{rl.PrimaryWindow, rl.SecondaryWindow} {
		if w == nil || w.LimitWindowSeconds <= 0 {
			continue
		}
		out = append(out, *mustWindow(&codexRateWindow{
			UsedPercent:   w.UsedPercent,
			WindowMinutes: w.LimitWindowSeconds / 60,
			ResetsAt:      w.ResetAt,
		}))
	}
	return dedupeWindows(out)
}

func mustWindow(w *codexRateWindow) *QuotaWindow {
	q, _ := codexQuotaWindow(w) // WindowMinutes > 0 is guaranteed by the caller
	return &q
}

// ── credits ──────────────────────────────────────────────────────────────────

type codexDailyJSON struct {
	Data []struct {
		Date   string `json:"date"`
		Totals struct {
			Credits float64 `json:"credits"`
			Turns   float64 `json:"turns"`
		} `json:"totals"`
	} `json:"data"`
}

// codexCredits sums the account's spend across the days the CURRENT window covers, and the same
// again for the window before it.
//
// It no longer derives a budget from the percentage. See the Credits type comment: two measured
// windows put credits-per-percentage-point at 630.2 and 166.9, so the proportionality that
// derivation rests on does not hold, and the answer it produced was off by 3.78×.
//
// The sum is by calendar day because that is the only grain the endpoint offers, so a window that
// opened mid-day includes some spend from before it opened. That is reported as an over-count
// (WholeDays) rather than silently corrected: guessing an intra-day split would replace a known
// bias with an unknown one.
func codexCredits(ctx context.Context, token, accountID string, window *codexWindowJSON, prior *Credits) (*Credits, error) {
	if window == nil || window.LimitWindowSeconds <= 0 || window.ResetAt <= 0 {
		return nil, errors.New("codex: 无周窗口，无法归集 credits")
	}
	span := int64(window.LimitWindowSeconds)
	start := time.Unix(window.ResetAt-span, 0)
	// One window further back, so a brand-new window can still say something about the budget:
	// whatever the previous window spent, the budget is at least that.
	priorStart := time.Unix(window.ResetAt-2*span, 0)
	today := time.Now()
	query := url.Values{
		"start_date":     {priorStart.Format(time.DateOnly)},
		"end_date":       {today.AddDate(0, 0, 1).Format(time.DateOnly)},
		"group_by":       {"day"},
		"workspace_user": {"true"},
	}
	var daily codexDailyJSON
	if err := codexGet(ctx, token, accountID, codexDailyPath+"?"+query.Encode(), &daily); err != nil {
		return nil, err
	}

	startDay, endDay := start.Format(time.DateOnly), today.Format(time.DateOnly)
	priorStartDay := priorStart.Format(time.DateOnly)
	credits := &Credits{Source: CreditsSourceAPI, WindowStart: start.UTC().Format(time.RFC3339)}
	var priorUsed float64
	for _, row := range daily.Data {
		switch {
		case row.Date >= startDay && row.Date <= endDay:
			credits.Used += row.Totals.Credits
			credits.Days++
		case row.Date >= priorStartDay && row.Date < startDay:
			priorUsed += row.Totals.Credits
		}
	}
	credits.PriorWindow, credits.PriorWindowStart = priorUsed, priorStart.UTC().Format(time.RFC3339)
	if credits.PriorWindow == 0 && prior != nil {
		// The ledger returned nothing for the previous window — it has aged out of the queried
		// range, or that window genuinely had no spend. Either way a figure we already measured is
		// not made false by a later query not repeating it.
		credits.PriorWindow, credits.PriorWindowStart = prior.PriorWindow, prior.PriorWindowStart
	}
	// The window opened mid-day ⟹ the first day's total includes spend from the previous
	// window. Say so; do not quietly prorate.
	credits.WholeDays = start.Truncate(24 * time.Hour).Equal(start)
	return credits, nil
}

// codexGet performs one authenticated read against the account API and decodes it into out.
func codexGet(ctx context.Context, token, accountID, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexAPIHost+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		// The user can act on this one, so it must not read as a generic failure.
		return errors.New("codex: 登录已过期，在本机运行一次 codex 即可续期")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("codex: 账号接口返回 HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
