package usage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The six-state matrix from the usage-billing spec (UB-01..UB-05, UB-09).
//
// The invariant under test is the one the 2026-07-12 regression broke: an ENVIRONMENT
// probe (can we execute the CLI?) must never delete a USER fact (this account exists).
// Every case below therefore asserts on presence separately from health, freshness and
// billing.

// quotaEnv points every path/PATH lookup at a throwaway tree so no test ever reads the
// developer's real credentials.
type quotaEnv struct {
	claudeHome string // CLAUDE_CONFIG_DIR  → .credentials.json, projects/
	deepwork   string // DEEPWORK_HOME      → claude-rate-limits.json
	codexHome  string // DW_CODEX_HOME      → auth.json, sessions/
	bin        string // PATH               → the only dir on PATH
}

func newQuotaEnv(t *testing.T) *quotaEnv {
	t.Helper()
	root := t.TempDir()
	env := &quotaEnv{
		claudeHome: filepath.Join(root, "claude"),
		deepwork:   filepath.Join(root, "deepwork"),
		codexHome:  filepath.Join(root, "codex"),
		bin:        filepath.Join(root, "bin"),
	}
	for _, dir := range []string{env.claudeHome, env.deepwork, env.codexHome, env.bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", env.claudeHome)
	t.Setenv("DEEPWORK_HOME", env.deepwork)
	t.Setenv("DW_CODEX_HOME", env.codexHome)
	t.Setenv("PATH", env.bin) // nothing else is reachable — no real claude/codex leaks in
	return env
}

// withCredentials plants a populated auth file (the "logged in" evidence).
func (e *quotaEnv) withCredentials(t *testing.T) *quotaEnv {
	t.Helper()
	write(t, filepath.Join(e.claudeHome, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"x"}}`)
	return e
}

// withRateLimits plants a claude statusline-hook reading. usedPct<0 writes null windows.
func (e *quotaEnv) withRateLimits(t *testing.T, source string, capturedAt, resetAt time.Time, usedPct float64) *quotaEnv {
	t.Helper()
	windows := "null,\"seven_day\":null"
	if usedPct >= 0 {
		windows = fmt.Sprintf(`{"used_percentage":%v,"resets_at":%d},"seven_day":{"used_percentage":%v,"resets_at":%d}`,
			usedPct, resetAt.Unix(), usedPct/2, resetAt.Add(48*time.Hour).Unix())
	}
	body := fmt.Sprintf(`{"captured_at":%d,"source":%q,"five_hour":%s}`, capturedAt.Unix(), source, windows)
	write(t, filepath.Join(e.deepwork, "claude-rate-limits.json"), body)
	return e
}

// withCLI plants a runnable CLI (mode 0755) that answers --version.
func (e *quotaEnv) withCLI(t *testing.T, name string) *quotaEnv {
	t.Helper()
	path := filepath.Join(e.bin, name)
	write(t, path, "#!/bin/sh\necho \""+name+" 9.9.9\"\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return e
}

// withBrokenCLI plants an INSTALLED but non-executable CLI — the exact shape of the
// 2026-07-12 regression (a self-update rewrote claude.exe without its +x bit).
func (e *quotaEnv) withBrokenCLI(t *testing.T, name string) *quotaEnv {
	t.Helper()
	write(t, filepath.Join(e.bin, name), "not-executable")
	return e
}

// withCodexAuth plants ~/.codex/auth.json in either billing shape.
func (e *quotaEnv) withCodexAuth(t *testing.T, apiKey bool) *quotaEnv {
	t.Helper()
	// Shape only — the probe reads WHICH field is populated, never its value. The literal
	// below is a placeholder, not a key.
	body := `{"OPENAI_API_KEY":null,"tokens":{"account_id":"acct_1"}}`
	if apiKey {
		body = `{"OPENAI_API_KEY":"placeholder-not-a-key","tokens":null}`
	}
	write(t, filepath.Join(e.codexHome, "auth.json"), body)
	return e
}

// withCodexRollout plants a rollout shaped like a real one: the ACCOUNT limit (unnamed) is
// followed by a per-model sub-limit ("GPT-5.3-Codex-Spark"), which in practice heartbeats
// more often and therefore lands LAST. Reading the last object reports the sub-limit's
// usage as if it were the account's — that is the 2026-07-12 "92% left while /status says
// 26%" defect, and this fixture is what keeps it dead.
func (e *quotaEnv) withCodexRollout(t *testing.T, plan string, resetAt time.Time) *quotaEnv {
	t.Helper()
	dir := filepath.Join(e.codexHome, "sessions", "2026", "07", "12")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	account := fmt.Sprintf(
		`{"timestamp":"2026-07-12T10:00:00.000Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","limit_name":null,"plan_type":%q,"primary":{"used_percent":78,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":12,"window_minutes":10080,"resets_at":%d}}}}`,
		plan, resetAt.Unix(), resetAt.Add(72*time.Hour).Unix())
	spark := fmt.Sprintf(
		`{"timestamp":"2026-07-12T10:05:00.000Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex_bengalfox","limit_name":"GPT-5.3-Codex-Spark","plan_type":%q,"primary":{"used_percent":8,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":3,"window_minutes":10080,"resets_at":%d}}}}`,
		plan, resetAt.Unix(), resetAt.Add(72*time.Hour).Unix())
	write(t, filepath.Join(dir, "rollout-2026-07-12T00-00-00-abc.jsonl"), account+"\n"+spark+"\n")
	return e
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ── UB-01 · healthy subscription ─────────────────────────────────────────────

func TestQuota_SubscriptionFresh(t *testing.T) {
	env := newQuotaEnv(t).withCredentials(t).withCLI(t, "claude")
	env.withRateLimits(t, "subscription", time.Now().Add(-2*time.Minute), time.Now().Add(3*time.Hour), 60)

	q := queryClaudeQuota()

	if !q.Present {
		t.Fatal("UB-01: logged-in account must be present")
	}
	if q.Billing != BillingSubscription {
		t.Fatalf("UB-01: billing = %q, want subscription", q.Billing)
	}
	if len(q.Windows) != 2 {
		t.Fatalf("UB-01: want 5h+7d windows, got %d", len(q.Windows))
	}
	if !q.Health.OK {
		t.Fatalf("UB-01: health should be OK, got %+v", q.Health)
	}
	if q.Snapshot == nil || q.Snapshot.Stale {
		t.Fatalf("UB-01: snapshot should be fresh, got %+v", q.Snapshot)
	}
	if q.Windows[0].RemainingPercent != 40 {
		t.Fatalf("UB-01: remaining%% = %v, want 40", q.Windows[0].RemainingPercent)
	}
}

// ── UB-02 · the regression: CLI broken, account intact ────────────────────────

func TestQuota_ExecutableBroken_ProviderSurvives(t *testing.T) {
	env := newQuotaEnv(t).withCredentials(t).withBrokenCLI(t, "claude")
	env.withRateLimits(t, "subscription", time.Now().Add(-1*time.Minute), time.Now().Add(3*time.Hour), 55)

	q := queryClaudeQuota()

	// The whole point: a dead binary degrades the card, it does not delete it.
	if !q.Present {
		t.Fatal("UB-02: broken executable must NOT delete a logged-in account")
	}
	if len(q.Windows) != 2 {
		t.Fatalf("UB-02: last-known windows must survive, got %d", len(q.Windows))
	}
	if q.Billing != BillingSubscription {
		t.Fatalf("UB-02: billing = %q, want subscription", q.Billing)
	}
	if q.Health.OK || q.Health.Reason != HealthNotExecutable {
		t.Fatalf("UB-02: health = %+v, want ok=false reason=not_executable", q.Health)
	}
	if q.Snapshot == nil || q.Snapshot.Stale {
		t.Fatalf("UB-02: snapshot is fresh and must say so, got %+v", q.Snapshot)
	}
}

// not_installed and not_executable are different stories and must not be conflated.
func TestQuota_NotInstalled_DistinctFromNotExecutable(t *testing.T) {
	newQuotaEnv(t).withCredentials(t) // no binary at all
	if got := queryClaudeQuota().Health; got.OK || got.Reason != HealthNotInstalled {
		t.Fatalf("health = %+v, want ok=false reason=not_installed", got)
	}
}

// ── UB-03 · stale snapshot ────────────────────────────────────────────────────

func TestQuota_SnapshotStale_WindowRolled(t *testing.T) {
	env := newQuotaEnv(t).withCredentials(t).withCLI(t, "claude")
	// Reading taken 6h ago describing a window that reset 1h ago → the used% we hold is
	// certainly wrong now.
	env.withRateLimits(t, "subscription", time.Now().Add(-6*time.Hour), time.Now().Add(-1*time.Hour), 90)

	q := queryClaudeQuota()

	if !q.Present {
		t.Fatal("UB-03: stale data must not hide the provider")
	}
	if len(q.Windows) == 0 {
		t.Fatal("UB-03: last-known values are still shown (flagged, not deleted)")
	}
	if q.Snapshot == nil || !q.Snapshot.Stale {
		t.Fatalf("UB-03: snapshot must be flagged stale, got %+v", q.Snapshot)
	}
	if q.Snapshot.StaleReason != "window_rolled" {
		t.Fatalf("UB-03: stale_reason = %q, want window_rolled", q.Snapshot.StaleReason)
	}
}

func TestQuota_SnapshotStale_TooOld(t *testing.T) {
	env := newQuotaEnv(t).withCredentials(t).withCLI(t, "claude")
	// No reset time to check against; age alone must trip staleness past maxSnapshotAge.
	env.withRateLimits(t, "subscription", time.Now().Add(-30*time.Hour), time.Time{}, 20)

	q := queryClaudeQuota()

	if q.Snapshot == nil || !q.Snapshot.Stale || q.Snapshot.StaleReason != "too_old" {
		t.Fatalf("UB-03: want stale/too_old, got %+v", q.Snapshot)
	}
}

// ── UB-04 · logged in, no reading yet ─────────────────────────────────────────

func TestQuota_LoggedInNoSnapshot(t *testing.T) {
	newQuotaEnv(t).withCredentials(t).withCLI(t, "claude") // credentials, but the hook never fired

	q := queryClaudeQuota()

	if !q.Present || !hasEvidence(q.Evidence, EvidenceCredentials) {
		t.Fatalf("UB-04: logged-in account must be present, got %+v", q)
	}
	if q.Snapshot != nil {
		t.Fatalf("UB-04: no reading exists — snapshot must be nil, got %+v", q.Snapshot)
	}
	if len(q.Windows) != 0 {
		t.Fatal("UB-04: must not fabricate 0%/100% windows")
	}
	if q.Billing != BillingUnknown {
		t.Fatalf("UB-04: billing = %q, want unknown (nothing proves which kind of money)", q.Billing)
	}
}

// ── UB-05 · absence is the ONLY reason to hide ────────────────────────────────

func TestQuota_LoggedOutEmpty_Hidden(t *testing.T) {
	newQuotaEnv(t) // no credentials, no snapshot, no sessions — nothing at all

	q := queryClaudeQuota()

	if q.Present || len(q.Evidence) != 0 {
		t.Fatalf("UB-05: nothing on disk ⟹ not present, got %+v", q)
	}
}

// An empty directory skeleton is NOT history. Both CLIs leave dated dirs behind even when
// they never wrote a transcript, and counting those would resurrect a runtime the user
// logged out of and never used.
func TestQuota_EmptySessionSkeleton_IsNotEvidence(t *testing.T) {
	env := newQuotaEnv(t).withCLI(t, "codex")
	if err := os.MkdirAll(filepath.Join(env.codexHome, "sessions", "2026", "07", "12"), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	if q := queryCodexQuota(); q.Present {
		t.Fatalf("UB-05: an empty session skeleton must not count as presence, got %+v", q)
	}
}

// An explicit logout removes credentials but leaves real history behind. We still show the
// provider (its usage happened), but we must NOT claim it is logged in.
func TestQuota_LogoutResidue_ShownButNotClaimedLoggedIn(t *testing.T) {
	env := newQuotaEnv(t).withCLI(t, "claude")
	projects := filepath.Join(env.claudeHome, "projects", "p1")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatalf("mkdir projects: %v", err)
	}
	write(t, filepath.Join(projects, "session.jsonl"), `{"type":"assistant"}`+"\n")

	q := queryClaudeQuota()

	if !q.Present {
		t.Fatal("UB-05: local history is presence evidence")
	}
	if hasEvidence(q.Evidence, EvidenceCredentials) {
		t.Fatal("UB-05: a logged-out account must not report credentials evidence")
	}
	if q.Note != "未检出登录凭据 · 仅存历史记录" {
		t.Fatalf("UB-05: note must not claim login, got %q", q.Note)
	}
}

// ── UB-09 · API billing ───────────────────────────────────────────────────────

func TestQuota_ClaudeAPIBilling(t *testing.T) {
	env := newQuotaEnv(t).withCredentials(t).withCLI(t, "claude")
	env.withRateLimits(t, "api", time.Now().Add(-1*time.Minute), time.Time{}, -1) // api session: no windows

	q := queryClaudeQuota()

	if !q.Present {
		t.Fatal("UB-09: API-billed account must still surface")
	}
	if q.Billing != BillingAPI {
		t.Fatalf("UB-09: billing = %q, want api", q.Billing)
	}
	if len(q.Windows) != 0 {
		t.Fatal("UB-09: API billing has no subscription window — none may be invented")
	}
	if q.Snapshot == nil {
		t.Fatal("UB-09: the api reading itself is a snapshot and carries a capture time")
	}
}

func TestQuota_CodexAPIOnly_FromAuthShape(t *testing.T) {
	newQuotaEnv(t).withCodexAuth(t, true).withCLI(t, "codex") // API key, no rollout

	q := queryCodexQuota()

	if !q.Present {
		t.Fatal("UB-09: API-key codex account must surface")
	}
	if q.Billing != BillingAPI {
		t.Fatalf("UB-09: billing = %q, want api", q.Billing)
	}
	if len(q.Windows) != 0 {
		t.Fatal("UB-09: no subscription window for API billing")
	}
}

func TestQuota_CodexSubscription(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	env.withCodexRollout(t, "plus", time.Now().Add(2*time.Hour))

	q := queryCodexQuota()

	if !q.Present || q.Billing != BillingSubscription {
		t.Fatalf("codex subscription: present=%v billing=%q", q.Present, q.Billing)
	}
	if q.Plan != "plus" {
		t.Fatalf("plan = %q, want plus", q.Plan)
	}
	if len(q.Windows) != 2 {
		t.Fatalf("want 5h+7d windows, got %d", len(q.Windows))
	}
	if q.Snapshot == nil || q.Snapshot.Stale {
		t.Fatalf("fresh rollout → fresh snapshot, got %+v", q.Snapshot)
	}
}

// Codex without a rollout must not vanish either — the same axis split applies.
func TestQuota_CodexBrokenCLI_ProviderSurvives(t *testing.T) {
	newQuotaEnv(t).withCodexAuth(t, false).withBrokenCLI(t, "codex")

	q := queryCodexQuota()

	if !q.Present {
		t.Fatal("UB-02 (codex): broken executable must not delete the account")
	}
	if q.Health.Reason != HealthNotExecutable {
		t.Fatalf("health = %+v, want not_executable", q.Health)
	}
}

// ── gemini: not_implemented never masquerades as a supported provider ──────────

func TestQuota_GeminiNeverPresent(t *testing.T) {
	if q := queryGeminiQuota(); q.Present {
		t.Fatal("gemini is unsupported and must not surface as a provider")
	}
}
