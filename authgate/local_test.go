package authgate

import "testing"

func noHeader(string) string { return "" }

func TestIsGenuineLocal(t *testing.T) {
	// Loopback peer + no forwarding header → genuine local.
	if !IsGenuineLocal("127.0.0.1:5051", noHeader) {
		t.Error("127.0.0.1 with no forwarding header should be genuine local")
	}
	if !IsGenuineLocal("[::1]:5051", noHeader) {
		t.Error("::1 with no forwarding header should be genuine local")
	}

	// Loopback peer BUT a forwarding header present (the tunnel case: cloudflared → 127.0.0.1) → NOT local.
	for _, h := range ForwardingHeaders {
		hdr := func(name string) string {
			if name == h {
				return "203.0.113.9"
			}
			return ""
		}
		if IsGenuineLocal("127.0.0.1:5051", hdr) {
			t.Errorf("loopback peer with %s set must NOT be genuine local (tunnel traffic)", h)
		}
	}

	// Non-loopback peer → never local, regardless of headers.
	if IsGenuineLocal("203.0.113.9:5051", noHeader) {
		t.Error("public peer must not be genuine local")
	}
	if IsGenuineLocal("192.168.1.20:5051", noHeader) {
		t.Error("LAN peer must not be genuine local")
	}
}
