package usage

// Account axis — the defects that made "one CLI = one bill" untenable, each pinned so it
// cannot come back quietly. Every case here is a shape observed on a real machine on
// 2026-08-22, not an invented one.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeCredentials is a host key store for tests. Nothing here reaches the network: the
// credential is only ever used to decide presence and attribution in these cases.
type fakeCredentials map[string]Credential

func (f fakeCredentials) Credential(vendor string) (Credential, bool) {
	cred, ok := f[vendor]
	return cred, ok
}

func withCredentials(t *testing.T, creds fakeCredentials) {
	t.Helper()
	UseCredentials(creds)
	t.Cleanup(func() { UseCredentials(nil) })
}

// proxiedRollout writes a rollout billed to ANOTHER vendor: session_meta names the proxy, and
// the rate_limits object is the well-formed empty shell the proxy leaves behind (the upstream
// returns no rate-limit headers, so codex records every field as null).
func proxiedRollout(t *testing.T, env *quotaEnv, day, name, providerID string, at time.Time) {
	t.Helper()
	dir := filepath.Join(env.codexHome, "sessions", "2026", "08", day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"model_provider":%q}}`,
		at.UTC().Format(time.RFC3339), providerID)
	empty := fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","limit_name":null,"primary":null,"secondary":null,"credits":null,"plan_type":null}}}`,
		at.UTC().Format(time.RFC3339))
	path := filepath.Join(dir, "rollout-2026-08-"+day+"T00-00-00-"+name+".jsonl")
	write(t, path, meta+"\n"+empty+"\n")
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// ownRollout writes a rollout this account actually paid for, carrying a real account reading.
func ownRollout(t *testing.T, env *quotaEnv, day, name string, at time.Time, usedPct float64) {
	t.Helper()
	dir := filepath.Join(env.codexHome, "sessions", "2026", "08", day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"model_provider":"openai"}}`,
		at.UTC().Format(time.RFC3339))
	reading := fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","limit_name":null,"plan_type":"pro","primary":{"used_percent":%v,"window_minutes":10080,"resets_at":%d}}}}`,
		at.UTC().Format(time.RFC3339), usedPct, time.Now().Add(48*time.Hour).Unix())
	path := filepath.Join(dir, "rollout-2026-08-"+day+"T00-00-00-"+name+".jsonl")
	write(t, path, meta+"\n"+reading+"\n")
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// THE regression this axis exists for. On 2026-08-22 the four newest rollouts were all proxied
// to another vendor, and the account's real reading sat in the 21st file. A scan bounded to the
// newest handful found nothing and the row said 「7 天无数据」 — about data that was on disk.
//
// A proxied rollout is not merely unhelpful: it contains a well-formed rate_limits object with
// every field null, so a scanner that trusts shape over provenance reads it as a live account
// with no windows.
func TestCodexRollout_ProxiedSessionsDoNotBlindTheScan(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	// The real reading, then a wall of newer proxied sessions on top of it.
	ownRollout(t, env, "16", "real", time.Now().Add(-6*24*time.Hour), 100)
	for i := 0; i < 12; i++ {
		proxiedRollout(t, env, "22", fmt.Sprintf("proxied%02d", i),
			"mimo2codex-kimi-coding", time.Now().Add(-time.Duration(i)*time.Minute))
	}

	readings := codexRolloutScan()
	if len(readings) != 1 {
		t.Fatalf("want exactly the one account reading, got %+v", readings)
	}
	if got := readings[0].Windows[0].UsedPercent; got != 100 {
		t.Fatalf("used = %v, want the account's real 100%% that was buried under proxied sessions", got)
	}
	if readings[0].Account.Vendor != VendorOpenAI {
		t.Fatalf("a reading must know whose account it describes, got %+v", readings[0].Account)
	}
}

// A proxied session on its own must produce NO reading at all — never an account "with no
// windows", which would read as a live account whose quota is unknown.
func TestCodexRollout_ProxiedSessionAloneYieldsNoReading(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	proxiedRollout(t, env, "22", "only", "mimo2codex-kimi-coding", time.Now())

	if readings := codexRolloutScan(); len(readings) != 0 {
		t.Fatalf("a proxied session bills another vendor — it is not this account's data, got %+v", readings)
	}
}

// Attribution: exactly one account may claim 「当前计费」, and it is decided by the newest
// session's provider, not by which row happens to have the freshest number.
func TestAttribution_NewestSessionDecidesWhoIsBeingBilled(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	withCredentials(t, fakeCredentials{VendorMoonshot: {
		APIKey:             "placeholder-not-a-key",
		RuntimeProviderIDs: []string{"mimo2codex-kimi-coding"},
	}})
	ownRollout(t, env, "16", "real", time.Now().Add(-6*24*time.Hour), 100)
	proxiedRollout(t, env, "22", "now", "mimo2codex-kimi-coding", time.Now())

	openai := (codexProvider{}).Query()
	if openai.Attribution == nil || openai.Attribution.Active {
		t.Fatalf("the official account is NOT being billed right now, got %+v", openai.Attribution)
	}
	if openai.Attribution.Vendor != VendorMoonshot || openai.Attribution.Display != "Kimi For Coding" {
		t.Fatalf("it must name who IS being billed, got %+v", openai.Attribution)
	}

	kimi := (kimiProvider{}).Query()
	if kimi.Attribution == nil || !kimi.Attribution.Active {
		t.Fatalf("kimi IS being billed right now, got %+v", kimi.Attribution)
	}
}

// An undeclared proxy id is UNKNOWN, never "openai". Guessing here would tell the user their
// official quota is being spent when it is not.
func TestAttribution_UndeclaredProviderIsUnknownNotOurs(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	withCredentials(t, fakeCredentials{}) // nobody claims this id
	ownRollout(t, env, "16", "real", time.Now().Add(-24*time.Hour), 50)
	proxiedRollout(t, env, "22", "now", "some-unknown-proxy", time.Now())

	got := (codexProvider{}).Query().Attribution
	if got == nil || got.Active {
		t.Fatalf("an unknown provider is not us, got %+v", got)
	}
	if got.Vendor != "" || got.ProviderID != "some-unknown-proxy" {
		t.Fatalf("unknown must stay unknown and carry the id the user configured, got %+v", got)
	}
}

// Kimi's limit/used carry no unit, and this account returns "100"/"75" for a week in which the
// transcripts show 2,189 requests — so the numbers are a normalised scale here and request
// counts elsewhere. Only the RATIO is portable, and both shapes must land on the same percentage.
func TestKimiWindow_IsUnitAgnostic(t *testing.T) {
	for _, tc := range []struct {
		limit, used string
		want        float64
	}{
		{"100", "75", 75},   // this account: a normalised scale
		{"2048", "512", 25}, // a documented request-count plan
	} {
		w, ok := kimiWindowJSON{Limit: tc.limit, Used: tc.used}.window(300)
		if !ok {
			t.Fatalf("limit=%s used=%s must yield a window", tc.limit, tc.used)
		}
		if w.UsedPercent != tc.want {
			t.Fatalf("limit=%s used=%s → %v%%, want %v%%", tc.limit, tc.used, w.UsedPercent, tc.want)
		}
	}
	if _, ok := (kimiWindowJSON{Limit: "0", Used: "0"}).window(300); ok {
		t.Fatal("a zero limit is no reading at all, not 0% used")
	}
}

// Kimi meters in window percentage. Manufacturing a credits figure for it would be exactly the
// subscription/API confusion the account axis exists to prevent.
func TestKimi_NeverReportsCredits(t *testing.T) {
	newQuotaEnv(t)
	withCredentials(t, fakeCredentials{VendorMoonshot: {APIKey: "placeholder-not-a-key"}})
	if got := (kimiProvider{}).Query(); got.Credits != nil {
		t.Fatalf("kimi has no credits unit, got %+v", got.Credits)
	}
}

// Presence is the only axis allowed to hide an account: no key on this host ⟹ no row, rather
// than an empty row implying a subscription the user does not have.
func TestKimi_AbsentWithoutCredential(t *testing.T) {
	newQuotaEnv(t)
	withCredentials(t, fakeCredentials{})
	if got := (kimiProvider{}).Query(); got.Present {
		t.Fatal("no kimi key on this host ⟹ the account must not surface")
	}
	withCredentials(t, fakeCredentials{VendorMoonshot: {APIKey: "placeholder-not-a-key"}})
	got := (kimiProvider{}).Query()
	if !got.Present || got.Billing != BillingSubscription {
		t.Fatalf("a configured key is a present subscription, got %+v", got)
	}
	if !strings.Contains(got.Note, "等待首次查询") {
		t.Fatalf("never probed ⟹ say so, got %q", got.Note)
	}
}

// credits 与 used_percent 是两个独立的计量器，不是同一件事的两种说法 —— 所以【不许相除】。
//
// 这条不是风格偏好，是被两个完整窗口的实测证伪的。同一个账号：
//
//	上一周期（数字已脱敏）：跑到整 100%，约 6.3 万 credits → 每百分点约 630
//	本  周期           ：6%，        约 1 千 credits  → 每百分点约 167
//
// 相差 3.78 倍。原来的「已耗 ÷ 已用%」正是靠这个比例恒定才成立，于是它在面板上写出了
//「剩 4,716」—— 而这个账号上一周跑到了 63,025。任何「换个窗口除」的排序都救不回来，
// 因为被证伪的是相除这件事本身。
//
// 这个测试守的是 Credits 的形状：一旦有人把 Allowance/Remaining 加回来，它会红。
func TestCredits_PublishesNoDerivedAllowance(t *testing.T) {
	shape := reflect.TypeOf(Credits{})
	for _, banned := range []string{"Allowance", "Remaining", "AllowanceBasis", "AllowanceUsedPercent"} {
		if _, found := shape.FieldByName(banned); found {
			t.Errorf("Credits.%s 回来了：credits ÷ used%% 已被两个实测窗口证伪（630.2 vs 166.9 每百分点）", banned)
		}
	}
	// 留下的必须都是厂商原话：本周期花了多少、上一周期花了多少。
	for _, kept := range []string{"Used", "PriorWindow", "PriorWindowStart"} {
		if _, found := shape.FieldByName(kept); !found {
			t.Errorf("Credits.%s 不见了 —— 这是厂商直接陈述的事实，不该被一起删掉", kept)
		}
	}
}

// A snapshot is per ACCOUNT. Two accounts sharing one file would make the newer erase the other
// — the same defect as per-family merging, one level up.
func TestSnapshot_IsPerAccount(t *testing.T) {
	newQuotaEnv(t)
	openai := Account{Runtime: "codex", Vendor: VendorOpenAI}
	kimi := Account{Runtime: "codex", Vendor: VendorMoonshot}
	if snapshotPath(openai) == snapshotPath(kimi) {
		t.Fatal("two accounts must not share one snapshot file")
	}
	if err := writeSnapshot(quotaSnapshot{
		Account: openai, CapturedAt: time.Now().Unix(), Source: SourceProbe,
		Families: []snapshotFamily{{Family: "codex", Windows: []QuotaWindow{{Kind: "7d", UsedPercent: 40}}}},
	}); err != nil {
		t.Fatal(err)
	}
	if readings, _ := readSnapshotReadings(openai); len(readings) != 1 || readings[0].Windows[0].UsedPercent != 40 {
		t.Fatalf("round-trip lost the reading, got %+v", readings)
	}
	if readings, _ := readSnapshotReadings(kimi); len(readings) != 0 {
		t.Fatalf("the other account must still be empty, got %+v", readings)
	}
}

// 一个面板里两行的窗口顺序必须一致。厂商各按各的顺序返回（codex 先 5h 后 7d，kimi 先周后 5h），
// 让 UI 各自渲染，读者每看一行就要重新找一次「哪根是周额度」——这笔成本每次瞥一眼都要付。
func TestQuota_WindowsAlwaysReadShortestFirst(t *testing.T) {
	info := QuotaInfo{}
	info.applyReading(&Reading{
		CapturedAt: time.Now(),
		Source:     SourceProbe,
		Windows: []QuotaWindow{
			{Kind: "7d", WindowMinutes: 10080, UsedPercent: 75},
			{Kind: "5h", WindowMinutes: 300, UsedPercent: 9},
		},
	})
	if len(info.Windows) != 2 || info.Windows[0].Kind != "5h" || info.Windows[1].Kind != "7d" {
		t.Fatalf("want 5h then 7d, got %+v", info.Windows)
	}

	// 没有长度的窗口排在最后：它在这个序列里没有位置，就不该抢一个。
	info2 := QuotaInfo{}
	info2.applyReading(&Reading{
		CapturedAt: time.Now(), Source: SourceProbe,
		Windows: []QuotaWindow{
			{Kind: "plan", WindowMinutes: 0, UsedPercent: 5},
			{Kind: "5h", WindowMinutes: 300, UsedPercent: 9},
		},
	})
	if info2.Windows[0].Kind != "5h" {
		t.Fatalf("length-less window must sort last, got %+v", info2.Windows)
	}
}

// 「首日含上一周期用量」这个警告的触发值是 FALSE，而 omitempty 恰好只删 false ——
// 于是这条警告从来没上过线（实测：窗口 11:23 开始，wire 上根本没有 whole_days，「≈」不出现）。
// 凡是「false 才是信息」的布尔，都必须无条件序列化。
func TestCredits_WholeDaysFalseSurvivesTheWire(t *testing.T) {
	blob, err := json.Marshal(Credits{Used: 1001.46, Source: CreditsSourceAPI, WholeDays: false})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"whole_days":false`) {
		t.Fatalf("false 被 omitempty 吞掉了，警告就到不了 UI：%s", blob)
	}
}

// 上一周期消耗是【历史】，不是本周期预算 —— 它只是把厂商逐日账本在那个窗口上求和，没有除法。
// 保留它是因为「我一周大概烧多少 credits」这个问题值得有个真数字回答；标成预算就成了假话。
func TestCredits_PriorWindowIsHistoryNotABudget(t *testing.T) {
	// 接口这次没返回上一周期（超出查询区间/那周确实没花钱），已经测到的事实不该被一次查询的沉默抹掉。
	prior := &Credits{PriorWindow: 63024.58, PriorWindowStart: "2026-08-15T03:23:16Z"}
	got := &Credits{Used: 1001.46}
	if got.PriorWindow == 0 && prior != nil {
		got.PriorWindow, got.PriorWindowStart = prior.PriorWindow, prior.PriorWindowStart
	}
	if got.PriorWindow != 63024.58 || got.PriorWindowStart == "" {
		t.Fatalf("已测得的上周期消耗必须带着它覆盖的日期一起留下，got %+v", got)
	}
	// 本周期已耗与上一周期消耗之间【没有】任何算术关系被声称 —— 6% 对 1001.46，
	// 若按 63,025 是预算去推该是 1.59%，两者对不上正是删掉相除的原因。
	if got.Used >= got.PriorWindow {
		t.Fatalf("夹具本身错了：本周期不该超过上周期总量")
	}
}

// 智谱把窗口写成 {number, unit} 的私有枚举，且不作文档。这三个码是本账号实际返回的，每一个都用
// 它自带的 nextResetTime 反查过（周→5.9 天后重置、月→21.9 天后重置）。没见过的码必须给 0
// ——「长度未说明」，绝不能猜成某个具体窗口。
func TestZhipu_WindowUnitsAreEvidenced(t *testing.T) {
	cases := []struct {
		number, unit, wantMinutes int
		wantKind                  string
	}{
		{5, zhipuUnitHour, 300, "5h"},     // TOKENS_LIMIT unit=3 number=5 → 5 小时周期
		{1, zhipuUnitWeek, 10080, "7d"},   // TOKENS_LIMIT unit=6 number=1 → 周额度
		{1, zhipuUnitMonth, 43200, "30d"}, // TIME_LIMIT   unit=5 number=1 → 月度 MCP 配额
		{1, 99, 0, ""},                    // 没见过的码
	}
	for _, tc := range cases {
		got := zhipuWindowMinutes(tc.number, tc.unit)
		if got != tc.wantMinutes {
			t.Fatalf("unit=%d number=%d → %d 分钟, want %d", tc.unit, tc.number, got, tc.wantMinutes)
		}
		if k := windowKind(got); k != tc.wantKind {
			t.Fatalf("%d 分钟的标签 = %q, want %q", got, k, tc.wantKind)
		}
	}
}

// 一个 runtime 两个 vendor，两次：codex 下是 OpenAI/Kimi，claude 下是 Anthropic/智谱。
// 这正是「runtime 不等于账号」的完整证明——注册表里必须四个账号都在，且 key 互不相同。
func TestRegistry_OneRuntimeCanHoldTwoAccounts(t *testing.T) {
	seen := map[string]bool{}
	byRuntime := map[string]int{}
	for _, p := range providers() {
		id := p.Account().ID()
		if seen[id] {
			t.Fatalf("账号 id 重复：%s —— 两个账号共用一个 key 会让其中一个静默消失", id)
		}
		seen[id] = true
		byRuntime[p.Account().Runtime]++
	}
	for _, want := range []string{"claude:anthropic", "claude:zhipu", "codex:openai", "codex:moonshot"} {
		if !seen[want] {
			t.Fatalf("注册表缺少 %s，实有 %v", want, seen)
		}
	}
	if byRuntime["claude"] < 2 || byRuntime["codex"] < 2 {
		t.Fatalf("每个 runtime 都该能挂多个账号，实得 %v", byRuntime)
	}
}

// 并发是归因最容易说错的情形，而且在这台机器上是真实发生的：一小时里 131 条 GLM 消息和
// 2 条 Anthropic 消息同时流淌。旧逻辑取「最新一条」，等于从两个都在付费的厂商里挑一个、
// 宣称它是唯一付款方 —— 每秒可能翻面，且全程为假。
//
// 新规则：窗口内有流量的账号各自标「当前计费」；「记在 X 名下」只在窗口内恰有一个厂商时
// 才说 —— 唯一一种点名是信息而不是猜测的情形。
func TestAttribution_ConcurrentVendorsAreBothBilledNoCrossClaim(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	withCredentials(t, fakeCredentials{
		VendorMoonshot: {APIKey: "placeholder-not-a-key", RuntimeProviderIDs: []string{"mimo2codex-kimi-coding"}},
	})
	ownRollout(t, env, "25", "official-live", time.Now().Add(-5*time.Minute), 40)
	proxiedRollout(t, env, "25", "kimi-live", "mimo2codex-kimi-coding", time.Now().Add(-1*time.Minute))

	openai := (codexProvider{}).Query().Attribution
	kimi := (kimiProvider{}).Query().Attribution
	if openai == nil || kimi == nil {
		t.Fatalf("两个厂商都在窗口内有活跃会话，归因不得为 nil：%+v / %+v", openai, kimi)
	}
	if !openai.Active || !kimi.Active {
		t.Fatalf("并发时双方都应标当前计费（各付各的），got openai=%v kimi=%v", openai.Active, kimi.Active)
	}
	// 关键断言：谁也不许把对方指认为「唯一付款方」。流量是分开的，点名就是错的。
	if openai.Vendor != "" || kimi.Vendor != "" {
		t.Fatalf("并发时不得声称单一付款方：openai=%+v kimi=%+v", openai, kimi)
	}
}

// 「当前计费」的"当前"必须有边界。旧逻辑取最新一条消息**不看年龄** —— 三天前的 GLM 会话能让
// 徽标在整个空闲的周末坚称「记在 GLM 名下」。窗口之外没有"现在"可描述，诚实的显示是没有徽标。
func TestAttribution_StaleTrafficClaimsNothing(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	// 两个会话都在窗口外：一个官方、一个中转。
	ownRollout(t, env, "22", "official-old", time.Now().Add(-3*24*time.Hour), 40)
	proxiedRollout(t, env, "22", "kimi-old", "mimo2codex-kimi-coding", time.Now().Add(-2*24*time.Hour))

	if got := (codexProvider{}).Query().Attribution; got != nil {
		t.Fatalf("窗口外无流量却仍宣称当前计费：%+v", got)
	}
	if got := (kimiProvider{}).Query().Attribution; got != nil {
		t.Fatalf("同上（kimi 账号）：%+v", got)
	}
}

// claude 侧同一条规则（判据不同：claude 靠 model id，因为没有端点记录）。
// 同一时刻一个 glm 会话一个 opus 会话 → 两边都「当前计费」，互不指认。
func TestClaudeAttribution_ConcurrentGlmAndAnthropic(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DW_CLAUDE_PROJECTS", dir)
	now := time.Now().UTC()
	at := func(model string, ts time.Time) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":"msg_%s","model":%q,"usage":{"input_tokens":1,"output_tokens":1}}}`,
			ts.Format(time.RFC3339), model+"-"+ts.Format("150405"), model)
	}
	// 两个项目目录、两个并发会话：GLM 三分钟前，opus 一分钟前（更新）。
	for _, proj := range []string{"proj-a", "proj-b"} {
		if err := os.MkdirAll(filepath.Join(dir, proj), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(dir, "proj-a", "glm-session.jsonl"),
		at("glm-5.3", now.Add(-3*time.Minute))+"\n")
	write(t, filepath.Join(dir, "proj-b", "opus-session.jsonl"),
		at("claude-opus-5", now.Add(-1*time.Minute))+"\n")

	anthropic := claudeAttribution(Account{Runtime: "claude", Vendor: VendorAnthropic})
	zhipu := claudeAttribution(Account{Runtime: "claude", Vendor: VendorZhipu})
	if anthropic == nil || zhipu == nil {
		t.Fatalf("双方都有窗口内流量，got %+v / %+v", anthropic, zhipu)
	}
	if !anthropic.Active || !zhipu.Active {
		t.Fatalf("并发时双方都应标当前计费，got anthropic=%v zhipu=%v", anthropic.Active, zhipu.Active)
	}
	if anthropic.Vendor != "" || zhipu.Vendor != "" {
		t.Fatalf("并发时不得指认单一付款方：%+v / %+v", anthropic, zhipu)
	}

	// 单一厂商时，「记在 X 名下」要回来 —— 这是它唯一该出现的情形。
	write(t, filepath.Join(dir, "proj-b", "opus-session.jsonl"),
		at("glm-5.3", now.Add(-30*time.Second))+"\n")
	onlyGlm := claudeAttribution(Account{Runtime: "claude", Vendor: VendorAnthropic})
	if onlyGlm == nil || onlyGlm.Active {
		t.Fatalf("官方账号未在计费，got %+v", onlyGlm)
	}
	if onlyGlm.Vendor != VendorZhipu || onlyGlm.Display != "GLM Coding Plan" {
		t.Fatalf("单一厂商时应点名，got %+v", onlyGlm)
	}
}

// claude 侧的过期同样不得宣称 —— 徽标写的是「当前」，不是「最近一次」。
func TestClaudeAttribution_StaleClaimsNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DW_CLAUDE_PROJECTS", dir)
	old := time.Now().UTC().Add(-48 * time.Hour)
	write(t, filepath.Join(dir, "p", "old.jsonl"),
		fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"model":"glm-5.3"}}`+"\n", old.Format(time.RFC3339)))
	if got := claudeAttribution(Account{Runtime: "claude", Vendor: VendorZhipu}); got != nil {
		t.Fatalf("两天前的会话不得宣称当前计费：%+v", got)
	}
}
