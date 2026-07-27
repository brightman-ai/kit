package tunnel

// Edge-aware self-healing.
//
// cloudflared can be alive as a PROCESS yet have ZERO live connections to the Cloudflare edge — the
// exact shape of a production "Error 1033", where the tunnel looked healthy locally (pid up, log said
// "ready") but the public hostname was dark. A pid-only liveness probe (IsRunning / pidAlive) is
// structurally BLIND to this: it returns true for a process that has silently lost the edge. The
// self-healing watchdog (supervise.go) therefore consults a second, authoritative signal —
// cloudflared's own `--metrics` HTTP server `/ready` endpoint, which answers 200 only while at least
// one edge connection is registered, 503 otherwise.
//
// Why /ready and not the run-log: countReadyFromLog() counts "Registered"−"Unregistered" over a
// bounded 64 KB log tail. For a healthy long-lived tunnel the original registration lines have long
// scrolled out of that window, so it reads 0 — using it as a liveness signal would false-positive and
// restart perfectly healthy tunnels. /ready is a live query of cloudflared's actual connection state.

import (
	"net"
	"net/http"
	"time"
)

const (
	// edgeGrace is how long after a (re)start the watchdog withholds edge judgment. Edge registration
	// is not instant (a named tunnel can take tens of seconds; waitForRegistered allows up to 90s), and
	// killing a tunnel that is merely still connecting would be a self-inflicted outage. Must exceed
	// worst-case registration time. Only matters right after a start — a steady-state tunnel that goes
	// edge-dead has an old startedAt, so grace is already spent and detection is prompt.
	edgeGrace = 90 * time.Second
	// edgeFailThreshold is how many CONSECUTIVE not-ready probes (past grace) confirm the edge is
	// genuinely down rather than a transient blip. With SuperviseInterval=15s, 3 → act within ~45s.
	edgeFailThreshold = 3
	// edgeProbeTimeout bounds a single /ready probe. The metrics server is loopback; 2s is generous.
	edgeProbeTimeout = 2 * time.Second
)

// edgeProbeClient is a dedicated client for the loopback /ready probe: a short timeout and NO proxy
// (an inherited HTTPS_PROXY must never intercept a 127.0.0.1 health check), keep-alives off so a
// probe never pins a connection to a cloudflared that is about to be torn down.
var edgeProbeClient = &http.Client{
	Timeout:   edgeProbeTimeout,
	Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
}

// healthVerdict is the watchdog's per-tick assessment of a supervised, running tunnel.
type healthVerdict int

const (
	healthDead        healthVerdict = iota // process is gone → restart now
	healthRegistering                      // process up, still within post-start grace → assume ok
	healthEdgeDown                         // process up but edge unreachable → restart once confirmed
	healthOK                               // process up + edge reachable (or unprobeable) → healthy
)

// pickMetricsAddr reserves a free loopback address for cloudflared's --metrics server. Returns "" if
// none can be reserved, in which case the tunnel runs without a metrics endpoint and the watchdog
// falls back to pid-only liveness — i.e. exactly the pre-edge-probe behavior, so no regression.
func pickMetricsAddr() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return ""
	}
	addr := l.Addr().String()
	l.Close() //nolint:errcheck — cloudflared re-binds it; a lost race just fails the start, and Supervise retries
	return addr
}

// probeEdgeReady reports whether cloudflared's /ready says at least one edge connection is live. An
// unreachable metrics endpoint, or any non-200 answer, counts as NOT ready. Callers must gate on a
// non-empty metricsAddr before trusting a false result (see probeHealth).
func probeEdgeReady(metricsAddr string) bool {
	if metricsAddr == "" {
		return false
	}
	resp, err := edgeProbeClient.Get("http://" + metricsAddr + "/ready")
	if err != nil {
		return false
	}
	defer resp.Body.Close() //nolint:errcheck
	return resp.StatusCode == http.StatusOK
}

// cloudflaredQuickArgs builds the quick-tunnel argv. --metrics is a `tunnel`-level flag → it goes
// right after "tunnel", before any positional.
func cloudflaredQuickArgs(metricsAddr, localAddr string) []string {
	args := []string{"tunnel"}
	if metricsAddr != "" {
		args = append(args, "--metrics", metricsAddr)
	}
	return append(args, "--url", localAddr)
}

// cloudflaredNamedRunArgs builds the named-tunnel run argv.
//
// CRITICAL ORDERING: --metrics is a flag of the PARENT `tunnel` command, NOT of the `run` subcommand.
// It MUST come BEFORE "run" — `cloudflared tunnel --metrics ADDR run …`. Placed after "run",
// cloudflared aborts at parse time with "flag provided but not defined: -metrics" and never connects.
// The test fake ignores argv, so only TestCloudflaredArgOrder guards this — do not reorder blindly.
func cloudflaredNamedRunArgs(metricsAddr, credFile, localAddr, name string) []string {
	args := []string{"tunnel"}
	if metricsAddr != "" {
		args = append(args, "--metrics", metricsAddr)
	}
	return append(args, "run", "--protocol", "http2", "--cred-file", credFile, "--url", localAddr, name)
}

// probeHealth is the watchdog's single health verdict for the currently-running tunnel — the one
// place pid-liveness and edge-reachability are combined, so the supervise loop stays declarative.
func (t *Tunnel) probeHealth(now time.Time) healthVerdict {
	t.stateMu.RLock()
	running := t.running && pidAlive(t.pid)
	metricsAddr := t.metricsAddr
	started := t.startedAt
	probe := t.edgeProbe
	t.stateMu.RUnlock()

	if !running {
		return healthDead
	}
	if metricsAddr == "" || probe == nil {
		return healthOK // unprobeable (no metrics server, e.g. an adopted legacy tunnel) — pid is all we have
	}
	if now.Sub(started) < edgeGrace {
		return healthRegistering // still coming up; an edge connection is not expected yet
	}
	if probe(metricsAddr) {
		return healthOK
	}
	return healthEdgeDown
}
