// Package tunnel manages a Cloudflare quick-tunnel (cloudflared) process: it lazily downloads the
// cloudflared binary, starts/stops a quick tunnel exposing an arbitrary local addr, and reports
// download progress + the public URL. It is generic infrastructure — exposing "any local server"
// to the internet — so it lives in the shared kit (github.com/brightman-ai/kit), consumed equally
// by deepwork-terminal and deepwork-pro. Neither owns it; the SSOT is here.
//
// Lifecycle: the tunnel is DECOUPLED from the host server's process lifecycle. cloudflared is
// launched detached (own session via Setsid, stderr → a log file rather than a parent-owned pipe),
// so restarting/rebuilding the host server does NOT kill the tunnel and does NOT change the public
// URL. State (url+pid+localAddr) is persisted to <dataDir>/tunnel.json; on construction or Start
// the manager adopts an already-running cloudflared (matching pid alive + localAddr) instead of
// spawning a duplicate. Per-dataDir state keeps terminal and pro independent. Only an explicit
// Stop() (or a dead pid) tears the tunnel down.
package tunnel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// State is the complete observable state of a Tunnel.
// Safe to read at any time — never blocks on a download in progress.
type State struct {
	Running         bool   `json:"running"`
	PublicURL       string `json:"publicURL,omitempty"`
	Downloading     bool   `json:"downloading"`
	DownloadedBytes int64  `json:"downloadedBytes,omitempty"`
	TotalBytes      int64  `json:"totalBytes,omitempty"` // -1 if unknown
	DownloadURL     string `json:"downloadURL,omitempty"`
	BinPath         string `json:"binPath,omitempty"`
}

// Tunnel manages a cloudflared quick-tunnel process.
//
// Locking discipline:
//
//	startMu — held for the full duration of Start/Stop; prevents concurrent starts.
//	          NOT held during status reads.
//	stateMu — RWMutex guarding running/publicURL/cmd/downloadURL.
//	          Never held while doing I/O (download, process wait).
//	downloadedBytes / totalBytes / downloading — atomic; zero-lock reads.
type Tunnel struct {
	startMu sync.Mutex
	stateMu sync.RWMutex

	running   bool
	publicURL string
	pid       int      // cloudflared PID (spawned or adopted); 0 = none
	localAddr string   // local addr the running tunnel forwards to
	cmd       *exec.Cmd // set only for a process WE spawned this run (for reaping); nil when adopted

	// Download progress — written under startMu, read via atomics from any goroutine.
	downloading     atomic.Bool
	downloadedBytes atomic.Int64
	totalBytes      atomic.Int64 // -1 means Content-Length unknown
	downloadURL     atomic.Value // stores string

	binPath   string
	statePath string // <dataDir>/tunnel.json — persisted url+pid+localAddr
	logPath   string // <dataDir>/tunnel.log  — detached cloudflared stderr/stdout
}

// persistedState is the on-disk record that lets a fresh host process adopt an
// already-running, detached cloudflared instead of spawning a duplicate.
type persistedState struct {
	PublicURL string `json:"publicURL"`
	PID       int    `json:"pid"`
	LocalAddr string `json:"localAddr"`
	LogPath   string `json:"logPath,omitempty"`
}

// New creates a tunnel manager. dataDir is where cloudflared is cached AND where the
// tunnel state/log live. If a previously-started tunnel is still alive (its PID responds),
// New adopts it so Status()/IsRunning() report the running tunnel immediately — without the
// host server having to re-Start it. A stale record (dead PID) is cleaned up.
func New(dataDir string) *Tunnel {
	binPath := filepath.Join(dataDir, "bin", "cloudflared")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	t := &Tunnel{
		binPath:   binPath,
		statePath: filepath.Join(dataDir, "tunnel.json"),
		logPath:   filepath.Join(dataDir, "tunnel.log"),
	}
	if st, ok := t.loadState(); ok {
		if pidAlive(st.PID) {
			t.running = true
			t.publicURL = st.PublicURL
			t.pid = st.PID
			t.localAddr = st.LocalAddr
		} else {
			t.removeState() // stale: the daemon died while no host was running
		}
	}
	return t
}

// Status returns a consistent snapshot of tunnel state without blocking.
// Running is liveness-checked: an adopted/spawned tunnel whose PID has since died is
// reported as not-running (a cheap signal-0 probe), so the UI never shows a dead URL.
func (t *Tunnel) Status() State {
	t.stateMu.RLock()
	running := t.running && pidAlive(t.pid)
	s := State{
		Running:   running,
		PublicURL: t.publicURL,
	}
	if !running {
		s.PublicURL = ""
	}
	t.stateMu.RUnlock()

	s.Downloading = t.downloading.Load()
	s.DownloadedBytes = t.downloadedBytes.Load()
	s.TotalBytes = t.totalBytes.Load()
	if u, ok := t.downloadURL.Load().(string); ok {
		s.DownloadURL = u
	}
	s.BinPath = t.binPath
	return s
}

// IsRunning returns whether the tunnel is active (liveness-checked).
func (t *Tunnel) IsRunning() bool {
	t.stateMu.RLock()
	defer t.stateMu.RUnlock()
	return t.running && pidAlive(t.pid)
}

// PublicURL returns the current public tunnel URL.
func (t *Tunnel) PublicURL() string {
	t.stateMu.RLock()
	defer t.stateMu.RUnlock()
	return t.publicURL
}

// Start ensures the cloudflared binary is present, then returns a quick-tunnel URL,
// adopting an already-running detached cloudflared when possible instead of spawning a
// duplicate. Resolution order:
//  1. In-memory running tunnel that is alive and forwards to localAddr → reuse.
//  2. Persisted tunnel that is alive and forwards to localAddr → adopt (covers a fresh
//     host process that didn't adopt at New, or a tunnel started by a prior host).
//  3. Otherwise spawn a NEW detached cloudflared, persist it, and return its URL.
//
// A persisted tunnel that is dead (stale) or points at a different localAddr is cleaned up
// (and killed, if it points elsewhere) before spawning. Concurrent calls are serialized.
func (t *Tunnel) Start(ctx context.Context, localAddr string) (string, error) {
	t.startMu.Lock()
	defer t.startMu.Unlock()

	// 1) In-memory running + alive + same addr → reuse.
	t.stateMu.RLock()
	running, url, pid, addr := t.running, t.publicURL, t.pid, t.localAddr
	t.stateMu.RUnlock()
	if running && addr == localAddr && pidAlive(pid) {
		return url, nil
	}
	if running && !pidAlive(pid) {
		// adopted/spawned earlier but the daemon has since died → clear before re-spawning.
		t.setState(false, "", 0, "", nil)
		t.removeState()
	}

	// 2) Persisted tunnel from a prior host process.
	if st, ok := t.loadState(); ok {
		switch {
		case pidAlive(st.PID) && st.LocalAddr == localAddr:
			t.setState(true, st.PublicURL, st.PID, st.LocalAddr, nil)
			return st.PublicURL, nil
		case pidAlive(st.PID) && st.LocalAddr != localAddr:
			killPID(st.PID) // points at a different local addr — replace it
		}
		t.removeState()
	}

	// 3) Spawn a fresh detached tunnel.
	// Download phase: startMu held, stateMu NOT held → status reads are unblocked.
	if err := t.ensureBinary(ctx); err != nil {
		return "", fmt.Errorf("cloudflared setup: %w", err)
	}

	// Detached launch: stderr/stdout → log file (NOT a parent-owned pipe, which would break
	// when the host dies) and Setsid so the process leaves the host's session/process group
	// and survives host restart and terminal signals. exec.Command (not CommandContext) so a
	// cancelled ctx never tears down the daemon.
	logf, err := os.Create(t.logPath)
	if err != nil {
		return "", fmt.Errorf("create tunnel log: %w", err)
	}
	cmd := exec.Command(t.binPath, "tunnel", "--url", localAddr) //nolint:gosec — binPath is ours
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		logf.Close() //nolint:errcheck
		return "", fmt.Errorf("start cloudflared: %w", err)
	}
	logf.Close() //nolint:errcheck — the child holds its own fd

	url, err = parseTunnelURLFromLog(t.logPath, cmd.Process.Pid, 45*time.Second)
	if err != nil {
		killPID(cmd.Process.Pid)
		cmd.Wait() //nolint:errcheck
		return "", fmt.Errorf("parse tunnel URL: %w", err)
	}

	// Reap the child if it dies while WE are still alive; if the host dies first, init reaps it.
	go cmd.Wait() //nolint:errcheck

	t.setState(true, url, cmd.Process.Pid, localAddr, cmd)
	t.saveState(persistedState{PublicURL: url, PID: cmd.Process.Pid, LocalAddr: localAddr, LogPath: t.logPath})

	return url, nil
}

// Stop kills the tunnel process (spawned this run OR adopted/persisted) and clears all state.
// The tunnel only stops on an explicit Stop — never implicitly when the host server exits.
func (t *Tunnel) Stop() {
	t.startMu.Lock()
	defer t.startMu.Unlock()

	t.stateMu.Lock()
	cmd := t.cmd
	pid := t.pid
	t.running = false
	t.publicURL = ""
	t.pid = 0
	t.localAddr = ""
	t.cmd = nil
	t.stateMu.Unlock()

	// Fall back to the persisted pid if this host process never adopted one in memory.
	if pid == 0 {
		if st, ok := t.loadState(); ok {
			pid = st.PID
		}
	}
	killPID(pid)
	if cmd != nil && cmd.Process != nil {
		cmd.Wait() //nolint:errcheck — reap our own child
	}
	t.removeState()
}

// setState atomically updates the live tunnel fields.
func (t *Tunnel) setState(running bool, url string, pid int, addr string, cmd *exec.Cmd) {
	t.stateMu.Lock()
	t.running = running
	t.publicURL = url
	t.pid = pid
	t.localAddr = addr
	t.cmd = cmd
	t.stateMu.Unlock()
}

// ── State persistence + process helpers ─────────────────────────────────────────

// loadState reads <dataDir>/tunnel.json. ok=false if absent or unparseable.
func (t *Tunnel) loadState() (persistedState, bool) {
	data, err := os.ReadFile(t.statePath)
	if err != nil {
		return persistedState{}, false
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil || st.PID <= 0 {
		return persistedState{}, false
	}
	return st, true
}

// saveState writes the record atomically (temp + rename, same dir).
func (t *Tunnel) saveState(st persistedState) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(t.statePath), 0755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(t.statePath), ".tunnel-*.json")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()        //nolint:errcheck
		os.Remove(tmpName) //nolint:errcheck
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName) //nolint:errcheck
		return
	}
	os.Rename(tmpName, t.statePath) //nolint:errcheck
}

func (t *Tunnel) removeState() {
	os.Remove(t.statePath) //nolint:errcheck
}

// pidAlive reports whether pid is a live process owned by us (signal-0 probe on unix).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// killPID sends SIGKILL to pid (best effort). A no-op for pid <= 0.
func killPID(pid int) {
	if pid <= 0 {
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		proc.Kill() //nolint:errcheck
	}
}

// parseTunnelURLFromLog polls the detached cloudflared's log file until the public URL
// appears, the process exits, or the timeout elapses. Used instead of reading a stderr pipe
// so the parent never owns a pipe that would break the tunnel on host restart.
func parseTunnelURLFromLog(path string, pid int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if data, err := os.ReadFile(path); err == nil {
			if m := tunnelURLRegex.FindString(string(data)); m != "" {
				return m, nil
			}
			if !pidAlive(pid) {
				if e := lastLogError(string(data)); e != "" {
					return "", fmt.Errorf("cloudflared: %s", e)
				}
				return "", fmt.Errorf("cloudflared exited without producing a tunnel URL")
			}
		} else if !pidAlive(pid) {
			return "", fmt.Errorf("cloudflared exited without producing a tunnel URL")
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for tunnel URL")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// lastLogError returns the last cloudflared error/fatal line, for diagnostics.
func lastLogError(log string) string {
	var last string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "level=error") || strings.Contains(line, "level=fatal") {
			last = line
		}
	}
	return last
}

// ── Binary management ─────────────────────────────────────────────────────────

// minCloudflaredSize is the minimum acceptable size for a cloudflared binary.
// The real binary is ~90 MB; anything smaller is a truncated/corrupt download.
const minCloudflaredSize = 10 * 1024 * 1024 // 10 MB

// ensureBinary downloads cloudflared if not present, not executable, or corrupt.
// Called with startMu held; must NOT hold stateMu (download may take minutes).
func (t *Tunnel) ensureBinary(ctx context.Context) error {
	if info, err := os.Stat(t.binPath); err == nil {
		if info.Size() >= minCloudflaredSize && info.Mode()&0111 != 0 {
			return nil
		}
		os.Remove(t.binPath) //nolint:errcheck
	}

	dlURL := cloudflaredDownloadURL()
	if dlURL == "" {
		return fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	t.downloadURL.Store(dlURL)

	if err := os.MkdirAll(filepath.Dir(t.binPath), 0755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download cloudflared: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download cloudflared: HTTP %d", resp.StatusCode)
	}

	t.totalBytes.Store(resp.ContentLength) // -1 if server omits Content-Length
	t.downloadedBytes.Store(0)
	t.downloading.Store(true)
	defer t.downloading.Store(false)

	body := &progressReader{
		r:    resp.Body,
		read: func(n int64) { t.downloadedBytes.Add(n) },
	}

	if strings.HasSuffix(dlURL, ".tgz") {
		return extractTgz(body, t.binPath)
	}
	return writeBinaryAtomic(body, t.binPath)
}

func cloudflaredDownloadURL() string {
	base := "https://github.com/cloudflare/cloudflared/releases/latest/download/"
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return base + "cloudflared-darwin-arm64.tgz"
		}
		return base + "cloudflared-darwin-amd64.tgz"
	case "linux":
		if runtime.GOARCH == "arm64" {
			return base + "cloudflared-linux-arm64"
		}
		return base + "cloudflared-linux-amd64"
	case "windows":
		return base + "cloudflared-windows-amd64.exe"
	}
	return ""
}

// ── URL parsing ───────────────────────────────────────────────────────────────

var tunnelURLRegex = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)

// ── I/O helpers ───────────────────────────────────────────────────────────────

// progressReader wraps an io.Reader and calls read(n) after each chunk.
type progressReader struct {
	r    io.Reader
	read func(int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.read(int64(n))
	}
	return n, err
}

// writeBinaryAtomic streams r into a temp file in the same directory as
// targetPath, sets 0755, then renames atomically.
// Temp files stay in the target directory — never in /tmp.
func writeBinaryAtomic(r io.Reader, targetPath string) error {
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, ".cloudflared-download-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck — no-op after successful rename

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return fmt.Errorf("write cloudflared: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0755); err != nil {
		return err
	}
	return os.Rename(tmpName, targetPath)
}

// extractTgz extracts the cloudflared binary from a .tgz archive atomically.
func extractTgz(r io.Reader, targetPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) == "cloudflared" {
			return writeBinaryAtomic(tr, targetPath)
		}
	}
	return fmt.Errorf("cloudflared binary not found in archive")
}
