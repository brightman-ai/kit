package usage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBuildReport_JSONLFixture verifies the claude JSONL path end to end:
// per-message dedup (a streaming partial with the same message id must not
// double-count) and correct summary/row aggregation through BuildReport.
func TestBuildReport_JSONLFixture(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	ts := today + "T10:00:00Z"

	// One assistant row: input=100, output=50, cache_read=20.
	fixture := fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"message":{"id":"msg_test001","model":"claude-3-5-sonnet-20241022","stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":20,"cache_creation_input_tokens":0}}}`,
		ts,
	) + "\n"

	// Duplicate streaming row (same message id) with lower counts — must be deduped away.
	fixtureDup := fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"message":{"id":"msg_test001","model":"claude-3-5-sonnet-20241022","stop_reason":null,"usage":{"input_tokens":80,"output_tokens":30,"cache_read_input_tokens":10,"cache_creation_input_tokens":0}}}`,
		ts,
	) + "\n"

	// A distinct message.
	fixture2 := fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"message":{"id":"msg_test002","model":"claude-3-5-sonnet-20241022","stop_reason":"end_turn","usage":{"input_tokens":200,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		ts,
	) + "\n"

	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "projects", "-test-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	jsonlPath := filepath.Join(projectDir, "session1.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(fixture+fixtureDup+fixture2), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	src := NewJSONLTokenSourceAt(filepath.Join(tmpDir, "projects"))
	report := BuildReport(Window7d, src)

	if !report.Available {
		t.Fatalf("expected available=true, got false (reason=%q)", report.Reason)
	}
	if report.Summary.InputTokens != 300 {
		t.Errorf("expected summary.input_tokens=300 (100+200), got %d", report.Summary.InputTokens)
	}
	if report.Summary.OutputTokens != 150 {
		t.Errorf("expected summary.output_tokens=150 (50+100), got %d", report.Summary.OutputTokens)
	}
	if report.Summary.CacheReadTokens != 20 {
		t.Errorf("expected summary.cache_read_tokens=20 (dup dropped), got %d", report.Summary.CacheReadTokens)
	}

	var todayRow *ReportRow
	for i := range report.Rows {
		if report.Rows[i].Date == today {
			todayRow = &report.Rows[i]
			break
		}
	}
	if todayRow == nil {
		t.Fatalf("no row found for today (%s) in report", today)
	}
	if todayRow.InputTokens != 300 {
		t.Errorf("today row input_tokens: expected 300, got %d", todayRow.InputTokens)
	}
}
