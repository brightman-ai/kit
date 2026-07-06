package authgate

import "net"

// ForwardingHeaders are set by any reverse proxy / tunnel in front of the server (cloudflared sets
// Cf-Connecting-Ip + X-Forwarded-For). Their mere PRESENCE means the request did NOT originate on
// this machine, so it disqualifies the localhost auth-bypass. They are NEVER read for access — they
// can only REVOKE the bypass, never grant it — so a forged header can't be used to get in.
var ForwardingHeaders = []string{
	"X-Forwarded-For",
	"X-Real-Ip",
	"Forwarded",
	"Cf-Connecting-Ip",
	"True-Client-Ip",
}

// IsGenuineLocal reports whether a request truly originated on this machine and therefore earns the
// no-code convenience. "Genuine" means BOTH: the real TCP peer is a loopback address, AND no
// proxy/forwarding header is present.
//
// This is the SSOT for the tunnel auth-bypass fix: a cloudflare tunnel reaches the server over
// loopback (cloudflared connects to 127.0.0.1), so the peer address alone can't tell tunnel traffic
// from a real local browser — and a framework's ClientIP() is worse, since it honours an
// attacker-set X-Forwarded-For. Callers pass the UN-spoofable TCP peer (net/http Request.RemoteAddr,
// gin c.Request.RemoteAddr) and a header accessor; the presence of ANY forwarding header proves the
// request came through a proxy/tunnel → not local → must auth.
//
//	authgate.IsGenuineLocal(c.Request.RemoteAddr, c.GetHeader)   // gin
//	authgate.IsGenuineLocal(r.RemoteAddr, r.Header.Get)          // net/http
func IsGenuineLocal(remoteAddr string, header func(name string) string) bool {
	if !isLoopbackHost(peerHost(remoteAddr)) {
		return false
	}
	for _, h := range ForwardingHeaders {
		if header(h) != "" {
			return false
		}
	}
	return true
}

// peerHost extracts the host portion of a "host:port" RemoteAddr (falling back to the raw value).
func peerHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// isLoopbackHost reports whether host is a loopback address (IPv4 127/8 or IPv6 ::1) or "localhost".
func isLoopbackHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost"
}
