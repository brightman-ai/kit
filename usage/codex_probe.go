// Package usage — codex_probe.go: the ON-DEMAND, authoritative codex quota probe.
//
// Why this exists. The offline reader (codex_quota.go) can only see what codex has already
// written to a rollout, and codex writes the rate-limit family of the model it is CURRENTLY
// running. Work a session on a per-model plan (e.g. GPT-5.3-Codex-Spark) and the ACCOUNT
// limit simply stops being refreshed — so after the account's 5h window rolls over, the
// newest account reading on disk is the pre-reset one, and no amount of re-reading the disk
// will ever produce a fresher number. Pressing 刷新 moved nothing, because there was nothing
// new to move to.
//
// This probe asks the account directly, the same way `codex-switch` does: one request to the
// Codex backend with the stored OAuth token, reading the x-codex-* rate-limit headers off the
// response. Requesting a NON-Spark model is deliberate — the headers then describe the
// ACCOUNT limit, which is the number `codex /status` prints and the number the chip shows.
//
// Cost and consent. This is a real API request, so it is USER-INITIATED ONLY: the 刷新 button
// calls it; the background poll never does. The access token is read from ~/.codex/auth.json,
// used for exactly this one call, and never logged, never persisted, never returned to any
// caller.
package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	codexProbeURL     = "https://chatgpt.com/backend-api/codex/responses"
	codexProbeModel   = "gpt-5.4" // NOT a per-model plan: we want the ACCOUNT limit headers
	codexProbeTimeout = 12 * time.Second
)

// codexAuthTokens is the token half of ~/.codex/auth.json. Only AccessToken is read, and only
// to authorize the probe below.
type codexAuthTokens struct {
	Tokens *struct {
		AccessToken string `json:"access_token"`
	} `json:"tokens"`
}

// probeCodexQuota asks the Codex backend for the account's CURRENT rate limits and persists
// the answer to the drop file, so every later (offline) read sees it too. Reached through
// codexProvider.Probe — see provider.go.
//
// Returns an error when the account is not OAuth-authenticated (an API-key account has no
// subscription limits to report), when the request fails, or when the response carries no
// rate-limit headers. A failure NEVER degrades the offline path — the caller falls back to
// whatever the rollout last recorded.
func probeCodexQuota(ctx context.Context) error {
	token, err := codexAccessToken()
	if err != nil {
		return err
	}

	body, _ := json.Marshal(map[string]any{
		"model":        codexProbeModel,
		"instructions": "ok",
		"input":        []map[string]string{{"role": "user", "content": "ok"}},
		"store":        false,
		"stream":       true,
	})

	ctx, cancel := context.WithTimeout(ctx, codexProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexProbeURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	// The rate-limit headers ride on the response HEAD; we never read the stream body. Close it
	// immediately so the generation stops as early as the protocol allows.
	defer resp.Body.Close()

	snap, ok := codexSnapshotFromHeaders(resp.Header)
	if !ok {
		// A 4xx still carries the headers on a live account, so reaching here means the account
		// really did not report limits (API-key billing, or an auth failure).
		return errors.New("codex probe: response carried no rate-limit headers")
	}
	return writeCodexProbeSnapshot(snap)
}

// codexAccessToken reads the OAuth access token out of ~/.codex/auth.json. The value is
// returned to the single caller above and goes nowhere else.
func codexAccessToken() (string, error) {
	data, err := os.ReadFile(codexAuthPath()) //nolint:gosec — the auth file is the point
	if err != nil {
		return "", err
	}
	var auth codexAuthTokens
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", err
	}
	if auth.Tokens == nil || strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		return "", errors.New("codex probe: no OAuth token (API-key accounts have no subscription quota)")
	}
	return auth.Tokens.AccessToken, nil
}

// codexProbeSnapshot is the drop file this probe writes — the codex counterpart of claude's
// statusline-hook drop file, and read by the same "newest reading wins" rule.
type codexProbeSnapshot struct {
	CapturedAt int64  `json:"captured_at"`
	Source     string `json:"source"` // "probe" — a reading we asked for
	PlanType   string `json:"plan_type,omitempty"`
	// Family is WHICH limit set these windows belong to (x-codex-active-limit). The account's
	// active family can change — ours moved from "codex" (5h+7d) to "premium" (one 7-day
	// window) — and a family with different windows is a different shape, not a different
	// value. Recording it is what lets the UI say so instead of a bar just going missing.
	Family    string           `json:"family,omitempty"`
	Primary   *codexRateWindow `json:"primary,omitempty"`
	Secondary *codexRateWindow `json:"secondary,omitempty"`
}

// codexSnapshotFromHeaders maps the x-codex-* response headers onto a snapshot. ok=false when
// neither slot describes a real window (nothing to record).
func codexSnapshotFromHeaders(h http.Header) (codexProbeSnapshot, bool) {
	snap := codexProbeSnapshot{
		CapturedAt: time.Now().Unix(),
		Source:     "probe",
		PlanType:   strings.TrimSpace(h.Get("x-codex-plan-type")),
		Family:     strings.TrimSpace(h.Get("x-codex-active-limit")),
		Primary:    codexWindowFromHeaders(h, "primary"),
		Secondary:  codexWindowFromHeaders(h, "secondary"),
	}
	if snap.Primary == nil && snap.Secondary == nil {
		return codexProbeSnapshot{}, false
	}
	return snap, true
}

// codexWindowFromHeaders reads one slot's headers.
//
// A window needs a LENGTH to exist. The account currently reports its unused secondary slot as
// used-percent=0 with window-minutes=0 and an empty reset — and taking that at face value
// invented a phantom window, which then collided with the real one and made a bar disappear.
// A slot is a window only when it says how long it is; anything else is nil, never a
// fabricated zero, and never a guessed default.
func codexWindowFromHeaders(h http.Header, slot string) *codexRateWindow {
	minutes, err := strconv.Atoi(strings.TrimSpace(h.Get("x-codex-" + slot + "-window-minutes")))
	if err != nil || minutes <= 0 {
		return nil
	}
	used, err := strconv.ParseFloat(strings.TrimSpace(h.Get("x-codex-"+slot+"-used-percent")), 64)
	if err != nil {
		return nil
	}
	w := &codexRateWindow{UsedPercent: used, WindowMinutes: minutes}
	if r, err := strconv.ParseInt(strings.TrimSpace(h.Get("x-codex-"+slot+"-reset-at")), 10, 64); err == nil && r > 0 {
		w.ResetsAt = r
	}
	return w
}

// codexProbePath returns ~/.deepwork/codex-rate-limits.json (DEEPWORK_HOME overrides), the
// mirror of claude's rate-limit drop file.
func codexProbePath() string {
	return deepworkFile("codex-rate-limits.json")
}

// writeCodexProbeSnapshot persists the probe result so the OFFLINE path sees it too — otherwise
// the next background poll would read the rollout again and quietly revert to the stale number
// the user just refreshed away from.
func writeCodexProbeSnapshot(snap codexProbeSnapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return os.WriteFile(codexProbePath(), data, 0o600)
}

// codexProbeReading turns the last probe result into a Reading. nil when we have never probed
// (or the file is unusable). It competes with the rollout reading on age alone — see
// newestReading: a probe must not be reverted by the next poll re-reading an older transcript,
// and an older probe must not hold back a transcript that has moved on.
func codexProbeReading() *Reading {
	data, err := os.ReadFile(codexProbePath()) //nolint:gosec — read-only quota probe
	if err != nil {
		return nil
	}
	var snap codexProbeSnapshot
	if json.Unmarshal(data, &snap) != nil {
		return nil
	}
	if snap.CapturedAt <= 0 || (snap.Primary == nil && snap.Secondary == nil) {
		return nil
	}
	return &Reading{
		CapturedAt: time.Unix(snap.CapturedAt, 0),
		Source:     SourceProbe,
		Plan:       snap.PlanType,
		Family:     snap.Family,
		Billing:    BillingSubscription,
		Windows:    codexWindows(snap.Primary, snap.Secondary),
	}
}
