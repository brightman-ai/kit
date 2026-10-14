package usage

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A real `scutil --proxy` answer (this developer's machine, 2026-10-14).
const scutilSample = `<dictionary> {
  ExceptionsList : <array> {
    0 : 127.0.0.1
    1 : 192.168.0.0/16
    2 : 10.0.0.0/8
    3 : localhost
    4 : *.local
    5 : <local>
    6 : stwork
  }
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 1080
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 1080
  HTTPSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}
`

// scutilScoped is what `scutil --proxy` prints when interfaces carry their own settings: the
// global dictionary first, then a __SCOPED__ dictionary holding one nested dictionary per
// interface. Here the global settings proxy through 127.0.0.1:1080, while a VPN interface
// (utun4) has everything off, an unreachable proxy of its own, and its own exceptions.
const scutilScoped = `<dictionary> {
  ExceptionsList : <array> {
    0 : 127.0.0.1
    1 : *.local
  }
  HTTPEnable : 1
  HTTPPort : 1080
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 1080
  HTTPSProxy : 127.0.0.1
  __SCOPED__ : <dictionary> {
    en0 : <dictionary> {
      ExceptionsList : <array> {
        0 : chatgpt.com
      }
      HTTPSEnable : 0
      HTTPSPort : 3128
      HTTPSProxy : 10.9.9.9
    }
    utun4 : <dictionary> {
      ExceptionsList : <array> {
        0 : <local>
        1 : api.openai.com
      }
      ExcludeSimpleHostnames : 1
      HTTPEnable : 0
      HTTPSEnable : 0
      HTTPSPort : 3128
      HTTPSProxy : 10.8.8.8
      SOCKSEnable : 1
      SOCKSPort : 9
      SOCKSProxy : 10.8.8.8
    }
  }
}
`

// allProxyEnv is every variable the proxy decision reads, listed here independently of the code
// under test so a test cleans the environment even if that code changes what it reads.
var allProxyEnv = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"}

// clearProxyEnv makes every proxy variable truly absent (not merely empty) for this test and
// restores the developer's own values afterwards.
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, k := range allProxyEnv {
		t.Setenv(k, "") // registers the restore of the developer's own value…
		os.Unsetenv(k)  // …then makes it truly absent, which an empty value is not
	}
}

// seedSystemProxy installs a parsed `scutil --proxy` answer as the cached system setting, fresh
// for the whole test, so a test never shells out to the real machine's configuration.
func seedSystemProxy(t *testing.T, scutil string) {
	t.Helper()
	seedSystemProxyAt(t, scutil, time.Now().Add(time.Hour))
}

// seedSystemProxyAt is seedSystemProxy with the moment the answer was read: in the past makes it
// stale, the zero time makes it "never read".
func seedSystemProxyAt(t *testing.T, scutil string, readAt time.Time) {
	t.Helper()
	sysProxyMu.Lock()
	saved, savedAt := sysProxy, sysProxyRead
	sysProxy, sysProxyRead = parseScutilProxy(scutil), readAt
	sysProxyMu.Unlock()
	t.Cleanup(func() {
		sysProxyMu.Lock()
		sysProxy, sysProxyRead = saved, savedAt
		sysProxyMu.Unlock()
	})
}

// proxyFor is what the probe transport would dial for rawURL: a proxy URL string, or "direct".
func proxyFor(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := probeProxy(&http.Request{URL: u})
	if err != nil {
		t.Fatalf("probeProxy(%s): %v", rawURL, err)
	}
	if got == nil {
		return "direct"
	}
	return got.String()
}

func TestSystemProxy_ParsesScutilAndHonoursExceptions(t *testing.T) {
	p := parseScutilProxy(scutilSample)
	if p.https == nil || p.https.Host != "127.0.0.1:1080" || p.http == nil || p.http.Host != "127.0.0.1:1080" {
		t.Fatalf("proxies = http %v https %v, want both 127.0.0.1:1080", p.http, p.https)
	}
	for host, bypass := range map[string]bool{
		"chatgpt.com":      false,
		"open.bigmodel.cn": false,
		"192.168.1.20":     true,
		"10.2.3.4":         true,
		"printer.local":    true,
		"stwork":           true, // exact
		"nas":              true, // <local>: no dots
		"localhost":        true,
		"127.0.0.1":        true,
		"evil-local.com":   false,
		"api.moonshot.cn":  false,
	} {
		if got := bypassesProxy(host, p.exceptions); got != bypass {
			t.Errorf("bypass(%q) = %v, want %v", host, got, bypass)
		}
	}
}

func TestSystemProxy_DisabledMeansDirect(t *testing.T) {
	p := parseScutilProxy("<dictionary> {\n  HTTPSEnable : 0\n  HTTPSProxy : 127.0.0.1\n  HTTPSPort : 1080\n}\n")
	if p.https != nil || p.http != nil {
		t.Fatalf("a disabled proxy must not be used: %v %v", p.http, p.https)
	}
}

// F2: only the top-level dictionary is the system's answer. A __SCOPED__ interface dictionary
// that says "off" or names an unreachable proxy, or that carries its own exceptions, must neither
// override the global values nor add to the global exceptions.
func TestScutilParse_OnlyTopLevelDictionaryCounts(t *testing.T) {
	p := parseScutilProxy(scutilScoped)
	if p.https == nil || p.https.String() != "http://127.0.0.1:1080" {
		t.Errorf("https = %v, want the GLOBAL http://127.0.0.1:1080 (a scoped interface overrode it)", p.https)
	}
	if p.http == nil || p.http.String() != "http://127.0.0.1:1080" {
		t.Errorf("http = %v, want the GLOBAL http://127.0.0.1:1080", p.http)
	}
	if want := []string{"127.0.0.1", "*.local"}; strings.Join(p.exceptions, ",") != strings.Join(want, ",") {
		t.Errorf("exceptions = %v, want only the global %v (scoped interfaces added theirs)", p.exceptions, want)
	}
	for _, host := range []string{"chatgpt.com", "api.openai.com"} {
		if bypassesProxy(host, p.exceptions) {
			t.Errorf("%s bypasses the proxy only because a scoped interface listed it", host)
		}
	}

	clearProxyEnv(t)
	seedSystemProxy(t, scutilScoped)
	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:1080" {
		t.Errorf("chatgpt.com via %s, want the global system proxy", got)
	}
	// ExcludeSimpleHostnames and SOCKS exist only inside utun4: global has neither.
	if got := proxyFor(t, "https://quota-gateway/"); got != "http://127.0.0.1:1080" {
		t.Errorf("quota-gateway via %s: a scoped ExcludeSimpleHostnames leaked into the global answer", got)
	}
}

// F3: "*.example.com" is a subdomain pattern. macOS proxies example.com itself; reading the
// wildcard as covering the apex sent chatgpt.com direct on a machine where direct never works.
// Entries without a wildcard keep their meaning.
func TestBypass_WildcardDomainExcludesSubdomainsNotTheApex(t *testing.T) {
	for _, tc := range []struct {
		exception, host string
		bypass          bool
	}{
		{"*.chatgpt.com", "chatgpt.com", false},
		{"*.chatgpt.com", "api.chatgpt.com", true},
		{"*.chatgpt.com", "a.b.chatgpt.com", true},
		{"*.chatgpt.com", "notchatgpt.com", false},
		{"*.chatgpt.com", "CHATGPT.com", false},
		{"*.chatgpt.com", "API.ChatGPT.com", true},
		{"*.local", "local", false},
		{"*.local", "printer.local", true},
		// no wildcard: unchanged
		{".chatgpt.com", "chatgpt.com", true},
		{".chatgpt.com", "api.chatgpt.com", true},
		{"chatgpt.com", "chatgpt.com", true},
		{"chatgpt.com", "api.chatgpt.com", false},
	} {
		if got := bypassesProxy(tc.host, []string{tc.exception}); got != tc.bypass {
			t.Errorf("bypass(%q) with exception %q = %v, want %v", tc.host, tc.exception, got, tc.bypass)
		}
	}
}

// The environment, whenever it names a proxy, is the whole answer — including when what it says
// is "not this host". A system proxy must never override an explicit NO_PROXY, and an empty
// HTTPS_PROXY that is present is a statement too ("no proxy"), not an absence.
func TestProbeProxy_EnvironmentIsAuthoritative(t *testing.T) {
	clearProxyEnv(t)
	seedSystemProxy(t, scutilSample) // a system proxy that WOULD apply, to prove it is not consulted

	// NO_PROXY alone names no proxy: the system one applies, minus what NO_PROXY excludes.
	t.Setenv("NO_PROXY", "chatgpt.com")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "direct" {
		t.Fatalf("NO_PROXY=chatgpt.com must keep chatgpt.com off the system proxy, got %s", got)
	}
	if got := proxyFor(t, "https://open.bigmodel.cn/"); got != "http://127.0.0.1:1080" {
		t.Fatalf("a NO_PROXY that names only chatgpt.com must not switch the system proxy off elsewhere, got %s", got)
	}

	// And with nothing in the environment, the system setting applies.
	os.Unsetenv("NO_PROXY")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:1080" {
		t.Fatalf("with no proxy environment the system proxy must apply, got %s", got)
	}

	// A proxy variable makes the environment authoritative: HTTPS_PROXY says nothing about http.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7777")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7777" {
		t.Fatalf("HTTPS_PROXY must win over the system proxy, got %s", got)
	}
	if got := proxyFor(t, "http://example.org/"); got != "direct" {
		t.Fatalf("with the environment authoritative and no HTTP_PROXY, http goes direct, got %s", got)
	}
	if got := proxyFor(t, "https://localhost:8443/"); got != "direct" {
		t.Fatalf("loopback never proxies, got %s", got)
	}

	// Present-but-empty is still the environment speaking.
	t.Setenv("HTTPS_PROXY", "")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "direct" {
		t.Fatalf("an empty HTTPS_PROXY that is present means no proxy, got %s", got)
	}
}

// F1: ALL_PROXY is the fallback for both schemes — net/http itself ignores it, which used to turn
// "ALL_PROXY=socks5://…" into "the environment spoke, so go direct".
func TestProbeProxy_AllProxyIsTheFallbackForBothSchemes(t *testing.T) {
	clearProxyEnv(t)
	seedSystemProxy(t, scutilSample) // must not win over an explicit ALL_PROXY

	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1080")
	for _, target := range []string{"https://chatgpt.com/backend-api/wham/usage", "http://example.org/"} {
		if got := proxyFor(t, target); got != "socks5://127.0.0.1:1080" {
			t.Errorf("ALL_PROXY alone: %s via %s, want socks5://127.0.0.1:1080", target, got)
		}
	}

	// A scheme's own variable outranks ALL_PROXY for that scheme only.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7000")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7000" {
		t.Errorf("HTTPS_PROXY must outrank ALL_PROXY for https, got %s", got)
	}
	if got := proxyFor(t, "http://example.org/"); got != "socks5://127.0.0.1:1080" {
		t.Errorf("http still falls back to ALL_PROXY, got %s", got)
	}

	// NO_PROXY applies to the fallback too.
	t.Setenv("NO_PROXY", "chatgpt.com,.internal")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "direct" {
		t.Errorf("NO_PROXY must be honoured for ALL_PROXY, got %s", got)
	}
	if got := proxyFor(t, "https://quota.internal/"); got != "direct" {
		t.Errorf("NO_PROXY .internal must be honoured, got %s", got)
	}
}

func TestProbeProxy_AllProxyLowercase(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("all_proxy", "socks5://127.0.0.1:1081")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "socks5://127.0.0.1:1081" {
		t.Fatalf("lowercase all_proxy ignored, got %s", got)
	}
}

// F7: net/http's own environment resolver reads the variables once per process. A probe that
// delegates to it keeps using whatever the environment said at the first request anyone made.
func TestProbeProxy_ReadsTheEnvironmentOnEveryCall(t *testing.T) {
	clearProxyEnv(t)
	// Initialise net/http's once-per-process cache while the environment is empty…
	warm, _ := http.NewRequest("GET", "https://chatgpt.com/", nil)
	if _, err := http.ProxyFromEnvironment(warm); err != nil {
		t.Fatal(err)
	}
	// …then change the environment, as a host that exports HTTPS_PROXY late — or a user — does.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7001")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7001" {
		t.Fatalf("a proxy exported after startup was not seen, got %s", got)
	}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7002")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7002" {
		t.Fatalf("a changed proxy kept resolving to the old one, got %s", got)
	}
}

// F5: a system that offers only a SOCKS proxy is still a system proxy.
func TestSystemProxy_SocksIsUsedWhenHTTPIsNotSet(t *testing.T) {
	clearProxyEnv(t)

	seedSystemProxy(t, "<dictionary> {\n  SOCKSEnable : 1\n  SOCKSPort : 1080\n  SOCKSProxy : 127.0.0.1\n}\n")
	for _, target := range []string{"https://chatgpt.com/", "http://example.org/"} {
		if got := proxyFor(t, target); got != "socks5://127.0.0.1:1080" {
			t.Errorf("SOCKS-only: %s via %s, want socks5://127.0.0.1:1080", target, got)
		}
	}

	// Per scheme: HTTPS configured, HTTP not — https uses its own, http falls to SOCKS.
	seedSystemProxy(t, "<dictionary> {\n  HTTPSEnable : 1\n  HTTPSPort : 8080\n  HTTPSProxy : 127.0.0.1\n  SOCKSEnable : 1\n  SOCKSPort : 1080\n  SOCKSProxy : 127.0.0.1\n}\n")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:8080" {
		t.Errorf("an enabled HTTPS proxy outranks SOCKS for https, got %s", got)
	}
	if got := proxyFor(t, "http://example.org/"); got != "socks5://127.0.0.1:1080" {
		t.Errorf("http with no HTTP proxy falls back to SOCKS, got %s", got)
	}

	// A disabled SOCKS entry is not a proxy.
	seedSystemProxy(t, "<dictionary> {\n  SOCKSEnable : 0\n  SOCKSPort : 1080\n  SOCKSProxy : 127.0.0.1\n}\n")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "direct" {
		t.Errorf("disabled SOCKS must go direct, got %s", got)
	}
}

// F5: a PAC script cannot be evaluated here, so it cannot be honoured — and guessing from a
// manual entry beside it would be a second opinion the system never gave. Go direct, and say
// so once: silence is how this class of "quota frozen, nobody knows why" lasts for hours.
func TestSystemProxy_PACGoesDirectAndSaysSoOnce(t *testing.T) {
	clearProxyEnv(t)
	seedSystemProxy(t, "<dictionary> {\n  HTTPSEnable : 1\n  HTTPSPort : 1080\n  HTTPSProxy : 127.0.0.1\n"+
		"  ProxyAutoConfigEnable : 1\n  ProxyAutoConfigURLString : http://127.0.0.1:9999/proxy.pac\n}\n")

	var mu sync.Mutex
	var notes []string
	origInfo := probeInfo
	probeInfo = func(msg string, _ ...any) { mu.Lock(); notes = append(notes, msg); mu.Unlock() }
	pacNoticeOnce = sync.Once{}
	t.Cleanup(func() { probeInfo = origInfo; pacNoticeOnce = sync.Once{} })

	for i := 0; i < 3; i++ {
		if got := proxyFor(t, "https://chatgpt.com/"); got != "direct" {
			t.Fatalf("an enabled PAC must go direct, got %s", got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notes) != 1 {
		t.Fatalf("PAC must be reported exactly once at Info, got %d notes: %q", len(notes), notes)
	}
	if !strings.Contains(notes[0], "PAC") {
		t.Errorf("the note must say PAC is unsupported, got %q", notes[0])
	}
}

func TestSystemProxy_PACDisabledIsNotReported(t *testing.T) {
	clearProxyEnv(t)
	seedSystemProxy(t, scutilSample) // ProxyAutoConfigEnable : 0
	var notes int
	origInfo := probeInfo
	probeInfo = func(string, ...any) { notes++ }
	pacNoticeOnce = sync.Once{}
	t.Cleanup(func() { probeInfo = origInfo; pacNoticeOnce = sync.Once{} })

	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:1080" {
		t.Fatalf("got %s", got)
	}
	if notes != 0 {
		t.Fatalf("no PAC is enabled, nothing to report; got %d notes", notes)
	}
}

// F6: ExcludeSimpleHostnames=1 is its own switch (not a "<local>" entry): no proxy for a name
// without a dot — `https://quota-gateway` is an internal host the proxy cannot resolve.
func TestSystemProxy_ExcludeSimpleHostnames(t *testing.T) {
	clearProxyEnv(t)
	seedSystemProxy(t, "<dictionary> {\n  ExcludeSimpleHostnames : 1\n  HTTPSEnable : 1\n  HTTPSPort : 1080\n  HTTPSProxy : 127.0.0.1\n}\n")
	for target, want := range map[string]string{
		"https://quota-gateway/":        "direct",
		"https://quota-gateway:8443/x":  "direct",
		"https://chatgpt.com/":          "http://127.0.0.1:1080",
		"https://[2001:db8::1]:8443/":   "http://127.0.0.1:1080", // an IP literal is not a simple hostname
		"https://quota-gateway.corp.x/": "http://127.0.0.1:1080",
	} {
		if got := proxyFor(t, target); got != want {
			t.Errorf("ExcludeSimpleHostnames=1: %s via %s, want %s", target, got, want)
		}
	}

	seedSystemProxy(t, "<dictionary> {\n  ExcludeSimpleHostnames : 0\n  HTTPSEnable : 1\n  HTTPSPort : 1080\n  HTTPSProxy : 127.0.0.1\n}\n")
	if got := proxyFor(t, "https://quota-gateway/"); got != "http://127.0.0.1:1080" {
		t.Errorf("ExcludeSimpleHostnames=0 must not bypass simple hostnames, got %s", got)
	}
}

// Presence, not value, decides whether a proxy variable speaks — and for which scheme. An
// explicitly empty HTTPS_PROXY is the user opting https out of proxying; it must not fall through
// to ALL_PROXY, nor (uppercase being the documented winner) to a lowercase spelling of itself.
func TestProbeProxy_PresentButEmptyVariableOptsOutWithoutFallback(t *testing.T) {
	const socks = "socks5://127.0.0.1:1080"
	clearProxyEnv(t)
	seedSystemProxy(t, scutilSample)

	t.Setenv("ALL_PROXY", socks)
	t.Setenv("HTTPS_PROXY", "")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "direct" {
		t.Errorf("HTTPS_PROXY=\"\" with ALL_PROXY set: https via %s, want direct (opted out, no fallback)", got)
	}
	if got := proxyFor(t, "http://example.org/"); got != socks {
		t.Errorf("the opt-out is per scheme: http still falls back to ALL_PROXY, got %s", got)
	}

	os.Unsetenv("HTTPS_PROXY")
	t.Setenv("HTTP_PROXY", "")
	if got := proxyFor(t, "http://example.org/"); got != "direct" {
		t.Errorf("HTTP_PROXY=\"\" with ALL_PROXY set: http via %s, want direct", got)
	}
	if got := proxyFor(t, "https://chatgpt.com/"); got != socks {
		t.Errorf("https still falls back to ALL_PROXY, got %s", got)
	}

	// A lowercase spelling that is present but empty opts out just the same.
	clearProxyEnv(t)
	t.Setenv("ALL_PROXY", socks)
	t.Setenv("https_proxy", "")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "direct" {
		t.Errorf("https_proxy=\"\" with ALL_PROXY set: https via %s, want direct", got)
	}

	// Uppercase outranks lowercase even when the uppercase one is empty…
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "http://127.0.0.1:7100")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "direct" {
		t.Errorf("empty HTTPS_PROXY must not fall through to https_proxy, got %s", got)
	}
	// …and when it is not.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7200")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7200" {
		t.Errorf("HTTPS_PROXY must outrank https_proxy, got %s", got)
	}
	// Lowercase alone still speaks.
	os.Unsetenv("HTTPS_PROXY")
	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7100" {
		t.Errorf("https_proxy alone must apply, got %s", got)
	}

	// The same rule for the catch-all: an empty ALL_PROXY is "no fallback", not "look at all_proxy".
	clearProxyEnv(t)
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", socks)
	for _, target := range []string{"https://chatgpt.com/", "http://example.org/"} {
		if got := proxyFor(t, target); got != "direct" {
			t.Errorf("empty ALL_PROXY must not fall through to all_proxy: %s via %s, want direct", target, got)
		}
	}
}

// ── the system-proxy cache is stale-while-revalidate ─────────────────────────────────────────
//
// Observed: with the cache expired and scutil stalled, every probe waited out scutil's 2s timeout
// under the cache mutex — a 25ms request took 352ms. The answer changes when a human toggles a
// menu-bar switch; nothing about that justifies blocking a request that already has one.

// fakeSystemProxyReader stands in for scutil: it can stall (until released) and can fail.
type fakeSystemProxyReader struct {
	gate  chan struct{} // nil: answers at once
	proxy systemProxy
	ok    bool
	calls atomic.Int32
	once  sync.Once
}

func (r *fakeSystemProxyReader) read() (systemProxy, bool) {
	r.calls.Add(1)
	if r.gate != nil {
		<-r.gate
	}
	return r.proxy, r.ok
}

func (r *fakeSystemProxyReader) release() {
	if r.gate != nil {
		r.once.Do(func() { close(r.gate) })
	}
}

// installSystemProxyReader swaps scutil for r. On cleanup it lets a stalled read finish and waits
// for the refresh to land, so no goroutine of this test writes the shared cache after it ends.
// Call it AFTER seeding the cache: cleanups run last-in-first-out.
func installSystemProxyReader(t *testing.T, r *fakeSystemProxyReader) {
	t.Helper()
	orig := systemProxyReader
	systemProxyReader = r.read
	t.Cleanup(func() {
		r.release()
		eventually(t, "the system-proxy refresh to finish", func() bool {
			sysProxyMu.Lock()
			defer sysProxyMu.Unlock()
			return sysProxyRefresh == nil
		})
		systemProxyReader = orig
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

const (
	scutilHTTPS1080 = "<dictionary> {\n  HTTPSEnable : 1\n  HTTPSPort : 1080\n  HTTPSProxy : 127.0.0.1\n}\n"
	scutilHTTPS2080 = "<dictionary> {\n  HTTPSEnable : 1\n  HTTPSPort : 2080\n  HTTPSProxy : 127.0.0.1\n}\n"
)

func TestSystemProxy_StaleAnswerIsServedWhileRefreshing(t *testing.T) {
	clearProxyEnv(t)
	seedSystemProxyAt(t, scutilHTTPS1080, time.Now().Add(-time.Hour)) // long expired
	r := &fakeSystemProxyReader{gate: make(chan struct{}), proxy: parseScutilProxy(scutilHTTPS2080), ok: true}
	installSystemProxyReader(t, r) // scutil is now stalled

	type outcome struct {
		got string
		err error
	}
	const probes = 12
	out := make(chan outcome, probes)
	for i := 0; i < probes; i++ {
		go func() {
			u, _ := url.Parse("https://chatgpt.com/")
			p, err := probeProxy(&http.Request{URL: u})
			o := outcome{err: err}
			if p != nil {
				o.got = p.String()
			}
			out <- o
		}()
	}
	for i := 0; i < probes; i++ {
		select {
		case o := <-out:
			if o.err != nil || o.got != "http://127.0.0.1:1080" {
				t.Errorf("probe got (%q, %v), want the cached http://127.0.0.1:1080", o.got, o.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("probes are waiting for the stalled system-proxy refresh instead of serving the cached answer")
		}
	}
	// The read runs in the background, so it may start after every probe has already been served:
	// wait for it, then give a broken single-flight time to show a second one.
	eventually(t, "the background refresh to start", func() bool { return r.calls.Load() >= 1 })
	time.Sleep(50 * time.Millisecond)
	if n := r.calls.Load(); n != 1 {
		t.Errorf("refresh must be single-flight: %d concurrent probes started %d reads", probes, n)
	}

	// Once scutil answers, the refreshed setting replaces the cached one.
	r.release()
	eventually(t, "the background refresh to land", func() bool {
		return proxyFor(t, "https://chatgpt.com/") == "http://127.0.0.1:2080"
	})
	if n := r.calls.Load(); n != 1 {
		t.Errorf("a fresh answer must not be refreshed again: %d reads", n)
	}
}

// With nothing cached there is nothing to serve, so the very first lookup must wait — but only as
// long as its own request is willing to.
func TestSystemProxy_FirstLookupWaitsOnlyAsLongAsTheCaller(t *testing.T) {
	clearProxyEnv(t)
	seedSystemProxyAt(t, "", time.Time{}) // never read
	r := &fakeSystemProxyReader{gate: make(chan struct{}), proxy: parseScutilProxy(scutilHTTPS1080), ok: true}
	installSystemProxyReader(t, r)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	u, _ := url.Parse("https://chatgpt.com/")
	req := (&http.Request{URL: u}).WithContext(ctx)
	errc := make(chan error, 1)
	go func() { _, err := probeProxy(req); errc <- err }()
	select {
	case err := <-errc:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("first lookup err = %v, want the caller's context.DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first lookup ignored its caller's deadline and waited for the stalled system-proxy read")
	}

	// The read the caller abandoned is still running for everyone else.
	r.release()
	eventually(t, "the abandoned read to populate the cache", func() bool {
		return proxyFor(t, "https://chatgpt.com/") == "http://127.0.0.1:1080"
	})
	if n := r.calls.Load(); n != 1 {
		t.Errorf("the abandoned read must be reused, not repeated: %d reads", n)
	}
}

func TestSystemProxy_FirstLookupGetsTheAnswer(t *testing.T) {
	clearProxyEnv(t)
	seedSystemProxyAt(t, "", time.Time{})
	installSystemProxyReader(t, &fakeSystemProxyReader{proxy: parseScutilProxy(scutilHTTPS1080), ok: true})
	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:1080" {
		t.Fatalf("with nothing cached the first lookup must wait for the read and use it, got %s", got)
	}
}

// A read that FAILS (scutil stalled past its timeout, say) says nothing about the setting; letting
// it replace a good cached answer with "no proxy" would send every probe direct for the next TTL.
func TestSystemProxy_FailedRefreshKeepsTheLastGoodAnswer(t *testing.T) {
	clearProxyEnv(t)
	seedSystemProxyAt(t, scutilHTTPS1080, time.Now().Add(-time.Hour))
	r := &fakeSystemProxyReader{ok: false}
	installSystemProxyReader(t, r)

	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:1080" {
		t.Fatalf("stale answer not served, got %s", got)
	}
	eventually(t, "the failed refresh to finish", func() bool { return r.calls.Load() == 1 && proxyRefreshIdle() })
	if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:1080" {
		t.Errorf("a failed refresh replaced the last good answer, got %s", got)
	}
	if n := r.calls.Load(); n != 1 {
		t.Errorf("a failed read must still wait out the TTL before the next try: %d reads", n)
	}
}

func proxyRefreshIdle() bool {
	sysProxyMu.Lock()
	defer sysProxyMu.Unlock()
	return sysProxyRefresh == nil
}

func TestSystemProxy_FreshAnswerIsNeverRefreshed(t *testing.T) {
	clearProxyEnv(t)
	seedSystemProxyAt(t, scutilHTTPS1080, time.Now().Add(-sysProxyTTL/2))
	r := &fakeSystemProxyReader{proxy: parseScutilProxy(scutilHTTPS2080), ok: true}
	installSystemProxyReader(t, r)
	for i := 0; i < 5; i++ {
		if got := proxyFor(t, "https://chatgpt.com/"); got != "http://127.0.0.1:1080" {
			t.Fatalf("got %s", got)
		}
	}
	if n := r.calls.Load(); n != 0 {
		t.Fatalf("an answer inside its TTL must not be re-read: %d reads", n)
	}
}
