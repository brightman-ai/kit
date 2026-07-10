package usage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadClaudeRateLimits covers the reader's honest-degradation contract.
func TestReadClaudeRateLimits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-rate-limits.json")

	// Missing file → not ok.
	if _, ok := readClaudeRateLimits(path); ok {
		t.Fatal("missing file should yield ok=false")
	}

	// Nulls (pre-first-response) → not ok.
	if err := os.WriteFile(path, []byte(`{"captured_at":1,"five_hour":null,"seven_day":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readClaudeRateLimits(path); ok {
		t.Fatal("all-null windows should yield ok=false (honest —)")
	}

	// Garbage → not ok (no panic).
	_ = os.WriteFile(path, []byte(`not json`), 0o644)
	if _, ok := readClaudeRateLimits(path); ok {
		t.Fatal("invalid JSON should yield ok=false")
	}

	// Valid → ok + correct windows.
	_ = os.WriteFile(path, []byte(`{"captured_at":100,"five_hour":{"used_percentage":37.5,"resets_at":1750000000},"seven_day":{"used_percentage":12,"resets_at":1750500000}}`), 0o644)
	rl, ok := readClaudeRateLimits(path)
	if !ok {
		t.Fatal("valid file should yield ok=true")
	}
	q5, ok := claudeQuotaWindow("5h", rl.FiveHour)
	if !ok || q5.Kind != "5h" || q5.UsedPercent != 37.5 || q5.RemainingPercent != 62.5 || q5.WindowMinutes != 300 {
		t.Fatalf("5h window mapped wrong: %+v", q5)
	}
	if q5.ResetAt == "" {
		t.Fatal("expected non-empty ResetAt for 5h")
	}
	q7, ok := claudeQuotaWindow("7d", rl.SevenDay)
	if !ok || q7.Kind != "7d" || q7.UsedPercent != 12 || q7.WindowMinutes != 10080 {
		t.Fatalf("7d window mapped wrong: %+v", q7)
	}

	// API-key billing: null windows but explicit source="api" IS usable (ok=true),
	// so the chip can label it 「API 计费」 rather than dropping claude or showing "expired".
	_ = os.WriteFile(path, []byte(`{"captured_at":200,"source":"api","five_hour":null,"seven_day":null}`), 0o644)
	api, ok := readClaudeRateLimits(path)
	if !ok {
		t.Fatal("source=api with null windows should yield ok=true")
	}
	if api.Source != "api" || api.FiveHour != nil || api.SevenDay != nil {
		t.Fatalf("api file parsed wrong: %+v", api)
	}
}
