//go:build !darwin

package usage

// readSystemProxy: no system-wide proxy setting this package knows how to read on this platform;
// the answer is always "none", so probes follow the environment alone.
func readSystemProxy() (systemProxy, bool) { return systemProxy{}, true }
