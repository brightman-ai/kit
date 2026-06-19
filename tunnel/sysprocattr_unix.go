//go:build !windows

package tunnel

import "syscall"

// detachedSysProcAttr puts cloudflared in its own session (Setsid → new session leader,
// new process group). This is what decouples the tunnel from the host server: signals sent
// to the host's process group (Ctrl-C, the restart script's pkill, job-control TERM) no
// longer reach cloudflared, and it is not torn down when the host process exits.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
