package usage

import (
	"context"
	"os/exec"
	"time"
)

// readSystemProxy asks macOS for the current proxy settings — the same ones System Settings shows
// and every app that uses the system proxy follows. ok=false when scutil could not answer (missing,
// failed, or past its timeout): the caller keeps whatever it had, and with nothing yet that is "no
// system proxy", which is exactly the behaviour before this existed.
//
// The timeout is the bound on one read, not on any request: callers never wait on it unless they
// have no answer at all (see currentSystemProxy).
func readSystemProxy() (systemProxy, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return systemProxy{}, false
	}
	return parseScutilProxy(string(out)), true
}
