package transcript

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func appendRequestFixture(t *testing.T, path, value string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(value); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestScanCodexRequestUsage_NormalizesInclusiveBucketsAndSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-test.jsonl")
	data := "" +
		`{"timestamp":"2026-07-14T01:00:00Z","type":"session_meta","payload":{"id":"thread-1"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:01Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.6-sol","model_provider_id":"openai","service_tier":"priority","reasoning_effort":"xhigh"}}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":11790,"cached_input_tokens":9000,"output_tokens":210,"reasoning_output_tokens":40,"total_tokens":12000}}}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	facts, err := ScanCodexRequestUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts=%d, want 1", len(facts))
	}
	f := facts[0]
	if f.InputTokens != 2790 || f.CachedInputTokens != 9000 || f.OutputTokens != 210 {
		t.Fatalf("normalized tokens=%+v, want fresh=2790 cached=9000 output=210", f)
	}
	if f.ReasoningOutputTokens != 40 || f.PhysicalTotalTokens() != 12000 {
		t.Fatalf("reasoning=%d total=%d, want 40/12000", f.ReasoningOutputTokens, f.PhysicalTotalTokens())
	}
	if f.Model != "gpt-5.6-sol" || f.Effort != "xhigh" || f.ServiceTier != "priority" {
		t.Fatalf("settings lost: %+v", f)
	}
}

func TestScanCodexRequestUsage_ObservedFirstResponseUsesTranscriptEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-response.jsonl")
	data := `{"timestamp":"2026-07-14T01:00:00Z","type":"session_meta","payload":{"id":"thread-response"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix"}]}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:03Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"inspect"}]}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2}}}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := ScanCodexRequestUsage(path)
	if err != nil || len(facts) != 1 {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	started := time.Date(2026, 7, 14, 1, 0, 1, 0, time.UTC)
	first := started.Add(2 * time.Second)
	if facts[0].StartedAt == nil || !facts[0].StartedAt.Equal(started) || facts[0].FirstObservedAt == nil || !facts[0].FirstObservedAt.Equal(first) || facts[0].EndedAt == nil || !facts[0].EndedAt.Equal(first) || facts[0].FirstTokenAt != nil {
		t.Fatalf("observed response was mislabeled as TTFT: %+v", facts[0])
	}
}

func TestScanCodexRequestUsage_ToolResultStartsNextRequestAfterPriorUsageCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-tools.jsonl")
	data := `{"timestamp":"2026-07-14T01:00:00Z","type":"session_meta","payload":{"id":"thread-tools"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix"}]}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:03Z","type":"response_item","payload":{"type":"reasoning","summary":[]}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:04Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"c1","name":"exec","input":"ls"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:09Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:09Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2}}}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:11Z","type":"response_item","payload":{"type":"reasoning","summary":[]}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:12Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"c2","name":"exec","input":"pwd"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:13Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c2","output":"ok"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:13Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":11,"output_tokens":3}}}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := ScanCodexRequestUsage(path)
	if err != nil || len(facts) != 2 {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	firstStart := time.Date(2026, 7, 14, 1, 0, 1, 0, time.UTC)
	firstEnd := firstStart.Add(3 * time.Second)
	secondStart := firstStart.Add(8 * time.Second)
	secondFirst := firstStart.Add(10 * time.Second)
	secondEnd := firstStart.Add(11 * time.Second)
	if facts[0].StartedAt == nil || !facts[0].StartedAt.Equal(firstStart) || facts[0].EndedAt == nil || !facts[0].EndedAt.Equal(firstEnd) {
		t.Fatalf("tool time leaked into prior response: %+v", facts[0])
	}
	if facts[1].StartedAt == nil || !facts[1].StartedAt.Equal(secondStart) || facts[1].FirstObservedAt == nil || !facts[1].FirstObservedAt.Equal(secondFirst) || facts[1].EndedAt == nil || !facts[1].EndedAt.Equal(secondEnd) {
		t.Fatalf("tool result was not promoted to next causal request: %+v", facts[1])
	}
}

func TestScanCodexRequestUsage_ReadsCurrentTurnContextEffortField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := `{"timestamp":"2026-07-14T01:00:00Z","type":"session_meta","payload":{"id":"s1"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:01Z","type":"turn_context","payload":{"model":"gpt-5.6-sol","effort":"xhigh","service_tier":"default"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":10}}}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := ScanCodexRequestUsage(path)
	if err != nil || len(facts) != 1 {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	if facts[0].Effort != "xhigh" || facts[0].Coverage.Effort != CoverageComplete {
		t.Fatalf("effort=%+v", facts[0])
	}
}

func TestScanCodexRequestUsage_SeparatesFastFromAPIPriority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := `{"timestamp":"2026-07-14T01:00:00Z","type":"session_meta","payload":{"id":"s1"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:01Z","type":"turn_context","payload":{"model":"gpt-5.5","effort":"high","service_tier":"fast"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":10}}}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := ScanCodexRequestUsage(path)
	if err != nil || len(facts) != 1 {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	fact := facts[0]
	if fact.ServiceTier != "default" || fact.Speed != "fast" || fact.BillingMode != "subscription" || fact.Coverage.Billing != CoverageComplete {
		t.Fatalf("fast request dimensions collapsed: %+v", fact)
	}
}

func TestScanClaudeRequestUsage_DedupAndCacheTTL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-1.jsonl")
	line := `{"type":"assistant","timestamp":"2026-07-14T02:00:00Z","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"input_tokens":52,"output_tokens":22556,"cache_read_input_tokens":1710200,"cache_creation_input_tokens":78151,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":78151},"service_tier":"standard","speed":"standard","inference_geo":"not_available"}}}`
	data := line + "\n" + line + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	facts, err := ScanClaudeRequestUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts=%d, want deduped 1", len(facts))
	}
	f := facts[0]
	if f.InputTokens != 52 || f.OutputTokens != 22556 || f.CachedInputTokens != 1710200 || f.CacheWrite1hTokens != 78151 {
		t.Fatalf("tokens=%+v", f)
	}
	if f.CacheWrite5mTokens != 0 || f.CacheWriteUnknownTokens != 0 || f.Coverage.CacheTTL != CoverageComplete {
		t.Fatalf("ttl split=%+v coverage=%+v", f, f.Coverage)
	}
}

func TestScanClaudeRequestUsage_ObservedResponseIntervalsUseCausalInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "causal.jsonl")
	data := `{"type":"user","timestamp":"2026-07-14T02:00:00Z","message":{"role":"user","content":"do it"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-07-14T02:00:05Z","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"output_tokens":10}}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-07-14T02:00:06Z","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"output_tokens":12}}}` + "\n" +
		`{"type":"user","timestamp":"2026-07-14T02:00:08Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"ok"}]}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-07-14T02:00:10Z","message":{"id":"msg_2","model":"claude-sonnet-5","usage":{"output_tokens":4}}}` + "\n" +
		`{"type":"user","timestamp":"2026-07-14T02:00:11Z","message":{"role":"user","content":"[Request interrupted by user]"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-07-14T02:00:20Z","message":{"id":"msg_3","model":"claude-sonnet-5","usage":{"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := ScanClaudeRequestUsage(path)
	if err != nil || len(facts) != 3 {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	wantStart := time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC)
	wantEnd := wantStart.Add(6 * time.Second)
	if facts[0].StartedAt == nil || !facts[0].StartedAt.Equal(wantStart) || facts[0].EndedAt == nil || !facts[0].EndedAt.Equal(wantEnd) || facts[0].OutputTokens != 12 {
		t.Fatalf("split response interval=%+v", facts[0])
	}
	wantFirstObserved := wantStart.Add(5 * time.Second)
	if facts[0].FirstObservedAt == nil || !facts[0].FirstObservedAt.Equal(wantFirstObserved) {
		t.Fatalf("first transcript observation=%v, want %v", facts[0].FirstObservedAt, wantFirstObserved)
	}
	toolResultAt := wantStart.Add(8 * time.Second)
	if facts[1].StartedAt == nil || !facts[1].StartedAt.Equal(toolResultAt) {
		t.Fatalf("tool-result boundary was not used: %+v", facts[1])
	}
	if facts[2].StartedAt != nil {
		t.Fatalf("post-interrupt response inherited stale start: %+v", facts[2])
	}
}

func TestScanClaudeRequestUsage_CausalInputIsConsumedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "causal-once.jsonl")
	data := `{"type":"user","timestamp":"2026-07-14T02:00:00Z","message":{"role":"user","content":"do it"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-07-14T02:00:01Z","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"output_tokens":2}}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-07-14T02:00:02Z","message":{"id":"msg_2","model":"claude-sonnet-5","usage":{"output_tokens":3}}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := ScanClaudeRequestUsage(path)
	if err != nil || len(facts) != 2 {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	if facts[0].StartedAt == nil || facts[1].StartedAt != nil {
		t.Fatalf("one causal input started multiple responses: %+v", facts)
	}
}

func TestScanClaudeRequestUsageIncremental_InterruptEmitsTimingTombstone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "interrupt-tombstone.jsonl")
	initial := `{"type":"user","timestamp":"2026-07-14T02:00:00Z","message":{"role":"user","content":"do it"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-07-14T02:00:01Z","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	first, cursor, err := ScanClaudeRequestUsageIncremental(path, ClaudeRequestCursor{})
	if err != nil || len(first) != 1 || first[0].StartedAt == nil {
		t.Fatalf("initial facts=%+v cursor=%+v err=%v", first, cursor, err)
	}
	appendRequestFixture(t, path,
		`{"type":"user","timestamp":"2026-07-14T02:00:02Z","message":{"role":"user","content":"[Request interrupted by user]"}}`+"\n"+
			`{"type":"assistant","timestamp":"2026-07-14T02:00:03Z","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"output_tokens":3}}}`+"\n")
	appended, _, err := ScanClaudeRequestUsageIncremental(path, cursor)
	if err != nil || len(appended) != 1 || !appended[0].TimingInvalidated || appended[0].StartedAt != nil || appended[0].Coverage.Timing != CoverageMissing {
		t.Fatalf("timing tombstone=%+v err=%v", appended, err)
	}
}

func TestScanCodexRequestUsageIncrementalPreservesSettingsAndPartialTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-incremental.jsonl")
	initial := `{"timestamp":"2026-07-14T01:00:00Z","type":"session_meta","payload":{"id":"thread-inc"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:01Z","type":"turn_context","payload":{"model":"gpt-5.6-terra","reasoning_effort":"high","service_tier":"standard"}}` + "\n" +
		`{"timestamp":"2026-07-14T01:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":10}}}}` + "\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	first, cursor, err := ScanCodexRequestUsageIncremental(path, CodexRequestCursor{})
	if err != nil || len(first) != 1 {
		t.Fatalf("first facts=%+v cursor=%+v err=%v", first, cursor, err)
	}
	if cursor.Model != "gpt-5.6-terra" || cursor.Effort != "high" || cursor.ServiceTier != "standard" {
		t.Fatalf("cursor lost settings: %+v", cursor)
	}

	appendRequestFixture(t, path,
		`{"timestamp":"2026-07-14T01:00:03Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.6-sol","service_tier":"priority","reasoning_effort":"xhigh"}}}`+"\n"+
			`{"timestamp":"2026-07-14T01:00:04Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":50,"cached_input_tokens":20,"output_tokens":5}}}}`+"\n")
	second, cursor, err := ScanCodexRequestUsageIncremental(path, cursor)
	if err != nil || len(second) != 1 || second[0].Model != "gpt-5.6-sol" || second[0].ServiceTier != "priority" {
		t.Fatalf("appended facts=%+v cursor=%+v err=%v", second, cursor, err)
	}
	if second[0].ID == first[0].ID || second[0].SourceOffset <= first[0].SourceOffset {
		t.Fatalf("append identity is not stable: first=%+v second=%+v", first[0], second[0])
	}

	partialStart := cursor.Offset
	appendRequestFixture(t, path, `{"timestamp":"2026-07-14T01:00:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":9`)
	partialFacts, partialCursor, err := ScanCodexRequestUsageIncremental(path, cursor)
	if err != nil || len(partialFacts) != 0 || partialCursor.Offset != partialStart {
		t.Fatalf("partial tail consumed: facts=%+v cursor=%+v start=%d err=%v", partialFacts, partialCursor, partialStart, err)
	}
	appendRequestFixture(t, path, `,"cached_input_tokens":4,"output_tokens":2}}}}`+"\n")
	completed, completedCursor, err := ScanCodexRequestUsageIncremental(path, partialCursor)
	if err != nil || len(completed) != 1 || completed[0].InputTokens != 5 || completedCursor.Offset <= partialStart {
		t.Fatalf("completed tail=%+v cursor=%+v err=%v", completed, completedCursor, err)
	}
}

func TestScanClaudeRequestUsageIncrementalEmitsAppendBatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-incremental.jsonl")
	firstLine := `{"type":"assistant","timestamp":"2026-07-14T02:00:00Z","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"input_tokens":10,"output_tokens":20,"service_tier":"standard"}}}`
	if err := os.WriteFile(path, []byte(firstLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, cursor, err := ScanClaudeRequestUsageIncremental(path, ClaudeRequestCursor{})
	if err != nil || len(first) != 1 {
		t.Fatalf("first=%+v cursor=%+v err=%v", first, cursor, err)
	}

	// A provider can repeat the same message with improved counters in a later
	// append. The batch emits that identity; the materialized view merges maxima.
	duplicate := `{"type":"assistant","timestamp":"2026-07-14T02:00:01Z","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"input_tokens":10,"output_tokens":25,"service_tier":"standard"}}}`
	appendRequestFixture(t, path, duplicate+"\n")
	second, cursor, err := ScanClaudeRequestUsageIncremental(path, cursor)
	if err != nil || len(second) != 1 || second[0].ID != first[0].ID || second[0].OutputTokens != 25 {
		t.Fatalf("append batch=%+v cursor=%+v err=%v", second, cursor, err)
	}

	partialStart := cursor.Offset
	appendRequestFixture(t, path, `{"type":"assistant","timestamp":"2026-07-14T02:00:02Z","message":{"id":"msg_2"`)
	partial, next, err := ScanClaudeRequestUsageIncremental(path, cursor)
	if err != nil || len(partial) != 0 || next.Offset != partialStart {
		t.Fatalf("partial tail consumed: facts=%+v cursor=%+v err=%v", partial, next, err)
	}
}

func TestScanClaudeRequestUsageIncrementalPreservesSplitMessageStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-causal-incremental.jsonl")
	initial := `{"type":"user","timestamp":"2026-07-14T02:00:00Z","message":{"role":"user","content":"go"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-07-14T02:00:04Z","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"output_tokens":8}}}` + "\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	first, cursor, err := ScanClaudeRequestUsageIncremental(path, ClaudeRequestCursor{})
	if err != nil || len(first) != 1 || first[0].StartedAt == nil {
		t.Fatalf("first=%+v cursor=%+v err=%v", first, cursor, err)
	}
	appendRequestFixture(t, path, `{"type":"assistant","timestamp":"2026-07-14T02:00:07Z","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"output_tokens":11}}}`+"\n")
	second, _, err := ScanClaudeRequestUsageIncremental(path, cursor)
	if err != nil || len(second) != 1 || second[0].StartedAt == nil || !second[0].StartedAt.Equal(*first[0].StartedAt) {
		t.Fatalf("split append lost causal start: first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestIncrementalRequestScannersCountMalformedCompleteRows(t *testing.T) {
	dir := t.TempDir()
	codexPath := filepath.Join(dir, "rollout-bad.jsonl")
	if err := os.WriteFile(codexPath, []byte("{bad}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, codexCursor, err := ScanCodexRequestUsageIncremental(codexPath, CodexRequestCursor{})
	if err != nil || codexCursor.MalformedLines != 1 {
		t.Fatalf("codex malformed cursor=%+v err=%v", codexCursor, err)
	}
	claudePath := filepath.Join(dir, "claude-bad.jsonl")
	if err := os.WriteFile(claudePath, []byte(`{"type":"assistant"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, claudeCursor, err := ScanClaudeRequestUsageIncremental(claudePath, ClaudeRequestCursor{})
	if err != nil || claudeCursor.MalformedLines != 1 {
		t.Fatalf("claude malformed cursor=%+v err=%v", claudeCursor, err)
	}
}

func TestCodexUsageMap_DoesNotDoubleCachedOrReasoning(t *testing.T) {
	u := (&codexTokenUsage{InputTokens: 11790, CachedInputTokens: 9000, OutputTokens: 210, ReasoningOutputTokens: 40}).usageMap()
	if got := intField(u, "input_tokens"); got != 2790 {
		t.Fatalf("input=%d", got)
	}
	if got := intField(u, "cache_read_input_tokens"); got != 9000 {
		t.Fatalf("cached=%d", got)
	}
	if got := intField(u, "output_tokens"); got != 210 {
		t.Fatalf("output=%d", got)
	}
	if got := intField(u, "thinking_tokens"); got != 40 {
		t.Fatalf("reasoning=%d", got)
	}
}
