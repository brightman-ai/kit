// Package webserve centralizes the production HTTP-serving conventions shared by the
// deepwork web services — the standalone terminal (net/http) and the pro host (Gin):
// the standard security-header set and content-hash-aware cache headers for Vite SPA
// builds. One source of truth so the two services can't drift on what "production-correct
// headers" means. The primitives operate on a bare http.Header, so a net/http middleware
// and a Gin middleware can both drive them without reimplementing the policy.
package webserve

import (
	"net/http"
	"strings"
)

// Config selects the response-specific security headers. The always-on headers
// (X-Content-Type-Options, Referrer-Policy, Permissions-Policy) need no configuration.
type Config struct {
	// CSP is the Content-Security-Policy value; empty omits the header. It is caller-supplied
	// because it legitimately differs per app: the terminal frames nothing (frame-src 'none'),
	// while pro embeds same-origin artifact iframes (frame-src 'self'). Build one with SPACSP.
	CSP string
	// FrameOptions is the X-Frame-Options value (e.g. "DENY", "SAMEORIGIN"); empty omits it.
	FrameOptions string
	// HSTS emits Strict-Transport-Security, but only on requests that reached us over HTTPS
	// (direct TLS or a trusted proxy's X-Forwarded-Proto) — never on a plain-HTTP LAN listener.
	HSTS bool
}

const (
	referrerPolicy    = "strict-origin-when-cross-origin"
	permissionsPolicy = "camera=(), microphone=(), geolocation=(), payment=(), usb=()"
	hstsValue         = "max-age=31536000" // 1y; no includeSubDomains/preload (reversible)

	// immutableCacheControl is for content-hashed assets — the hash IS the cache key, so a
	// changed file ships under a new name and can never be served stale.
	immutableCacheControl  = "public, max-age=31536000, immutable"
	revalidateCacheControl = "no-cache" // the shell (index.html, sw.js, manifest, icons)
)

// SetSecurity writes the configured security headers onto h. https reports whether the
// request arrived over TLS. This is the SSOT primitive both the net/http middleware and a
// Gin middleware call, so neither reimplements the set.
func (c Config) SetSecurity(h http.Header, https bool) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", referrerPolicy)
	h.Set("Permissions-Policy", permissionsPolicy)
	if c.FrameOptions != "" {
		h.Set("X-Frame-Options", c.FrameOptions)
	}
	if c.CSP != "" {
		h.Set("Content-Security-Policy", c.CSP)
	}
	if c.HSTS && https {
		h.Set("Strict-Transport-Security", hstsValue)
	}
}

// Middleware wraps next, applying SetSecurity to every response. HTTPS is detected from
// r.TLS or a trusted X-Forwarded-Proto=https (set by the tunnel / reverse proxy).
func (c Config) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.SetSecurity(w.Header(), IsHTTPS(r))
		next.ServeHTTP(w, r)
	})
}

// IsHTTPS reports whether r reached the server over TLS, directly or via a trusted proxy.
func IsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// AssetCacheControl returns the Cache-Control for a path served from a Vite build: content-
// hashed assets/* are immutable (cached a year), the shell (index.html, sw.js, manifest,
// icons) is no-cache so a new build is picked up on the next load. A stale index.html would
// pin the previous build's asset hashes and silently block updates — hence the split.
func AssetCacheControl(urlPath string) string {
	if strings.HasPrefix(strings.TrimPrefix(urlPath, "/"), "assets/") {
		return immutableCacheControl
	}
	return revalidateCacheControl
}

// SPACSP builds a Content-Security-Policy for a Vite SPA that talks only to its own origin
// (same-origin API + WebSocket). frameSrc sets the frame-src directive — "'none'" when the
// app embeds nothing, "'self'" when it embeds same-origin iframes (e.g. rendered artifacts);
// empty defaults to "'none'". style-src keeps 'unsafe-inline' because Vue's :style bindings
// emit inline style attributes; script-src stays strict 'self' (Vite emits no inline script).
func SPACSP(frameSrc string) string {
	if frameSrc == "" {
		frameSrc = "'none'"
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"worker-src 'self'",
		"manifest-src 'self'",
		"frame-src " + frameSrc,
		"object-src 'none'",
		"base-uri 'self'",
		"frame-ancestors 'self'",
		"form-action 'self'",
	}, "; ")
}
