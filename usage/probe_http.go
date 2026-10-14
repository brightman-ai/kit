package usage

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"

	"github.com/brightman-ai/kit/log"
)

// probe_http.go — the one HTTP client every quota probe dials out with.
//
// The probes used http.DefaultClient, which proxies only when the PROCESS environment says so.
// That makes a probe's reachability a property of whichever shell happened to start the host:
// observed 2026-10-13, the terminal server was started from a shell without HTTPS_PROXY on a
// machine whose system proxy (127.0.0.1:1080) is how it reaches chatgpt.com at all — every codex
// probe for fifteen hours was a "connection reset by peer", while the same request through the
// system proxy answered 200. The browser on that machine, and every other app, uses the system
// proxy; a probe asking the same server the same question should reach it the same way.
//
// So: the environment wins whenever it names a proxy (HTTPS_PROXY / HTTP_PROXY / ALL_PROXY, set
// but empty included — an explicit configuration is never second-guessed), and otherwise the
// operating system's proxy settings apply, exceptions included. NO_PROXY is honoured on both
// paths. Where the platform has no readable system setting, this is the environment alone.
//
// The environment is resolved with golang.org/x/net/http/httpproxy on every call, never with
// http.ProxyFromEnvironment: that one reads the variables once per process (a proxy exported
// after the first request anywhere is never seen) and does not know ALL_PROXY.

var probeHTTPClient = &http.Client{Transport: probeTransport()}

func probeTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = probeProxy
	return t
}

// probeInfo is the one place this file speaks; a variable so a test can count what is said.
var (
	probeLogger = log.Module("usage")
	probeInfo   = func(msg string, args ...any) { probeLogger.Info(msg, args...) }
)

func probeProxy(req *http.Request) (*url.URL, error) {
	cfg, named := proxyConfigFromEnv()
	if named {
		return cfg.ProxyFunc()(req.URL)
	}
	p, err := systemProxyFor(req.Context(), req.URL)
	if p == nil || err != nil {
		return nil, err
	}
	if excludedByNoProxy(cfg.NoProxy, req.URL) {
		return nil, nil
	}
	return p, nil
}

// proxyConfigFromEnv reads the proxy environment NOW. named reports whether it names a proxy at
// all — the condition for the environment to be authoritative.
//
// Each scheme's proxy comes from its own variable, else from the catch-all ALL_PROXY (curl's, and
// what a socks5:// URL usually lives in; httpproxy does not know it). Within a pair the uppercase
// spelling wins, and what counts is PRESENCE, not value: a variable that is present but empty is
// the user opting that scheme out of proxying, so it ends the search — httpproxy's own reading
// takes the first NON-empty value, which would let an empty HTTPS_PROXY fall through to
// ALL_PROXY or to a lowercase spelling it was meant to override.
func proxyConfigFromEnv() (cfg *httpproxy.Config, named bool) {
	cfg = httpproxy.FromEnvironment() // NO_PROXY, CGI
	lookup := func(names ...string) (string, bool) {
		for _, n := range names {
			if v, ok := os.LookupEnv(n); ok {
				return v, true
			}
		}
		return "", false
	}
	all, allSet := lookup("ALL_PROXY", "all_proxy")
	pick := func(upper, lower string) string {
		if v, ok := lookup(upper, lower); ok {
			named = true
			return v
		}
		if allSet {
			named = true
		}
		return all // "" when ALL_PROXY is absent
	}
	cfg.HTTPProxy = pick("HTTP_PROXY", "http_proxy")
	cfg.HTTPSProxy = pick("HTTPS_PROXY", "https_proxy")
	return cfg, named
}

// excludedByNoProxy applies a NO_PROXY value to a request with httpproxy's own matching
// (domains and their subdomains, leading dots, CIDR blocks, ports, "*") — borrowed rather than
// re-implemented, by asking it about a stand-in proxy.
func excludedByNoProxy(noProxy string, u *url.URL) bool {
	if noProxy == "" {
		return false
	}
	standIn := httpproxy.Config{HTTPProxy: "http://proxy.invalid", HTTPSProxy: "http://proxy.invalid", NoProxy: noProxy}
	p, _ := standIn.ProxyFunc()(u)
	return p == nil
}

// systemProxy is the operating system's proxy configuration, as far as a probe cares: one proxy
// per scheme (SOCKS standing behind both), the hosts that bypass it, and the two switches that
// change how the rest is read.
type systemProxy struct {
	http, https, socks *url.URL
	exceptions         []string
	excludeSimple      bool // ExcludeSimpleHostnames: names without a dot never use the proxy
	pac                bool // an auto-config (PAC) script is enabled; it cannot be evaluated here
}

var (
	sysProxyMu      sync.Mutex
	sysProxy        systemProxy
	sysProxyRead    time.Time     // when sysProxy was last refreshed; zero until the first read finishes
	sysProxyRefresh chan struct{} // non-nil while a read is in flight; closed when it has landed
)

// sysProxyTTL is how old the cached setting may get before a refresh is started. A proxy toggled
// in the menu bar is picked up within this (plus one read); a probe runs every few minutes, so
// re-reading per request buys nothing. Past the TTL the old answer is still served — see
// currentSystemProxy.
const sysProxyTTL = 30 * time.Second

// pacNoticeOnce keeps the "PAC is unsupported" note to one line per process: the setting does not
// change per request, and a probe that runs every few minutes would otherwise repeat it forever.
var pacNoticeOnce sync.Once

// systemProxyReader is readSystemProxy — a variable so a test can stall or fail it.
var systemProxyReader = readSystemProxy

// currentSystemProxy returns the cached system setting, stale-while-revalidate: an expired answer
// is served as it is while ONE background read replaces it. Reading is a subprocess that can
// stall, and the setting changes only when a human flips a switch — neither justifies making a
// request that already has an answer wait, let alone every concurrent request behind a lock.
//
// Only the very first lookup has nothing to serve. It waits for the read, but only as long as its
// own request is willing to: the read itself belongs to nobody and runs on for the next caller.
func currentSystemProxy(ctx context.Context) (systemProxy, error) {
	sysProxyMu.Lock()
	if time.Since(sysProxyRead) > sysProxyTTL && sysProxyRefresh == nil {
		sysProxyRefresh = make(chan struct{})
		go refreshSystemProxy(systemProxyReader, sysProxyRefresh)
	}
	p, warm, landing := sysProxy, !sysProxyRead.IsZero(), sysProxyRefresh
	sysProxyMu.Unlock()
	if warm {
		return p, nil
	}
	select {
	case <-landing:
	case <-ctx.Done():
		return systemProxy{}, ctx.Err()
	}
	sysProxyMu.Lock()
	defer sysProxyMu.Unlock()
	return sysProxy, nil
}

// refreshSystemProxy runs one read and publishes it. A read that failed says nothing about the
// setting, so it never replaces what is cached (before the first success that is "no proxy", the
// zero value) — but it still restarts the TTL, so a broken scutil is retried every TTL, not on
// every request.
func refreshSystemProxy(read func() (systemProxy, bool), landed chan struct{}) {
	p, ok := read() // per-platform; bounded by its own timeout
	sysProxyMu.Lock()
	if ok {
		sysProxy = p
	}
	sysProxyRead = time.Now()
	sysProxyRefresh = nil
	sysProxyMu.Unlock()
	close(landed)
}

func systemProxyFor(ctx context.Context, u *url.URL) (*url.URL, error) {
	p, err := currentSystemProxy(ctx)
	if err != nil {
		return nil, err
	}

	host := u.Hostname()
	if bypassesProxy(host, p.exceptions) || (p.excludeSimple && isSimpleHostname(host)) {
		return nil, nil
	}
	if p.pac {
		// The script decides per URL, so there is nothing to fall back on: a manual entry beside
		// it is not the system's answer. Go direct — what probes did before the system proxy was
		// read at all — and say so, because a quota that silently stops moving is the symptom.
		pacNoticeOnce.Do(func() {
			probeInfo("system proxy uses a PAC script, which quota probes cannot evaluate; connecting directly")
		})
		return nil, nil
	}
	if u.Scheme == "https" {
		if p.https != nil {
			return p.https, nil
		}
	} else if p.http != nil {
		return p.http, nil
	}
	return p.socks, nil
}

// isSimpleHostname is a name macOS calls "simple": no dot, and not an IP literal (an IPv6 literal
// has no dot either).
func isSimpleHostname(host string) bool {
	return host != "" && net.ParseIP(host) == nil && !strings.Contains(host, ".")
}

// bypassesProxy applies the exceptions list the way the system does for the patterns it uses:
// exact hosts, "*.suffix" (subdomains only) / ".suffix" domains, CIDR blocks for literal IPs,
// "<local>" for simple hostnames, and loopback always.
func bypassesProxy(host string, exceptions []string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return true
	}
	host = strings.ToLower(host)
	for _, ex := range exceptions {
		ex = strings.ToLower(strings.TrimSpace(ex))
		switch {
		case ex == "":
		case ex == "<local>": // simple hostnames — no dots — as macOS means it
			if isSimpleHostname(host) {
				return true
			}
		case strings.Contains(ex, "/"):
			if _, block, err := net.ParseCIDR(ex); err == nil && ip != nil && block.Contains(ip) {
				return true
			}
		case strings.HasPrefix(ex, "*."):
			// "*.example.com" excludes the subdomains; example.com itself is still proxied
			// (CFNetwork's reading, and the one that matters: the apex is the API host).
			if strings.HasSuffix(host, ex[1:]) {
				return true
			}
		case strings.HasPrefix(ex, "."):
			if strings.HasSuffix(host, ex) || host == ex[1:] {
				return true
			}
		case host == ex:
			return true
		}
	}
	return false
}

// parseScutilProxy reads the output of macOS `scutil --proxy` into a systemProxy. Kept
// platform-neutral so it is testable anywhere.
//
// The output is nested dictionaries: the global settings at the top level, and — when interfaces
// carry their own — a __SCOPED__ dictionary holding one dictionary per interface, each with its
// own Enable flags, proxies and ExceptionsList. Only the top level is the system's answer for a
// process that is bound to no interface; reading the scoped ones too lets a VPN's "off" or
// unreachable proxy govern every probe, and pours its exceptions into the global list.
func parseScutilProxy(out string) systemProxy {
	vals := map[string]string{}
	var exceptions []string
	var open []string // keys of the containers currently open; open[0] is the top-level dictionary
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		key, v, hasValue := strings.Cut(line, " : ")
		key, v = strings.TrimSpace(key), strings.TrimSpace(v)
		switch {
		case line == "}":
			if len(open) > 0 {
				open = open[:len(open)-1]
			}
		case strings.HasSuffix(line, "<dictionary> {") || strings.HasSuffix(line, "<array> {"):
			if !hasValue {
				key = "" // the unnamed top-level dictionary
			}
			open = append(open, key)
		case !hasValue:
		case len(open) == 1: // a setting of the top-level dictionary
			vals[key] = v
		case len(open) == 2 && open[1] == "ExceptionsList": // an element of the top-level array
			exceptions = append(exceptions, v)
		}
	}
	proxy := func(prefix, scheme string) *url.URL {
		if vals[prefix+"Enable"] != "1" || vals[prefix+"Proxy"] == "" {
			return nil
		}
		host := vals[prefix+"Proxy"]
		if port := vals[prefix+"Port"]; port != "" {
			host = net.JoinHostPort(host, port)
		}
		return &url.URL{Scheme: scheme, Host: host}
	}
	return systemProxy{
		http:          proxy("HTTP", "http"),
		https:         proxy("HTTPS", "http"),
		socks:         proxy("SOCKS", "socks5"),
		exceptions:    exceptions,
		excludeSimple: vals["ExcludeSimpleHostnames"] == "1",
		pac:           vals["ProxyAutoConfigEnable"] == "1",
	}
}
