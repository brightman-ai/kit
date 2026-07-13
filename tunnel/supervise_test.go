//go:build linux

package tunnel

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSuperviseRestartsDeadTunnel is the whole point of the watchdog: cloudflared dies on its own
// (crash / OOM / edge kill) and the tunnel comes back WITHOUT a human. Regression guard for the
// production incident where the tunnel died overnight and stayed dead until the user noticed.
func TestSuperviseRestartsDeadTunnel(t *testing.T) {
	dir := t.TempDir()
	const url = "https://fake-heal.trycloudflare.com"
	writeFakeCloudflared(t, dir, url)

	tun := New(dir)
	if _, err := tun.Start(context.Background(), "http://127.0.0.1:9999"); err != nil {
		t.Fatalf("initial start: %v", err)
	}
	firstPID := tun.pid
	if firstPID <= 0 || !tun.IsRunning() {
		t.Fatalf("tunnel not running after Start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tun.SuperviseEvery(ctx, 100*time.Millisecond)

	killPID(firstPID) // cloudflared dies under us
	waitFor(t, 2*time.Second, "tunnel to report dead", func() bool { return !tun.IsRunning() })

	// The watchdog must bring it back on its own — new PID, tunnel live again.
	waitFor(t, 10*time.Second, "watchdog to restart the tunnel", func() bool { return tun.IsRunning() })

	newPID := tun.pid
	if newPID == firstPID {
		t.Fatalf("expected a fresh cloudflared PID after self-heal, got the dead one (%d)", firstPID)
	}
	if got := tun.PublicURL(); got != url {
		t.Fatalf("public URL after self-heal = %q, want %q", got, url)
	}
	tun.Stop()
}

// TestSuperviseNeverResurrectsStoppedTunnel is the safety half: a tunnel the USER turned off must
// stay off. Intent — not the presence of a dead process record — is what the watchdog acts on.
func TestSuperviseNeverResurrectsStoppedTunnel(t *testing.T) {
	dir := t.TempDir()
	writeFakeCloudflared(t, dir, "https://fake-stop.trycloudflare.com")

	tun := New(dir)
	if _, err := tun.Start(context.Background(), "http://127.0.0.1:9999"); err != nil {
		t.Fatalf("initial start: %v", err)
	}
	tun.Stop()

	if _, ok := tun.loadIntent(); ok {
		t.Fatalf("Stop() must clear intent; a stopped tunnel is not a tunnel to heal")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tun.SuperviseEvery(ctx, 50*time.Millisecond)

	time.Sleep(400 * time.Millisecond) // several ticks
	if tun.IsRunning() {
		t.Fatalf("watchdog resurrected a tunnel the user explicitly stopped")
	}
}

// TestSuperviseRevivesTunnelDeadBeforeHostStart covers the deploy case: the host process restarts
// (rebuild/redeploy) while cloudflared is already dead. New() drops the stale record, but intent
// survives on disk — so the watchdog re-establishes the tunnel instead of leaving it down.
func TestSuperviseRevivesTunnelDeadBeforeHostStart(t *testing.T) {
	dir := t.TempDir()
	const url = "https://fake-revive.trycloudflare.com"
	writeFakeCloudflared(t, dir, url)

	first := New(dir)
	if _, err := first.Start(context.Background(), "http://127.0.0.1:9999"); err != nil {
		t.Fatalf("initial start: %v", err)
	}
	deadPID := first.pid
	killPID(deadPID) // daemon dies while no host is watching
	// SIGKILL is async and the corpse lingers until reaped — wait for the pid to really be gone,
	// otherwise the liveness probe legitimately still sees it.
	waitFor(t, 2*time.Second, "killed cloudflared to be reaped", func() bool { return !pidAlive(deadPID) })

	next := New(dir) // fresh host process: stale state dropped...
	if next.IsRunning() {
		t.Fatalf("a dead tunnel must not be reported as running")
	}
	if _, ok := next.loadIntent(); !ok { // ...but the user's intent must outlive the dead daemon
		t.Fatalf("intent must survive a dead cloudflared + host restart")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go next.SuperviseEvery(ctx, 100*time.Millisecond)

	waitFor(t, 10*time.Second, "watchdog to revive the tunnel after host restart",
		func() bool { return next.IsRunning() })
	next.Stop()
}

// TestIntentBackfilledFromLiveTunnel covers the migration path: a tunnel started by an OLD binary
// (no intent file) must come under supervision when the new binary adopts it — no user action.
func TestIntentBackfilledFromLiveTunnel(t *testing.T) {
	dir := t.TempDir()
	writeFakeCloudflared(t, dir, "https://fake-migrate.trycloudflare.com")

	old := New(dir)
	if _, err := old.Start(context.Background(), "http://127.0.0.1:9999"); err != nil {
		t.Fatalf("initial start: %v", err)
	}
	os.Remove(filepath.Join(dir, "tunnel-intent.json")) //nolint:errcheck — simulate the old binary
	defer old.Stop()

	next := New(dir) // new binary adopts the still-live tunnel
	if !next.IsRunning() {
		t.Fatalf("expected the live tunnel to be adopted")
	}
	in, ok := next.loadIntent()
	if !ok {
		t.Fatalf("adopting a live tunnel must back-fill intent, else it is never supervised")
	}
	if in.Mode != "quick" || in.LocalAddr != "http://127.0.0.1:9999" {
		t.Fatalf("back-filled intent = %+v, want quick tunnel on the adopted addr", in)
	}
}

// TestLoadIntentRejectsGarbage: a corrupt/partial intent record must read as "no intent" rather
// than driving the watchdog to start a tunnel with a missing hostname or addr.
func TestLoadIntentRejectsGarbage(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty addr", `{"mode":"quick"}`},
		{"unknown mode", `{"mode":"weird","localAddr":"http://x"}`},
		{"named without hostname", `{"mode":"named","localAddr":"http://x"}`},
		{"not json", `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tun := New(dir)
			if err := os.WriteFile(tun.intentPath, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, ok := tun.loadIntent(); ok {
				t.Fatalf("loadIntent accepted a malformed record: %s", tc.body)
			}
		})
	}
}
