//go:build linux

package tunnel

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeFakeCloudflared writes a stand-in cloudflared at <dataDir>/bin/cloudflared that prints a
// trycloudflare URL to stderr and then sleeps, so the tunnel manager treats it as a live tunnel.
// It is padded past minCloudflaredSize so ensureBinary accepts it without a network download.
func writeFakeCloudflared(t *testing.T, dataDir, url string) {
	t.Helper()
	binDir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/bash\n" +
		"echo \"$(date) INF |  " + url + "  |\" 1>&2\n" +
		"sleep 300\n" +
		"# padding so the file exceeds minCloudflaredSize; never parsed (bash blocks on sleep):\n#"
	body := append([]byte(script), bytes.Repeat([]byte("x"), minCloudflaredSize+1024)...)
	bin := filepath.Join(binDir, "cloudflared")
	if err := os.WriteFile(bin, body, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestStartDetachedAndPersist covers REQ-TUN-03/04: a freshly started tunnel runs detached
// (its own session via Setsid) and its url+pid+localAddr are persisted to tunnel.json.
func TestStartDetachedAndPersist(t *testing.T) {
	dir := t.TempDir()
	const url = "https://fake-detach.trycloudflare.com"
	writeFakeCloudflared(t, dir, url)

	tn := New(dir)
	got, err := tn.Start(context.Background(), "http://localhost:9991")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tn.Stop()

	if got != url {
		t.Fatalf("url = %q, want %q", got, url)
	}
	if !tn.IsRunning() {
		t.Fatal("IsRunning() = false after Start")
	}
	if s := tn.Status(); !s.Running || s.PublicURL != url {
		t.Fatalf("Status = %+v, want running with url", s)
	}

	// Persisted record present, pid alive, addr matches.
	st, ok := tn.loadState()
	if !ok {
		t.Fatal("tunnel.json not persisted")
	}
	if st.PublicURL != url || st.LocalAddr != "http://localhost:9991" || !pidAlive(st.PID) {
		t.Fatalf("persisted state wrong: %+v", st)
	}

	// Detached: the child is its own session leader (Setsid), distinct from the test's session.
	childSid := procSession(t, st.PID)
	ourSid := procSession(t, os.Getpid())
	if childSid == ourSid {
		t.Fatalf("child session %d == test session %d — NOT detached (Setsid missing)", childSid, ourSid)
	}
	if childSid != st.PID {
		t.Fatalf("child sid %d != child pid %d — child is not a session leader", childSid, st.PID)
	}
}

// procSession reads the session id (field 6) from /proc/<pid>/stat.
func procSession(t *testing.T, pid int) int {
	t.Helper()
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatalf("read /proc/%d/stat: %v", pid, err)
	}
	s := string(data)
	// comm (field 2) is parenthesized and may contain spaces — split after the last ')'.
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		t.Fatalf("bad stat: %q", s)
	}
	fields := strings.Fields(s[i+1:]) // [0]=state [1]=ppid [2]=pgrp [3]=session
	if len(fields) < 4 {
		t.Fatalf("stat too short: %q", s)
	}
	sid, err := strconv.Atoi(fields[3])
	if err != nil {
		t.Fatalf("parse session: %v", err)
	}
	return sid
}

// TestAdoptAcrossRestart covers REQ-TUN-02/05: a brand-new manager (simulating a restarted host
// server) adopts the still-running detached tunnel instead of spawning a duplicate.
func TestAdoptAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	const url = "https://fake-adopt.trycloudflare.com"
	const addr = "http://localhost:9992"
	writeFakeCloudflared(t, dir, url)

	first := New(dir)
	if _, err := first.Start(context.Background(), addr); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	st1, _ := first.loadState()
	defer func() { New(dir).Stop() }() // ensure the daemon is reaped at end

	// A fresh manager, as if the host restarted: must adopt on construction, no Start needed.
	second := New(dir)
	if !second.IsRunning() {
		t.Fatal("second manager did not adopt running tunnel on New")
	}
	if second.PublicURL() != url {
		t.Fatalf("adopted url = %q, want %q", second.PublicURL(), url)
	}

	// Start on the adopted manager reuses the SAME process (no new pid).
	got, err := second.Start(context.Background(), addr)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if got != url {
		t.Fatalf("reused url = %q, want %q", got, url)
	}
	st2, _ := second.loadState()
	if st2.PID != st1.PID {
		t.Fatalf("pid changed on adopt: was %d now %d — spawned a duplicate", st1.PID, st2.PID)
	}
}

// TestStaleCleanup covers REQ-TUN-06: a persisted record whose process is dead is treated as
// not-running and cleaned up, rather than surfaced as a live (but dead) URL.
func TestStaleCleanup(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "tunnel.json")

	// A pid that is essentially guaranteed dead.
	deadPID := findDeadPID(t)
	rec := `{"publicURL":"https://stale.trycloudflare.com","pid":` + strconv.Itoa(deadPID) + `,"localAddr":"http://localhost:9993"}`
	if err := os.WriteFile(statePath, []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}

	tn := New(dir)
	if tn.IsRunning() {
		t.Fatal("IsRunning() = true for a dead persisted pid")
	}
	if s := tn.Status(); s.Running || s.PublicURL != "" {
		t.Fatalf("Status surfaced a stale tunnel: %+v", s)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("stale tunnel.json not removed (err=%v)", err)
	}
}

// findDeadPID returns a pid that is not currently alive.
func findDeadPID(t *testing.T) int {
	t.Helper()
	for pid := 4_000_000; pid > 100; pid -= 99_991 {
		if !pidAlive(pid) {
			return pid
		}
	}
	t.Fatal("could not find a dead pid")
	return 0
}
