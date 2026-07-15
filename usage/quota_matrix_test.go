package usage

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// Relative timestamps: a hard-coded date silently ages past maxSnapshotAge and the test
	// starts failing on the calendar rather than on the code.
	at := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	sparkAt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	account := fmt.Sprintf(
		`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","limit_name":null,"plan_type":%q,"primary":{"used_percent":78,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":12,"window_minutes":10080,"resets_at":%d}}}}`,
		at, plan, resetAt.Unix(), resetAt.Add(72*time.Hour).Unix())
	spark := fmt.Sprintf(
		`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex_bengalfox","limit_name":"GPT-5.3-Codex-Spark","plan_type":%q,"primary":{"used_percent":8,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":3,"window_minutes":10080,"resets_at":%d}}}}`,
		sparkAt, plan, resetAt.Unix(), resetAt.Add(72*time.Hour).Unix())
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

	q := claudeProvider{}.Query()

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

	q := claudeProvider{}.Query()

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
	if got := (claudeProvider{}).Query().Health; got.OK || got.Reason != HealthNotInstalled {
		t.Fatalf("health = %+v, want ok=false reason=not_installed", got)
	}
}

// ── UB-03 · stale snapshot ────────────────────────────────────────────────────

// A rolled window is flagged PER WINDOW. The 5h window resetting says nothing about the 7d
// window, and condemning the whole reading would throw away a number that is still true.
func TestQuota_WindowExpiry_IsPerWindow(t *testing.T) {
	env := newQuotaEnv(t).withCredentials(t).withCLI(t, "claude")
	// 5h reset an hour ago (rolled → its used% is certainly wrong now); 7d resets in 2 days.
	env.withRateLimits(t, "subscription", time.Now().Add(-6*time.Hour), time.Now().Add(-1*time.Hour), 90)

	q := claudeProvider{}.Query()

	if !q.Present {
		t.Fatal("UB-03: stale data must not hide the provider")
	}
	if len(q.Windows) != 2 {
		t.Fatalf("UB-03: both windows are still reported, got %d", len(q.Windows))
	}
	if !q.Windows[0].Expired {
		t.Fatal("UB-03: the 5h window rolled — it must be flagged expired")
	}
	if q.Windows[1].Expired {
		t.Fatal("UB-03: the 7d window has NOT rolled — flagging it would discard a true number")
	}
	// Not every window rolled, so the reading as a whole is not condemned.
	if q.Snapshot == nil || q.Snapshot.Stale {
		t.Fatalf("UB-03: one rolled window must not condemn the whole snapshot, got %+v", q.Snapshot)
	}
}

// When EVERY window has rolled, the reading as a whole is worthless — say so.
func TestQuota_SnapshotStale_AllWindowsRolled(t *testing.T) {
	env := newQuotaEnv(t).withCredentials(t).withCLI(t, "codex")
	env.withCodexAuth(t, false)
	// The fixture puts the 7d reset 72h after the 5h one, so back-date far enough that BOTH
	// windows have already rolled.
	env.withCodexRollout(t, "pro", time.Now().Add(-100*time.Hour))

	q := codexProvider{}.Query()

	for i, w := range q.Windows {
		if !w.Expired {
			t.Fatalf("UB-03: window %d (%s) rolled and must be flagged expired", i, w.Kind)
		}
	}
	if q.Snapshot == nil || !q.Snapshot.Stale || q.Snapshot.StaleReason != "window_rolled" {
		t.Fatalf("UB-03: every window rolled → snapshot stale/window_rolled, got %+v", q.Snapshot)
	}
}

func TestQuota_SnapshotStale_TooOld(t *testing.T) {
	env := newQuotaEnv(t).withCredentials(t).withCLI(t, "claude")
	// No reset time to check against; age alone must trip staleness past maxSnapshotAge.
	env.withRateLimits(t, "subscription", time.Now().Add(-30*time.Hour), time.Time{}, 20)

	q := claudeProvider{}.Query()

	if q.Snapshot == nil || !q.Snapshot.Stale || q.Snapshot.StaleReason != "too_old" {
		t.Fatalf("UB-03: want stale/too_old, got %+v", q.Snapshot)
	}
}

// ── UB-04 · logged in, no reading yet ─────────────────────────────────────────

func TestQuota_LoggedInNoSnapshot(t *testing.T) {
	newQuotaEnv(t).withCredentials(t).withCLI(t, "claude") // credentials, but the hook never fired

	q := claudeProvider{}.Query()

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

	q := claudeProvider{}.Query()

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

	if q := (codexProvider{}).Query(); q.Present {
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

	q := claudeProvider{}.Query()

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

	q := claudeProvider{}.Query()

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

	q := codexProvider{}.Query()

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

	q := codexProvider{}.Query()

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

	q := codexProvider{}.Query()

	if !q.Present {
		t.Fatal("UB-02 (codex): broken executable must not delete the account")
	}
	if q.Health.Reason != HealthNotExecutable {
		t.Fatalf("health = %+v, want not_executable", q.Health)
	}
}

// ── gemini: not_implemented never masquerades as a supported provider ──────────

func TestQuota_GeminiNeverPresent(t *testing.T) {
	if q := (geminiProvider{}).Query(); q.Present {
		t.Fatal("gemini is unsupported and must not surface as a provider")
	}
}

// ── provenance + the merge rule ───────────────────────────────────────────────

// The probe exists because re-reading the disk cannot always help: codex records only the
// limit family of the model it is RUNNING, so a session on a per-model plan leaves the account
// limit frozen. The freshest reading must therefore win — and, critically, the very next
// background poll must NOT quietly revert a probe result by re-reading an older transcript.
func TestQuota_ProbeBeatsOlderRollout_AndSurvivesReload(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	env.withCodexRollout(t, "pro", time.Now().Add(2*time.Hour)) // rollout reading: 2026-07-12T10:00Z

	// A probe taken NOW — hours after the rollout's newest account entry.
	probe := fmt.Sprintf(
		`{"captured_at":%d,"source":"probe","plan_type":"pro","primary":{"used_percent":9,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":16,"window_minutes":10080,"resets_at":%d}}`,
		time.Now().Unix(), time.Now().Add(3*time.Hour).Unix(), time.Now().Add(150*time.Hour).Unix())
	write(t, filepath.Join(env.deepwork, "codex-rate-limits.json"), probe)

	q := (codexProvider{}).Query()

	if q.Snapshot == nil || q.Snapshot.Source != SourceProbe {
		t.Fatalf("the fresher probe must win, got source=%v", q.Snapshot)
	}
	if q.Windows[0].RemainingPercent != 91 {
		t.Fatalf("5h remaining = %v, want 91 (the probe's number, not the rollout's 22)",
			q.Windows[0].RemainingPercent)
	}
	// Query() is what every background poll calls. Calling it again must not revert.
	if again := (codexProvider{}).Query(); again.Snapshot.Source != SourceProbe {
		t.Fatal("a poll re-reading the transcript must NOT revert the probe result")
	}
}

// An older probe must not hold back a transcript that has since moved on.
func TestQuota_FresherRolloutBeatsStaleProbe(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	env.withCodexRollout(t, "pro", time.Now().Add(2*time.Hour)) // rollout: 2026-07-12T10:00Z

	stale := fmt.Sprintf(
		`{"captured_at":%d,"source":"probe","plan_type":"pro","primary":{"used_percent":1,"window_minutes":300,"resets_at":%d}}`,
		time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC).Unix(), time.Now().Add(3*time.Hour).Unix())
	write(t, filepath.Join(env.deepwork, "codex-rate-limits.json"), stale)

	q := (codexProvider{}).Query()

	if q.Snapshot == nil || q.Snapshot.Source != SourceRollout {
		t.Fatalf("the fresher rollout must win over a 2h-older probe, got %v", q.Snapshot)
	}
}

// UB-16: family is part of quota identity. This is the reported production shape: an older
// premium probe (one 7d window, 96% left) coexists with a newer codex rollout (5h+7d, 83% on
// the weekly window). The newer family becomes the legacy projection, but must not erase the
// independently useful premium observation.
func TestQuota_DifferentFamiliesCoexist_CompatibilityUsesGlobalNewest(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	env.withCodexRollout(t, "pro", time.Now().Add(2*time.Hour))

	probe := fmt.Sprintf(
		`{"captured_at":%d,"source":"probe","plan_type":"pro","family":"premium","primary":{"used_percent":4,"window_minutes":10080,"resets_at":%d}}`,
		time.Now().Add(-13*time.Hour).Unix(), time.Now().Add(150*time.Hour).Unix())
	write(t, filepath.Join(env.deepwork, "codex-rate-limits.json"), probe)

	q := (codexProvider{}).Query()
	if q.Family != "codex" || q.Snapshot == nil || q.Snapshot.Source != SourceRollout {
		t.Fatalf("legacy projection must be globally newest codex rollout, got family=%q snapshot=%+v", q.Family, q.Snapshot)
	}
	if len(q.QuotaGroups) != 2 {
		t.Fatalf("want codex + premium groups, got %+v", q.QuotaGroups)
	}
	byFamily := make(map[string]QuotaGroup)
	for _, group := range q.QuotaGroups {
		byFamily[group.Family] = group
	}
	if byFamily["codex"].Snapshot == nil || byFamily["codex"].Snapshot.Stale {
		t.Fatalf("fresh codex group = %+v", byFamily["codex"])
	}
	if byFamily["premium"].Snapshot == nil || !byFamily["premium"].Snapshot.Stale {
		t.Fatalf("13h-old premium group must remain visible but stale, got %+v", byFamily["premium"])
	}
	if got := byFamily["premium"].Windows[0].RemainingPercent; got != 96 {
		t.Fatalf("premium remaining = %v, want 96", got)
	}
}

func TestQuota_SameFamilyKeepsOnlyItsNewestReading(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	env.withCodexRollout(t, "pro", time.Now().Add(2*time.Hour))
	probe := fmt.Sprintf(
		`{"captured_at":%d,"source":"probe","plan_type":"pro","family":"codex","primary":{"used_percent":9,"window_minutes":300,"resets_at":%d}}`,
		time.Now().Unix(), time.Now().Add(3*time.Hour).Unix())
	write(t, filepath.Join(env.deepwork, "codex-rate-limits.json"), probe)

	q := (codexProvider{}).Query()
	if len(q.QuotaGroups) != 1 {
		t.Fatalf("same family must collapse to one group, got %+v", q.QuotaGroups)
	}
	if q.QuotaGroups[0].Snapshot == nil || q.QuotaGroups[0].Snapshot.Source != SourceProbe {
		t.Fatalf("newer probe must win within codex family, got %+v", q.QuotaGroups[0])
	}
}

func TestCodexRollout_KeepsNewestObservationPerAccountFamily(t *testing.T) {
	env := newQuotaEnv(t)
	dir := filepath.Join(env.codexHome, "sessions", "2026", "07", "14")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := func(at time.Time, family string, used float64) string {
		return fmt.Sprintf(
			`{"timestamp":%q,"payload":{"rate_limits":{"limit_id":%q,"limit_name":null,"plan_type":"pro","primary":{"used_percent":%v,"window_minutes":10080,"resets_at":%d}}}}`,
			at.UTC().Format(time.RFC3339), family, used, time.Now().Add(150*time.Hour).Unix())
	}
	now := time.Now()
	write(t, filepath.Join(dir, "rollout-2026-07-14T00-00-00-families.jsonl"), strings.Join([]string{
		line(now.Add(-30*time.Minute), "premium", 4),
		line(now.Add(-20*time.Minute), "codex", 17),
		line(now.Add(-10*time.Minute), "codex", 18),
	}, "\n"))

	readings := codexRolloutReadings()
	if len(readings) != 2 {
		t.Fatalf("want two account families, got %+v", readings)
	}
	if readings[0].Family != "codex" || readings[0].Windows[0].UsedPercent != 18 {
		t.Fatalf("newest codex observation must win within family, got %+v", readings[0])
	}
	if readings[1].Family != "premium" || readings[1].Windows[0].UsedPercent != 4 {
		t.Fatalf("premium must survive newer codex events, got %+v", readings[1])
	}
}

// Probing is a domain fact, not a caller's guess: claude cannot be asked (its usage arrives
// only when claude itself renders), codex can. ProbeAll says so per runtime.
func TestProbeAll_ReportsWhatEachRuntimeCanDo(t *testing.T) {
	newQuotaEnv(t) // no codex auth → codex has no token to ask with either

	byRuntime := map[string]string{}
	for _, r := range ProbeAll(context.Background()) {
		byRuntime[r.Runtime] = r.Status
	}
	if byRuntime["claude"] != ProbeNotSupported {
		t.Fatalf("claude exposes no endpoint to ask — want not_supported, got %q", byRuntime["claude"])
	}
	if byRuntime["gemini"] != ProbeNotSupported {
		t.Fatalf("gemini is unsupported — want not_supported, got %q", byRuntime["gemini"])
	}
	if byRuntime["codex"] != ProbeNotSupported {
		t.Fatalf("no OAuth token ⟹ nothing to ask with — want not_supported, got %q", byRuntime["codex"])
	}
}

// ── 2026-07-13: the probe made things WORSE than not probing ──────────────────
//
// The account's active limit family moved from "codex" (5h + 7d) to "premium" (a single
// 7-day window). The API then reports its UNUSED secondary slot as used=0 / window=0 /
// reset="" — and taking that at face value invented a phantom window, which resolved to the
// same kind as the real one and made a bar disappear from the UI.

// A slot with no window LENGTH is not a window: the 5h/7d label is derived from that length,
// so without it there is nothing to call the thing.
func TestCodexWindow_SlotWithoutLengthIsNotAWindow(t *testing.T) {
	if _, ok := codexQuotaWindow(&codexRateWindow{UsedPercent: 0, WindowMinutes: 0}); ok {
		t.Fatal("used=0 with no window length is an empty slot, not a window at 100% for 7 days")
	}
	if _, ok := codexQuotaWindow(&codexRateWindow{UsedPercent: 4, WindowMinutes: 10080}); !ok {
		t.Fatal("a slot WITH a length is a real window")
	}
}

// Two windows of one kind is a contradiction. It must die at the boundary — pushed into the
// UI, a keyed list renders only one of them and the other silently vanishes.
func TestQuota_DuplicateWindowKindIsDropped(t *testing.T) {
	info := QuotaInfo{}
	info.applyReading(&Reading{
		CapturedAt: time.Now(),
		Source:     SourceProbe,
		Windows: []QuotaWindow{
			{Kind: "7d", WindowMinutes: 10080, RemainingPercent: 96, ResetAt: time.Now().Add(160 * time.Hour).Format(time.RFC3339)},
			{Kind: "7d", WindowMinutes: 10080, RemainingPercent: 100}, // the phantom
		},
	})
	if len(info.Windows) != 1 {
		t.Fatalf("want one 7d window, got %d — a duplicate kind reached the UI", len(info.Windows))
	}
	if info.Windows[0].RemainingPercent != 96 {
		t.Fatalf("the REAL window must survive, got %v%%", info.Windows[0].RemainingPercent)
	}
}

// The reading says which limit family it speaks for. Without it, a family switch (5h+7d →
// one 7-day window) looks exactly like the app losing a bar.
func TestCodexProbe_RecordsActiveFamilyAndDropsEmptySlot(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-active-limit", "premium")
	h.Set("x-codex-plan-type", "pro")
	h.Set("x-codex-primary-used-percent", "4")
	h.Set("x-codex-primary-window-minutes", "10080")
	h.Set("x-codex-primary-reset-at", strconv.FormatInt(time.Now().Add(160*time.Hour).Unix(), 10))
	h.Set("x-codex-secondary-used-percent", "0") // the empty slot the account really sends
	h.Set("x-codex-secondary-window-minutes", "0")
	h.Set("x-codex-secondary-reset-at", "")

	snap, ok := codexSnapshotFromHeaders(h)
	if !ok {
		t.Fatal("a real primary window is a usable reading")
	}
	if snap.Family != "premium" {
		t.Fatalf("family = %q, want premium (x-codex-active-limit)", snap.Family)
	}
	if snap.Secondary != nil {
		t.Fatal("the empty secondary slot must not become a phantom window")
	}
	if snap.Primary == nil || snap.Primary.WindowMinutes != 10080 {
		t.Fatalf("primary = %+v, want the 7-day window the account actually reports", snap.Primary)
	}
}

// An expired window is not unknown. The runtime reports a fresh reading whenever it is used,
// so a window that rolled with nothing behind it has not been touched since it reset: usage
// is zero, and the next boundary is the old one rolled forward. The value is INFERRED, and
// says so.
func TestQuota_ExpiredWindowInfersFullAndRollsForward(t *testing.T) {
	env := newQuotaEnv(t).withCredentials(t).withCLI(t, "claude")
	// A 5h window that reset 90 minutes ago; nothing has reported since.
	reset := time.Now().Add(-90 * time.Minute)
	env.withRateLimits(t, "subscription", time.Now().Add(-6*time.Hour), reset, 91)

	q := (claudeProvider{}).Query()
	w := q.Windows[0]

	if !w.Expired || !w.Inferred {
		t.Fatalf("a rolled window is expired AND its value inferred, got %+v", w)
	}
	if w.RemainingPercent != 100 || w.UsedPercent != 0 {
		t.Fatalf("nothing reported since the reset ⟹ nothing was used, got %v%% used", w.UsedPercent)
	}
	next, err := time.Parse(time.RFC3339, w.ResetAt)
	if err != nil || !next.After(time.Now()) {
		t.Fatalf("reset must be rolled forward to a boundary AHEAD of us, got %q", w.ResetAt)
	}
	// One whole span forward (RFC3339 keeps seconds, so allow the formatting truncation).
	if got := next.Sub(reset); got < 5*time.Hour-2*time.Second || got > 5*time.Hour+2*time.Second {
		t.Fatalf("the 5h window advances by exactly one span, got %v", got)
	}
}
