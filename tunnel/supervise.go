package tunnel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ── Self-healing ────────────────────────────────────────────────────────────────
//
// cloudflared is a detached daemon: it survives host restarts, but nothing brings it BACK when it
// dies (OOM, edge kill, crash). Observed in production: the tunnel died overnight and stayed dead
// until a human noticed. Supervise closes that hole.
//
// The watchdog needs to distinguish "the tunnel died" from "the user turned it off" — otherwise it
// would resurrect a tunnel the user explicitly Stopped. So intent is persisted SEPARATELY from the
// live process record:
//
//	tunnel.json        — the running process (url+pid+addr). Removed whenever the process is gone.
//	tunnel-intent.json — what the user ASKED FOR. Written on a successful Start/StartNamed,
//	                     removed only by Stop(). Survives host restarts.
//
// Restart therefore means: intent exists AND no live process → re-establish exactly that intent.

// intent is the user's declared wish: the tunnel that SHOULD be up.
type intent struct {
	Mode      string `json:"mode"`               // "quick" | "named"
	Hostname  string `json:"hostname,omitempty"` // named only
	LocalAddr string `json:"localAddr"`
}

// SetLogf installs a log sink for supervisor events (restart attempts, failures, recovery).
func (t *Tunnel) SetLogf(f func(format string, v ...any)) {
	t.logMu.Lock()
	t.logf = f
	t.logMu.Unlock()
}

func (t *Tunnel) log(format string, v ...any) {
	t.logMu.RLock()
	f := t.logf
	t.logMu.RUnlock()
	if f != nil {
		f(format, v...)
	}
}

// saveIntent records what the user asked for. Called after a successful start.
func (t *Tunnel) saveIntent(in intent) {
	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(t.intentPath), 0755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(t.intentPath), ".intent-*.json")
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
	os.Rename(tmpName, t.intentPath) //nolint:errcheck
}

// loadIntent reads the declared intent. ok=false when the user has no tunnel enabled.
func (t *Tunnel) loadIntent() (intent, bool) {
	data, err := os.ReadFile(t.intentPath)
	if err != nil {
		return intent{}, false
	}
	var in intent
	if err := json.Unmarshal(data, &in); err != nil {
		return intent{}, false
	}
	if in.LocalAddr == "" || (in.Mode != "quick" && in.Mode != "named") {
		return intent{}, false
	}
	if in.Mode == "named" && in.Hostname == "" {
		return intent{}, false
	}
	return in, true
}

func (t *Tunnel) removeIntent() {
	os.Remove(t.intentPath) //nolint:errcheck
}

// adoptIntentFromState back-fills intent from a live tunnel started before intent existed, so an
// already-running deployment comes under supervision on its next host start without user action.
func (t *Tunnel) adoptIntentFromState(st persistedState) {
	if _, ok := t.loadIntent(); ok {
		return
	}
	mode := st.Mode
	if mode == "" {
		mode = "quick"
	}
	t.saveIntent(intent{Mode: mode, Hostname: st.Hostname, LocalAddr: st.LocalAddr})
}

// superviseBackoff is the restart backoff ladder. A tunnel that cannot come back (Cloudflare down,
// no network) must not be retried in a hot loop; a tunnel that died transiently must come back fast.
var superviseBackoff = []time.Duration{
	15 * time.Second,
	30 * time.Second,
	60 * time.Second,
	2 * time.Minute,
	5 * time.Minute,
}

// SuperviseInterval is how often the watchdog probes liveness.
const SuperviseInterval = 15 * time.Second

// Supervise runs the self-healing watchdog until ctx is cancelled. It probes tunnel liveness every
// SuperviseInterval and, when the user has an enabled tunnel whose cloudflared is gone, restarts it
// (exponential backoff on repeated failure, reset on success). It never starts a tunnel the user
// never enabled, and never resurrects one the user Stopped — both are "no intent".
//
// Call it once, in a goroutine, from the host server: go tun.Supervise(ctx).
func (t *Tunnel) Supervise(ctx context.Context) {
	t.SuperviseEvery(ctx, SuperviseInterval)
}

// SuperviseEvery is Supervise with an explicit probe interval (tests use a short one).
func (t *Tunnel) SuperviseEvery(ctx context.Context, interval time.Duration) {
	t.superviseOnce.Do(func() { t.supervise(ctx, interval) })
}

func (t *Tunnel) supervise(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	fails := 0
	var nextAttempt time.Time // zero = attempt as soon as a death is seen

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		in, ok := t.loadIntent()
		if !ok {
			fails, nextAttempt = 0, time.Time{} // tunnel off by the user's choice — nothing to heal
			continue
		}
		if t.IsRunning() {
			if fails > 0 {
				t.log("tunnel: recovered (%s → %s)", in.Mode, t.PublicURL())
			}
			fails, nextAttempt = 0, time.Time{}
			continue
		}
		if now := time.Now(); !nextAttempt.IsZero() && now.Before(nextAttempt) {
			continue // still backing off from the last failed restart
		}

		t.log("tunnel: cloudflared is gone — restarting (%s %s → %s, attempt %d)",
			in.Mode, in.Hostname, in.LocalAddr, fails+1)

		var err error
		if in.Mode == "named" {
			_, err = t.StartNamed(ctx, in.Hostname, in.LocalAddr)
		} else {
			_, err = t.Start(ctx, in.LocalAddr)
		}
		if err != nil {
			back := superviseBackoff[min(fails, len(superviseBackoff)-1)]
			fails++
			nextAttempt = time.Now().Add(back)
			t.log("tunnel: restart failed: %v (retry in %s)", err, back)
			continue
		}
		t.log("tunnel: restarted — %s", t.PublicURL())
		fails, nextAttempt = 0, time.Time{}
	}
}
