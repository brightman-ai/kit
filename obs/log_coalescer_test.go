package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLogCoalescerSuppressesDuplicatesAndEmitsIntervalSummary(t *testing.T) {
	var buf bytes.Buffer
	Init(INFO, &buf)
	defer Init(INFO, nil)

	now := time.Date(2026, 5, 4, 1, 0, 0, 0, time.UTC)
	coalescer := newLogCoalescer(10*time.Second, func() time.Time { return now })
	logger := Module("coal")
	ctx := context.Background()

	coalescer.Info(ctx, logger, "session-a", "idle", "poll", "status", "idle")
	coalescer.Info(ctx, logger, "session-a", "idle", "poll", "status", "idle")
	now = now.Add(11 * time.Second)
	coalescer.Info(ctx, logger, "session-a", "idle", "poll", "status", "idle")

	entries := decodeLogLines(t, buf.String())
	if len(entries) != 2 {
		t.Fatalf("entries=%d want 2\nraw:\n%s", len(entries), buf.String())
	}
	if got := entries[1].Ext["coalesce_reason"]; got != "interval" {
		t.Fatalf("coalesce_reason=%v want interval", got)
	}
	if got := entries[1].Ext["coalesce_suppressed"]; got != float64(1) {
		t.Fatalf("coalesce_suppressed=%v want 1", got)
	}
	if got := entries[1].Ext["coalesce_count"]; got != float64(2) {
		t.Fatalf("coalesce_count=%v want 2", got)
	}
}

func TestLogCoalescerEmitsImmediatelyOnFingerprintChange(t *testing.T) {
	var buf bytes.Buffer
	Init(INFO, &buf)
	defer Init(INFO, nil)

	now := time.Date(2026, 5, 4, 1, 0, 0, 0, time.UTC)
	coalescer := newLogCoalescer(time.Minute, func() time.Time { return now })
	logger := Module("coal-change")
	ctx := context.Background()

	coalescer.Info(ctx, logger, "session-a", "idle", "poll", "status", "idle")
	coalescer.Info(ctx, logger, "session-a", "idle", "poll", "status", "idle")
	coalescer.Info(ctx, logger, "session-a", "running", "poll", "status", "running")

	entries := decodeLogLines(t, buf.String())
	if len(entries) != 2 {
		t.Fatalf("entries=%d want 2\nraw:\n%s", len(entries), buf.String())
	}
	if got := entries[1].Ext["status"]; got != "running" {
		t.Fatalf("status=%v want running", got)
	}
	if got := entries[1].Ext["coalesce_reason"]; got != "changed" {
		t.Fatalf("coalesce_reason=%v want changed", got)
	}
}

func decodeLogLines(t *testing.T, raw string) []Entry {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	entries := make([]Entry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("unmarshal log line: %v\nline=%s", err, line)
		}
		entries = append(entries, entry)
	}
	return entries
}
