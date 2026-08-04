package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

// Codex materializes a fork by copying the PARENT's rollout into the child file — the parent's
// session_meta included — so the model has the inherited context. Those rows are the parent's
// requests, already counted in the parent's own rollout.
//
// Shape taken from a real subagent spawn: the child's session_meta, then the copied parent block,
// then the child's own task_started and its genuine work.
func TestScanCodexRequestUsageSkipsCopiedParentHistory(t *testing.T) {
	// uuid-v7 ids: leading 12 hex digits are the millisecond timestamp. The child's turn must fall
	// within 10 minutes AFTER the child's session id for the boundary to accept it.
	const child = "019faf2f-e3a2-7711-a2bd-c36089627192" // 019faf2fe3a2 ms
	const body = `{"timestamp":"2026-07-29T18:42:59.427Z","type":"session_meta","payload":{"id":"019faf2f-e3a2-7711-a2bd-c36089627192","forked_from_id":"019f9d9c-adfe-7852-8c4f-9d4c94e4c84c"}}
{"timestamp":"2026-07-29T18:42:59.428Z","type":"session_meta","payload":{"id":"019f9d9c-adfe-7852-8c4f-9d4c94e4c84c"}}
{"timestamp":"2026-07-29T18:42:59.429Z","type":"turn_context","payload":{"model":"gpt-5.6-sol","service_tier":"default"}}
{"timestamp":"2026-07-29T18:42:59.430Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":900000,"output_tokens":5000,"total_tokens":905000}}}}
{"timestamp":"2026-07-29T18:42:59.431Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":800000,"output_tokens":4000,"total_tokens":804000}}}}
{"timestamp":"2026-07-29T18:42:59.470Z","type":"event_msg","payload":{"type":"task_started","turn_id":"019faf2f-f000-7000-8000-000000000000"}}
{"timestamp":"2026-07-29T18:42:59.480Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.6-sol","service_tier":"default"}}}
{"timestamp":"2026-07-29T19:23:31.444Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":300,"output_tokens":10,"total_tokens":310}}}}
`
	facts := scanCodexFixture(t, "forked.jsonl", body)
	if len(facts) != 1 {
		t.Fatalf("got %d facts, want 1 — the copied parent block must not be counted; facts=%+v", len(facts), facts)
	}
	if facts[0].InputTokens != 300 || facts[0].OutputTokens != 10 {
		t.Errorf("surviving fact = in %d / out %d, want the child's own 300/10", facts[0].InputTokens, facts[0].OutputTokens)
	}
	_ = child
}

// The guard must be narrow. An ORDINARY rollout is read whole — dropping real usage is the
// opposite mistake and a harder one to notice than an inflated total.
func TestScanCodexRequestUsageReadsPlainRolloutWhole(t *testing.T) {
	const body = `{"timestamp":"2026-07-29T18:00:00.000Z","type":"session_meta","payload":{"id":"019faf2f-e3a2-7711-a2bd-c36089627192"}}
{"timestamp":"2026-07-29T18:00:01.000Z","type":"turn_context","payload":{"model":"gpt-5.6-sol","service_tier":"default"}}
{"timestamp":"2026-07-29T18:00:02.000Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"output_tokens":50,"total_tokens":1050}}}}
{"timestamp":"2026-07-29T18:05:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":300,"output_tokens":10,"total_tokens":310}}}}
`
	if facts := scanCodexFixture(t, "plain.jsonl", body); len(facts) != 2 {
		t.Fatalf("got %d facts, want 2 — an ordinary rollout has no inherited block to skip", len(facts))
	}
}

// A fork whose boundary cannot be located must FAIL, not fall back to reading from the top.
// Reading it whole would silently double-count the parent's entire history; one skipped file is
// visible in coverage, an inflated total is not.
func TestScanCodexRequestUsageRefusesForkWithNoBoundary(t *testing.T) {
	const body = `{"timestamp":"2026-07-29T18:42:59.427Z","type":"session_meta","payload":{"id":"019faf2f-e3a2-7711-a2bd-c36089627192"}}
{"timestamp":"2026-07-29T18:42:59.428Z","type":"session_meta","payload":{"id":"019f9d9c-adfe-7852-8c4f-9d4c94e4c84c"}}
{"timestamp":"2026-07-29T18:42:59.430Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":900000,"output_tokens":5000,"total_tokens":905000}}}}
`
	path := filepath.Join(t.TempDir(), "noboundary.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if facts, err := ScanCodexRequestUsage(path); err == nil {
		t.Errorf("read %d facts from a fork with no boundary; want an error rather than the parent's history", len(facts))
	}
}

func TestCodexOwnStartOffsetLeavesOrdinaryRolloutsAlone(t *testing.T) {
	const body = `{"timestamp":"2026-07-29T18:00:00.000Z","type":"session_meta","payload":{"id":"019faf2f-e3a2-7711-a2bd-c36089627192"}}
`
	path := filepath.Join(t.TempDir(), "solo.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	off, err := CodexOwnStartOffset(path, "019faf2f-e3a2-7711-a2bd-c36089627192")
	if err != nil || off != 0 {
		t.Errorf("offset=%d err=%v, want 0/nil for a rollout with no inherited block", off, err)
	}
}

func scanCodexFixture(t *testing.T, name, body string) []ModelRequestUsage {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	facts, err := ScanCodexRequestUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

// Shape B: only the parent's EVENTS are copied in — no foreign session_meta — so shape A's
// detector never fires. Every unattributed request on this machine came from this shape.
//
// The boundary is the child's first turn declaration: codex cannot run a turn it has not
// configured, so usage recorded before the first declaration was configured by somebody else.
func TestScanCodexRequestUsageSkipsCopiedEventsWithoutParentSessionMeta(t *testing.T) {
	const body = `{"timestamp":"2026-07-29T18:42:59.427Z","type":"session_meta","payload":{"id":"019faf2f-e3a2-7711-a2bd-c36089627192","forked_from_id":"019f9d9c-adfe-7852-8c4f-9d4c94e4c84c","source":{"subagent":{"thread_spawn":{"depth":1}}}}}
{"timestamp":"2026-07-29T18:42:59.428Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":900000,"output_tokens":5000,"total_tokens":905000}}}}
{"timestamp":"2026-07-29T18:42:59.429Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":800000,"output_tokens":4000,"total_tokens":804000}}}}
{"timestamp":"2026-07-29T18:42:59.479Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.6-sol","service_tier":"default"}}}
{"timestamp":"2026-07-29T19:23:31.444Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":300,"output_tokens":10,"total_tokens":310}}}}
`
	facts := scanCodexFixture(t, "shapeB.jsonl", body)
	if len(facts) != 1 {
		t.Fatalf("got %d facts, want 1 — the copied event block must not be counted; facts=%+v", len(facts), facts)
	}
	if facts[0].InputTokens != 300 {
		t.Errorf("surviving fact input=%d, want the child's own 300", facts[0].InputTokens)
	}
	if facts[0].Model != "gpt-5.6-sol" {
		t.Errorf("model=%q, want gpt-5.6-sol — the declaration that ends the copied block also names the model", facts[0].Model)
	}
}

// Shape B must not fire on a rollout that declares no parent. An ordinary session reporting usage
// before its first turn_context is real work; skipping it would delete spend silently, which is
// the worse of the two mistakes.
func TestShapeBRequiresADeclaredParent(t *testing.T) {
	const body = `{"timestamp":"2026-07-29T18:00:00.000Z","type":"session_meta","payload":{"id":"019faf2f-e3a2-7711-a2bd-c36089627192"}}
{"timestamp":"2026-07-29T18:00:01.000Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"output_tokens":50,"total_tokens":1050}}}}
{"timestamp":"2026-07-29T18:00:02.000Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.6-sol","service_tier":"default"}}}
`
	if facts := scanCodexFixture(t, "orphan.jsonl", body); len(facts) != 1 {
		t.Fatalf("got %d facts, want 1 — an unforked rollout's early usage is its own", len(facts))
	}
}
