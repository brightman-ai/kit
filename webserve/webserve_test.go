package webserve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssetCacheControl(t *testing.T) {
	cases := map[string]string{
		"/assets/index-ABC123.js": immutableCacheControl,
		"assets/x.css":            immutableCacheControl,
		"/index.html":             revalidateCacheControl,
		"/":                       revalidateCacheControl,
		"/sw.js":                  revalidateCacheControl,
		"/pwa-192.png":            revalidateCacheControl,
	}
	for path, want := range cases {
		if got := AssetCacheControl(path); got != want {
			t.Errorf("AssetCacheControl(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestSetSecurity(t *testing.T) {
	c := Config{CSP: SPACSP("'none'"), FrameOptions: "SAMEORIGIN", HSTS: true}
	// over HTTPS → all headers incl HSTS
	h := http.Header{}
	c.SetSecurity(h, true)
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        referrerPolicy,
		"X-Frame-Options":        "SAMEORIGIN",
	} {
		if h.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, h.Get(k), want)
		}
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-src 'none'") {
		t.Errorf("CSP missing frame-src 'none': %q", h.Get("Content-Security-Policy"))
	}
	if h.Get("Strict-Transport-Security") == "" {
		t.Error("HSTS must be set over HTTPS")
	}
	// over plain HTTP → NO HSTS (don't pin a LAN HTTP host to HTTPS)
	h2 := http.Header{}
	c.SetSecurity(h2, false)
	if h2.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS must NOT be set over plain HTTP")
	}
}

func TestSPACSP_FrameSrc(t *testing.T) {
	if !strings.Contains(SPACSP("'self'"), "frame-src 'self'") {
		t.Error("pro variant must allow same-origin frames")
	}
	if !strings.Contains(SPACSP(""), "frame-src 'none'") {
		t.Error("empty frameSrc must default to 'none'")
	}
	// no inline script allowance (Vite emits external module scripts only)
	if strings.Contains(SPACSP("'none'"), "script-src 'self' 'unsafe-inline'") {
		t.Error("script-src must stay strict 'self'")
	}
}

func TestIsHTTPS(t *testing.T) {
	r := httptest.NewRequest("GET", "http://x/", nil)
	if IsHTTPS(r) {
		t.Error("plain HTTP must not read as HTTPS")
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if !IsHTTPS(r) {
		t.Error("X-Forwarded-Proto=https must read as HTTPS")
	}
}
