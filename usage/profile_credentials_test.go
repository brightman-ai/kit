package usage

import "testing"

// The table is the whole contract: a routing host names exactly one vendor, and anything
// unparsable, relative, or unknown simply does not derive. Path and case are noise — the
// same host serves routing and quota at different path prefixes.
func TestDeriveVendorFromBaseURL(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		vendor string
		ok     bool
	}{
		{"zhipu routing path", "https://open.bigmodel.cn/api/anthropic", VendorZhipu, true},
		{"zhipu bare host", "https://open.bigmodel.cn", VendorZhipu, true},
		{"zhipu with port and caps", "https://OPEN.BigModel.cn:8443/api/x", VendorZhipu, true},
		{"kimi coding root", "https://api.kimi.com/coding/", VendorMoonshot, true},
		{"kimi versioned", "https://api.kimi.com/coding/v1", VendorMoonshot, true},
		{"unknown vendor host", "https://api.example.com/v1", "", false},
		{"empty", "", "", false},
		{"blank", "   ", "", false},
		{"relative", "/api/anthropic", "", false},
		{"garbage", "://nope", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vendor, ok := DeriveVendorFromBaseURL(tc.raw)
			if ok != tc.ok || vendor != tc.vendor {
				t.Fatalf("DeriveVendorFromBaseURL(%q) = (%q, %v), want (%q, %v)", tc.raw, vendor, ok, tc.vendor, tc.ok)
			}
		})
	}
}
