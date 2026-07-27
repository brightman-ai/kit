//go:build linux

package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func hostport(httptestURL string) string { return strings.TrimPrefix(httptestURL, "http://") }

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

// TestCloudflaredArgOrder guards the parse-time ordering the test fake CANNOT catch (it ignores argv):
// --metrics is a `tunnel`-level flag and MUST precede the `run` subcommand / the --url positional,
// else real cloudflared aborts with "flag provided but not defined: -metrics" and never connects.
func TestCloudflaredArgOrder(t *testing.T) {
	named := cloudflaredNamedRunArgs("127.0.0.1:5000", "/cred.json", "http://127.0.0.1:8090", "mytun")
	mi, ri := indexOf(named, "--metrics"), indexOf(named, "run")
	if mi < 0 || ri < 0 || mi > ri {
		t.Fatalf("named: --metrics must come BEFORE 'run', got %v", named)
	}
	if named[mi+1] != "127.0.0.1:5000" {
		t.Fatalf("named: --metrics value must follow the flag, got %v", named)
	}
	if named[len(named)-1] != "mytun" { // the tunnel name must stay the trailing positional
		t.Fatalf("named: tunnel name must be the last arg, got %v", named)
	}

	quick := cloudflaredQuickArgs("127.0.0.1:5000", "http://127.0.0.1:8090")
	if qi, ui := indexOf(quick, "--metrics"), indexOf(quick, "--url"); qi < 0 || ui < 0 || qi > ui {
		t.Fatalf("quick: --metrics must precede --url, got %v", quick)
	}

	// Empty metricsAddr → the flag is omitted entirely (pid-only fallback, no stray "--metrics").
	if indexOf(cloudflaredNamedRunArgs("", "/c", "u", "n"), "--metrics") >= 0 {
		t.Fatal("empty metricsAddr must omit --metrics (named)")
	}
	if indexOf(cloudflaredQuickArgs("", "u"), "--metrics") >= 0 {
		t.Fatal("empty metricsAddr must omit --metrics (quick)")
	}
}

// setEdge overrides a running tunnel's edge-health inputs for a test: the grace anchor and the probe.
func setEdge(tun *Tunnel, started time.Time, probe func(string) bool) {
	tun.stateMu.Lock()
	tun.startedAt = started
	tun.edgeProbe = probe
	tun.stateMu.Unlock()
}

// TestProbeEdgeReady: cloudflared /ready == 200 → edge is up; 503 / unreachable / empty → NOT up.
// This is the signal the whole edge-aware self-heal rests on.
func TestProbeEdgeReady(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer up.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // cloudflared answers 503 with 0 connections
	}))
	defer down.Close()

	if !probeEdgeReady(hostport(up.URL)) {
		t.Fatal("200 /ready must read as edge-ready")
	}
	if probeEdgeReady(hostport(down.URL)) {
		t.Fatal("503 /ready must read as NOT ready (0 edge connections)")
	}
	if probeEdgeReady("127.0.0.1:1") {
		t.Fatal("an unreachable metrics endpoint must read as NOT ready")
	}
	if probeEdgeReady("") {
		t.Fatal("an empty metrics addr must read as NOT ready")
	}
}

// TestProbeHealthVerdicts pins the single place pid-liveness and edge-reachability combine.
func TestProbeHealthVerdicts(t *testing.T) {
	now := time.Now()
	mk := func(metrics string, started time.Time, probe func(string) bool) *Tunnel {
		return &Tunnel{running: true, pid: os.Getpid(), metricsAddr: metrics, startedAt: started, edgeProbe: probe}
	}
	past := now.Add(-2 * edgeGrace)

	if v := (&Tunnel{running: false}).probeHealth(now); v != healthDead {
		t.Fatalf("dead process → healthDead, got %v", v)
	}
	if v := mk("", now, nil).probeHealth(now); v != healthOK {
		t.Fatalf("no metrics server → unprobeable → healthOK, got %v", v)
	}
	if v := mk("127.0.0.1:1", now, func(string) bool { return false }).probeHealth(now); v != healthRegistering {
		t.Fatalf("within grace → healthRegistering (edge not expected yet), got %v", v)
	}
	if v := mk("127.0.0.1:1", past, func(string) bool { return false }).probeHealth(now); v != healthEdgeDown {
		t.Fatalf("past grace + /ready down → healthEdgeDown, got %v", v)
	}
	if v := mk("127.0.0.1:1", past, func(string) bool { return true }).probeHealth(now); v != healthOK {
		t.Fatalf("past grace + /ready up → healthOK, got %v", v)
	}
}

// TestSuperviseRestartsEdgeDeadTunnel (acceptance T1): the production 1033 shape — cloudflared's PID
// stays ALIVE but its edge connection is gone (/ready fails). A pid-only watchdog is blind to this and
// leaves the tunnel dark; the edge-aware watchdog must tear down the stale process and re-establish on
// a fresh PID, with no human. Regression guard for the incident this whole change exists to fix.
func TestSuperviseRestartsEdgeDeadTunnel(t *testing.T) {
	dir := t.TempDir()
	const url = "https://fake-edge.trycloudflare.com"
	writeFakeCloudflared(t, dir, url)

	tun := New(dir)
	if _, err := tun.Start(context.Background(), "http://127.0.0.1:9999"); err != nil {
		t.Fatalf("initial start: %v", err)
	}
	firstPID := tun.pid
	if firstPID <= 0 || !tun.IsRunning() {
		t.Fatalf("tunnel not running after Start")
	}
	if tun.metricsAddr == "" {
		t.Fatalf("spawn must reserve a --metrics addr so the edge is probeable")
	}

	// pid stays alive; the edge is gone. Back-date startedAt so the post-start grace is already spent.
	var edgeUp atomic.Bool // stays false: /ready down
	setEdge(tun, time.Now().Add(-2*edgeGrace), func(string) bool { return edgeUp.Load() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tun.SuperviseEvery(ctx, 50*time.Millisecond)

	waitFor(t, 10*time.Second, "watchdog to re-establish the edge-dead tunnel on a fresh pid", func() bool {
		return tun.IsRunning() && tun.pid != firstPID
	})
	if pidAlive(firstPID) {
		t.Fatalf("the stale edge-dead cloudflared (pid %d) must be torn down, not left running", firstPID)
	}
	tun.Stop()
}

// TestSuperviseTrustsReadyOverStaleLog (acceptance T2): a healthy long-lived tunnel whose "Registered"
// lines have scrolled out of the 64 KB log tail reads countReadyFromLog()==0. The watchdog must trust
// the live /ready probe (up) and NOT restart it — else the fix would be worse than the bug, churning
// perfectly healthy tunnels.
func TestSuperviseTrustsReadyOverStaleLog(t *testing.T) {
	dir := t.TempDir()
	writeFakeCloudflared(t, dir, "https://fake-ready.trycloudflare.com")

	tun := New(dir)
	if _, err := tun.Start(context.Background(), "http://127.0.0.1:9999"); err != nil {
		t.Fatalf("initial start: %v", err)
	}
	firstPID := tun.pid
	// Edge IS reachable, even though the fake never logs "Registered" (log-count would say 0).
	setEdge(tun, time.Now().Add(-2*edgeGrace), func(string) bool { return true })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tun.SuperviseEvery(ctx, 50*time.Millisecond)

	time.Sleep(600 * time.Millisecond) // many ticks, all past grace
	if !tun.IsRunning() || tun.pid != firstPID {
		t.Fatalf("a tunnel with healthy /ready must not be restarted (log-count 0 is not a death)")
	}
	tun.Stop()
}

// TestSuperviseDebouncesEdgeBlip (acceptance T6): a transient edge blip (fewer than the threshold of
// CONSECUTIVE misses) must not trigger a disruptive restart. Only a sustained miss is a real death.
func TestSuperviseDebouncesEdgeBlip(t *testing.T) {
	dir := t.TempDir()
	writeFakeCloudflared(t, dir, "https://fake-blip.trycloudflare.com")

	tun := New(dir)
	if _, err := tun.Start(context.Background(), "http://127.0.0.1:9999"); err != nil {
		t.Fatalf("initial start: %v", err)
	}
	firstPID := tun.pid
	// First (threshold-1) probes miss, then /ready recovers — a blip, not a death.
	var calls atomic.Int32
	setEdge(tun, time.Now().Add(-2*edgeGrace), func(string) bool {
		return calls.Add(1) > int32(edgeFailThreshold-1)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tun.SuperviseEvery(ctx, 30*time.Millisecond)

	time.Sleep(600 * time.Millisecond)
	if !tun.IsRunning() || tun.pid != firstPID {
		t.Fatalf("a transient blip (< %d consecutive misses) must not restart the tunnel", edgeFailThreshold)
	}
	tun.Stop()
}
