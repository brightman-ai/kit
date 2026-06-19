//go:build windows

package tunnel

import "syscall"

// detachedSysProcAttr starts cloudflared in a new process group so a console Ctrl-Break to the
// host does not propagate. NOTE: full host-restart survival (and the signal-0 liveness probe in
// pidAlive) is only implemented for unix; Windows decoupling is best-effort and not yet covered
// by the tunnel spec.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000200} // CREATE_NEW_PROCESS_GROUP
}
