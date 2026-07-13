// Named-tunnel mode: a persistent Cloudflare *named* tunnel bound to a user-supplied hostname
// (e.g. sub.example.com), as opposed to the ephemeral quick tunnel in tunnel.go.
//
// Why it exists: a quick tunnel's `*.trycloudflare.com` URL is random and dies on any cloudflared
// restart; a named tunnel keeps a fixed hostname the user configures once. The hostname is NEVER
// hardcoded — the host UI collects it and passes it to StartNamed.
//
// Cloudflare state (login cert + tunnel credentials) lives under <dataDir>/cloudflared so each
// host (pro/terminal/teamworkbench) keeps an independent account — never the global ~/.cloudflared.
// All cloudflared invocations get TUNNEL_ORIGIN_CERT pointed at that per-dataDir cert.
//
// Transport is forced to http2 (--protocol http2): the quick tunnel's QUIC/UDP path to the edge
// proved unreliable in the field ("no recent network activity" timeouts); http2 over TCP is the
// robust fallback and connected cleanly where QUIC did not.
package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	// The Cloudflare authorization URL printed by `cloudflared tunnel login`.
	loginURLRegex = regexp.MustCompile(`https://dash\.cloudflare\.com/argotunnel\S*`)
	// The tunnel UUID printed by `cloudflared tunnel create` ("… with id <uuid>").
	tunnelIDRegex = regexp.MustCompile(`with id ([0-9a-fA-F-]{36})`)
	// Non-alphanumeric runs, collapsed to '-' when deriving a tunnel name from a hostname.
	nonAlnumRegex = regexp.MustCompile(`[^a-z0-9]+`)
)

// Login runs `cloudflared tunnel login` (detached) and surfaces the Cloudflare authorization URL
// via Status().LoginURL / LoginState. It returns as soon as the URL is available (or an error);
// a background watcher flips LoginState to "done" once the user authorizes and cert.pem lands.
// Idempotent: a no-op ("done") if already logged in, and coalesces a concurrent pending login.
func (t *Tunnel) Login(ctx context.Context) error {
	if t.hasAccount() {
		t.setLogin("done", "")
		return nil
	}
	t.stateMu.RLock()
	pending := t.loginState == "pending"
	t.stateMu.RUnlock()
	if pending {
		return nil
	}

	t.startMu.Lock()
	if t.hasAccount() {
		t.startMu.Unlock()
		t.setLogin("done", "")
		return nil
	}
	if err := t.ensureBinary(ctx); err != nil {
		t.startMu.Unlock()
		return fmt.Errorf("cloudflared setup: %w", err)
	}
	if err := os.MkdirAll(t.cfHome, 0700); err != nil {
		t.startMu.Unlock()
		return fmt.Errorf("create cloudflared dir: %w", err)
	}
	logf, err := os.Create(t.loginLog)
	if err != nil {
		t.startMu.Unlock()
		return fmt.Errorf("create login log: %w", err)
	}
	cmd := exec.Command(t.binPath, "tunnel", "login") //nolint:gosec — binPath is ours
	cmd.Env = append(tunnelChildEnv(), "TUNNEL_ORIGIN_CERT="+t.certPath, "NO_PROXY=*", "no_proxy=*")
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		logf.Close() //nolint:errcheck
		t.startMu.Unlock()
		return fmt.Errorf("start cloudflared login: %w", err)
	}
	logf.Close() //nolint:errcheck — the child holds its own fd
	loginPID := cmd.Process.Pid
	go cmd.Wait() //nolint:errcheck
	t.setLogin("pending", "")
	t.startMu.Unlock()

	url, err := parseLoginURLFromLog(t.loginLog, loginPID, 40*time.Second)
	if err != nil {
		killPID(loginPID)
		t.setLogin("error", "")
		return err
	}
	t.setLogin("pending", url)

	// Wait (out of band) for the user to authorize in a browser: cloudflared writes cert.pem and
	// exits on success. Check cert-first so a same-instant exit+write still resolves to "done".
	go func() {
		deadline := time.Now().Add(5 * time.Minute)
		for {
			if fileExists(t.certPath) {
				t.setLogin("done", "")
				return
			}
			if !pidAlive(loginPID) {
				t.setLogin("error", "") // exited without a cert
				return
			}
			if time.Now().After(deadline) {
				killPID(loginPID)
				t.setLogin("error", "")
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
	return nil
}

// StartNamed brings up a named tunnel for hostname, forwarding the edge to localAddr. It ensures
// the cloudflared tunnel exists (create if needed), (re)points the hostname's DNS at it, and runs
// it detached over http2. Returns https://<hostname> once an edge connection registers. Reuses /
// adopts an already-running named tunnel for the same hostname+addr. Requires a prior Login.
func (t *Tunnel) StartNamed(ctx context.Context, hostname, localAddr string) (string, error) {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	if hostname == "" {
		return "", fmt.Errorf("hostname required")
	}

	t.startMu.Lock()
	defer t.startMu.Unlock()

	// 1) In-memory named tunnel already up for this hostname+addr → reuse.
	t.stateMu.RLock()
	running, url, pid, addr, mode, hn := t.running, t.publicURL, t.pid, t.localAddr, t.mode, t.hostname
	t.stateMu.RUnlock()
	if running && pidAlive(pid) && mode == "named" && hn == hostname && addr == localAddr {
		return url, nil
	}

	// 2) Persisted named tunnel (from a prior host process) for this hostname+addr → adopt.
	if st, ok := t.loadState(); ok {
		if pidAlive(st.PID) && st.Mode == "named" && st.Hostname == hostname && st.LocalAddr == localAddr {
			t.adoptPersisted(st)
			return st.PublicURL, nil
		}
		if pidAlive(st.PID) {
			killPID(st.PID) // a different tunnel (quick / other host / other addr) — replace it
		}
		t.removeState()
	}
	// A different in-memory tunnel is live → tear it down before starting the named one.
	if running && pidAlive(pid) {
		killPID(pid)
	}
	t.setState(false, "", 0, "", nil, "", "")

	if !t.hasAccount() {
		return "", fmt.Errorf("not logged in to Cloudflare — connect an account first")
	}
	if err := t.ensureBinary(ctx); err != nil {
		return "", fmt.Errorf("cloudflared setup: %w", err)
	}
	if err := os.MkdirAll(t.cfHome, 0700); err != nil {
		return "", fmt.Errorf("create cloudflared dir: %w", err)
	}

	name := "dw-" + sanitizeHostname(hostname)
	credFile, err := t.ensureNamedTunnel(ctx, name)
	if err != nil {
		return "", err
	}
	if err := t.routeDNS(ctx, name, hostname); err != nil {
		return "", err
	}

	logf, err := os.Create(t.logPath)
	if err != nil {
		return "", fmt.Errorf("create tunnel log: %w", err)
	}
	// Detached run, forced onto http2. NO_AUTOUPDATE so cloudflared never self-restarts under us.
	cmd := exec.Command(t.binPath, "tunnel", "run", //nolint:gosec — binPath is ours
		"--protocol", "http2", "--cred-file", credFile, "--url", localAddr, name)
	// cloudflared 的出站只有一个目的地: Cloudflare 边缘 (7844)。它**绝不该走 HTTP 代理** ——
	// 走了会把边缘连接塞进代理, 代理若不放行 7844 / 本身不可达, 就表现为"隧道一直 Starting 卡死"
	// (实测: 主机 HTTPS_PROXY 指向一个死代理时, cloudflared 直连边缘明明通, 却因继承该变量去走
	// 死代理而超时挂掉; 清空 proxy 后 31s 注册成功)。故给子进程剥掉全部 proxy 变量 + NO_PROXY=*。
	cmd.Env = append(tunnelChildEnv(),
		"TUNNEL_ORIGIN_CERT="+t.resolveCert(), "NO_AUTOUPDATE=true",
		"NO_PROXY=*", "no_proxy=*")
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		logf.Close() //nolint:errcheck
		return "", fmt.Errorf("start cloudflared: %w", err)
	}
	logf.Close() //nolint:errcheck

	if err := waitForRegistered(t.logPath, cmd.Process.Pid, 90*time.Second); err != nil {
		killPID(cmd.Process.Pid)
		cmd.Wait() //nolint:errcheck
		return "", fmt.Errorf("named tunnel did not connect: %w", err)
	}
	go cmd.Wait() //nolint:errcheck — reap if it dies while we live; init reaps if the host dies first

	url = "https://" + hostname
	t.setNamedRunning(url, cmd.Process.Pid, localAddr, cmd, hostname, name, credFile)
	t.saveState(persistedState{
		PublicURL: url, PID: cmd.Process.Pid, LocalAddr: localAddr, LogPath: t.logPath,
		Mode: "named", Hostname: hostname, TunnelName: name, CredFile: credFile,
	})
	t.saveIntent(intent{Mode: "named", Hostname: hostname, LocalAddr: localAddr})
	return url, nil
}

// ensureNamedTunnel returns the credentials-file path for a tunnel named `name`, creating it if
// absent. If the tunnel exists in the account but its credentials are not in this cfHome (e.g. a
// prior login elsewhere), it is force-deleted and recreated so fresh creds land locally.
func (t *Tunnel) ensureNamedTunnel(ctx context.Context, name string) (string, error) {
	if uuid := t.findTunnel(ctx, name); uuid != "" {
		cred := filepath.Join(t.credDir(), uuid+".json")
		if fileExists(cred) {
			return cred, nil
		}
		t.runCF(ctx, "tunnel", "delete", "--force", name) //nolint:errcheck — best effort, then recreate
	}

	out, err := t.runCFRetry(ctx, 3, "tunnel", "create", name)
	if err != nil && !strings.Contains(out, "already exists") {
		return "", fmt.Errorf("create tunnel: %s", firstCloudflaredError(out, err))
	}
	uuid := ""
	if m := tunnelIDRegex.FindStringSubmatch(out); len(m) == 2 {
		uuid = m[1]
	}
	if uuid == "" {
		uuid = t.findTunnel(ctx, name)
	}
	if uuid == "" {
		return "", fmt.Errorf("could not resolve tunnel id for %q", name)
	}
	cred := filepath.Join(t.credDir(), uuid+".json")
	if !fileExists(cred) {
		return "", fmt.Errorf("tunnel credentials not found at %s", cred)
	}
	return cred, nil
}

// routeDNS points hostname at the named tunnel, overwriting any existing record (idempotent).
func (t *Tunnel) routeDNS(ctx context.Context, name, hostname string) error {
	out, err := t.runCFRetry(ctx, 3, "tunnel", "route", "dns", "--overwrite-dns", name, hostname)
	if err != nil {
		return fmt.Errorf("route DNS for %s: %s", hostname, firstCloudflaredError(out, err))
	}
	return nil
}

// findTunnel returns the UUID of the account tunnel named `name`, or "" if none (or if the API
// stayed unreachable after retries — the caller then attempts an idempotent create).
func (t *Tunnel) findTunnel(ctx context.Context, name string) string {
	for i := 0; i < 3; i++ {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var stdout bytes.Buffer
		cmd := exec.CommandContext(cctx, t.binPath, "tunnel", "list", "--output", "json") //nolint:gosec
		cmd.Env = append(tunnelChildEnv(), "TUNNEL_ORIGIN_CERT="+t.resolveCert(), "NO_PROXY=*", "no_proxy=*")
		cmd.Stdout = &stdout
		err := cmd.Run()
		cancel()
		if err != nil {
			if i < 2 {
				time.Sleep(1500 * time.Millisecond)
				continue
			}
			return "" // transient API failure — treat as not-found; create is idempotent
		}
		var tuns []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(stdout.Bytes(), &tuns) != nil {
			return ""
		}
		for _, tn := range tuns {
			if tn.Name == name {
				return tn.ID
			}
		}
		return "" // listed OK, no such tunnel
	}
	return ""
}

// runCF runs a short-lived cloudflared subcommand with the per-dataDir origin cert, returning its
// combined output (cloudflared logs to stderr, so both are captured for parsing/diagnostics).
func (t *Tunnel) runCF(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, t.binPath, args...) //nolint:gosec — binPath is ours
	cmd.Env = append(tunnelChildEnv(), "TUNNEL_ORIGIN_CERT="+t.resolveCert(), "NO_PROXY=*", "no_proxy=*")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runCFRetry runs a cloudflared subcommand, retrying on transient failure. Calls to
// api.cloudflare.com (tunnel create / route dns) intermittently time out or EOF on a flaky network
// — a single blip should not fail the whole "enable" action. An "already exists" result is
// definitive (not transient) and returns immediately.
func (t *Tunnel) runCFRetry(ctx context.Context, attempts int, args ...string) (string, error) {
	var out string
	var err error
	for i := 0; i < attempts; i++ {
		if out, err = t.runCF(ctx, args...); err == nil || strings.Contains(out, "already exists") {
			return out, err
		}
		if i < attempts-1 {
			time.Sleep(1500 * time.Millisecond)
		}
	}
	return out, err
}

// ── live-state setters ──────────────────────────────────────────────────────────

func (t *Tunnel) setLogin(state, url string) {
	t.stateMu.Lock()
	t.loginState = state
	t.loginURL = url
	t.stateMu.Unlock()
}

func (t *Tunnel) setNamedRunning(url string, pid int, addr string, cmd *exec.Cmd, hostname, name, credFile string) {
	t.stateMu.Lock()
	t.running = true
	t.publicURL = url
	t.pid = pid
	t.localAddr = addr
	t.cmd = cmd
	t.mode = "named"
	t.hostname = hostname
	t.tunnelName = name
	t.credFile = credFile
	t.stateMu.Unlock()
}

// adoptPersisted reflects an already-running (spawned by a prior host) tunnel — quick or named —
// into live state without re-spawning. cmd is nil (we did not start it, so we do not reap it).
func (t *Tunnel) adoptPersisted(st persistedState) {
	mode := st.Mode
	if mode == "" {
		mode = "quick"
	}
	t.stateMu.Lock()
	t.running = true
	t.publicURL = st.PublicURL
	t.pid = st.PID
	t.localAddr = st.LocalAddr
	t.cmd = nil
	t.mode = mode
	t.hostname = st.Hostname
	t.tunnelName = st.TunnelName
	t.credFile = st.CredFile
	t.stateMu.Unlock()
	t.adoptIntentFromState(st) // adopting a live tunnel = the user wants it up → supervise it
}

// ── log/file helpers ──────────────────────────────────────────────────────────

// parseLoginURLFromLog polls the login log until the Cloudflare auth URL appears, the login
// process exits, or the timeout elapses.
func parseLoginURLFromLog(path string, pid int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if data, err := os.ReadFile(path); err == nil {
			if m := loginURLRegex.FindString(string(data)); m != "" {
				return m, nil
			}
		}
		if !pidAlive(pid) {
			return "", fmt.Errorf("cloudflared login exited before printing a URL")
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for login URL")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForRegistered polls the run log until at least one edge connection registers or the process
// exits. A DEAD cloudflared is a real failure (bad creds / deleted tunnel) and returns its error.
// A timeout while the process is still ALIVE is NOT a failure: edge registration can be slow when
// re-enabling right after a disconnect (the edge is still draining the previous connections), so we
// return optimistically — the tunnel keeps connecting in the background and Status().Ready reflects
// it via polling. This avoids a spurious "failed to start" on a tunnel that is merely slow.
func waitForRegistered(path string, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if data, err := os.ReadFile(path); err == nil {
			if strings.Contains(string(data), "Registered tunnel connection") {
				return nil
			}
			if !pidAlive(pid) {
				if e := lastLogError(string(data)); e != "" {
					return fmt.Errorf("%s", e)
				}
				return fmt.Errorf("cloudflared exited before connecting")
			}
		} else if !pidAlive(pid) {
			return fmt.Errorf("cloudflared exited before connecting")
		}
		if time.Now().After(deadline) {
			if pidAlive(pid) {
				return nil // still connecting — optimistic success, not a failure
			}
			return fmt.Errorf("cloudflared exited before connecting")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// countReadyFromLog approximates the current live edge-connection count from the tail of the run
// log (registered minus unregistered). Bounded read; good enough for a status badge.
func countReadyFromLog(path string) int {
	tail := tailFile(path, 64*1024)
	n := strings.Count(tail, "Registered tunnel connection") - strings.Count(tail, "Unregistered tunnel connection")
	if n < 0 {
		return 0
	}
	return n
}

// tailFile returns up to the last n bytes of a file (empty string on any error).
func tailFile(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	if off := fi.Size() - n; off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return ""
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	return string(b)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// resolveCert returns the Cloudflare origin cert to use: the per-dataDir cert if present, else the
// global ~/.cloudflared cert if present, else the per-dataDir path (the login write target). This
// reuses an existing global login and stays correct however cloudflared chose to write the cert.
func (t *Tunnel) resolveCert() string {
	if fileExists(t.certPath) {
		return t.certPath
	}
	if t.globalCert != "" && fileExists(t.globalCert) {
		return t.globalCert
	}
	return t.certPath
}

// hasAccount reports whether a Cloudflare login cert exists (per-dataDir or global fallback).
func (t *Tunnel) hasAccount() bool {
	return fileExists(t.certPath) || (t.globalCert != "" && fileExists(t.globalCert))
}

// credDir is where cloudflared writes/reads tunnel credentials JSON — the resolved cert's directory.
func (t *Tunnel) credDir() string {
	return filepath.Dir(t.resolveCert())
}

// sanitizeHostname turns a hostname into a cloudflared-safe tunnel-name segment.
func sanitizeHostname(h string) string {
	return strings.Trim(nonAlnumRegex.ReplaceAllString(strings.ToLower(h), "-"), "-")
}

// firstCloudflaredError extracts the most useful line from a failed cloudflared invocation:
// the last ERR/error line if present, else the raw wrapped error.
func firstCloudflaredError(out string, err error) string {
	if e := lastLogError(out); e != "" {
		return e
	}
	out = strings.TrimSpace(out)
	if out != "" {
		lines := strings.Split(out, "\n")
		return strings.TrimSpace(lines[len(lines)-1])
	}
	return err.Error()
}

// tunnelChildEnv — 父进程环境去掉全部 HTTP(S)/ALL 代理变量, 供启动 cloudflared 子进程用。
// cloudflared 只跟 Cloudflare 边缘通信, 走代理没有意义且会挂 (见 StartNamed 处注释)。
// 大小写两种拼法都清 (Go 只认大写, 但子进程库可能读小写)。
func tunnelChildEnv() []string {
	drop := map[string]bool{
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true,
		"http_proxy": true, "https_proxy": true, "all_proxy": true,
	}
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		eq := strings.IndexByte(kv, '=')
		if eq > 0 && drop[kv[:eq]] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
