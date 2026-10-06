package usage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The vendor projects one ratio as `used` or as `remaining` and omits the boring side.
// Observed 2026-10-06: an untouched account sends ONLY limit+remaining — requiring `used`
// made every fresh subscription read as "no windows at all".
func TestKimiWindow_RemainingProjection(t *testing.T) {
	cases := []struct {
		name   string
		w      kimiWindowJSON
		used   float64
		wantOK bool
	}{
		{"untouched: remaining only, no used field", kimiWindowJSON{Limit: "100", Remaining: "100", ResetTime: "2026-10-06T15:44:54.135297Z"}, 0, true},
		{"partially used: remaining projection", kimiWindowJSON{Limit: "100", Remaining: "78"}, 22, true},
		{"classic: explicit used", kimiWindowJSON{Limit: "100", Used: "35"}, 35, true},
		{"no projection at all", kimiWindowJSON{Limit: "100"}, 0, false},
		{"zero limit is no reading", kimiWindowJSON{Limit: "0", Used: "0"}, 0, false},
		{"unparseable limit is no reading", kimiWindowJSON{Limit: "", Remaining: "1"}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.w.window(kimiPlanWindowMinutes)
			if ok != tc.wantOK {
				t.Fatalf("window(%+v) ok=%v, want %v", tc.w, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if diff := got.UsedPercent - tc.used; diff > 0.01 || diff < -0.01 {
				t.Fatalf("window(%+v) used%% = %f, want %f", tc.w, got.UsedPercent, tc.used)
			}
		})
	}
}

// The failure branches are product surface: each vendor answer names its own fix. A probe
// that collapses them into one generic error sends users guessing (UB-33).
func TestKimiGet_FailureBranches(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantSubstr string
	}{
		{"unauthorized names the key", http.StatusUnauthorized, "订阅 key 无效或已过期"},
		{"open-platform key has no usages endpoint", http.StatusNotFound, "开放平台按量 key 不是订阅 key"},
		{"server error keeps the number", http.StatusInternalServerError, "HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			err := kimiGet(context.Background(), Credential{APIKey: "k", BaseURL: srv.URL}, kimiUsagesPath, &kimiUsagesJSON{})
			if err == nil || !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("kimiGet status %d: got %v, want containing %q", tc.status, err, tc.wantSubstr)
			}
		})
	}
}

func TestZhipuGet_FailureBranches(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantSubstr string
	}{
		{"unauthorized names the key", http.StatusUnauthorized, "订阅 key 无效或已过期"},
		{"server error keeps the number", http.StatusInternalServerError, "HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			err := zhipuGet(context.Background(), Credential{APIKey: "k", BaseURL: srv.URL}, &zhipuQuotaJSON{})
			if err == nil || !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("zhipuGet status %d: got %v, want containing %q", tc.status, err, tc.wantSubstr)
			}
		})
	}
}

// The session row names the party it is billed to — from the host's profile→vendor
// derivation, not a model-name guess. A profile the host knows nothing about makes no
// vendor claim at all.
func TestClaudeAPISessions_CarryVendorFromHostMapping(t *testing.T) {
	deepwork := t.TempDir()
	t.Setenv("DEEPWORK_HOME", deepwork)
	SetProfileVendors(map[string]string{"glm": VendorZhipu, "kimi": VendorMoonshot})
	t.Cleanup(func() { SetProfileVendors(nil) })

	now := time.Now()
	for _, p := range []string{"glm", "minimax"} {
		drop := `{"captured_at":` + strconv.FormatInt(now.Unix(), 10) + `,"source":"api","profile":"` + p + `"}`
		if err := os.WriteFile(filepath.Join(deepwork, "claude-rate-limits-"+p+".json"), []byte(drop), 0o600); err != nil {
			t.Fatalf("write drop file: %v", err)
		}
	}

	sessions := claudeAPISessions(now)
	if len(sessions) != 2 {
		t.Fatalf("expected 2 live session rows, got %d", len(sessions))
	}
	byName := map[string]ClaudeAPISession{}
	for _, s := range sessions {
		byName[s.Name] = s
	}
	if got := byName["glm"].Vendor; got != VendorZhipu {
		t.Fatalf("glm session row must carry its derived vendor, got %q", got)
	}
	if got := byName["minimax"].Vendor; got != "" {
		t.Fatalf("an unmapped profile makes no vendor claim, got %q", got)
	}
}
