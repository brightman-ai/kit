package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScanCodexModelUsage_ModelSwitchAndDedup proves the shared ScanCodexModelUsage
// primitive (a) tags each usage delta with the model active for that turn,
// switching correctly on a mid-session turn_context change, and (b) sums
// last_token_usage deltas — NOT the cumulative total_token_usage — so an
// interleaved/repeating cumulative counter (observed in real ~/.codex data:
// a big number repeated, then two smaller numbers alternating — the signature
// of a main+subagent conversation sharing one rollout) does not get
// double-counted.
func TestScanCodexModelUsage_ModelSwitchAndDedup(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"timestamp":"2026-07-01T10:00:00.000Z","type":"session_meta","payload":{"id":"test-id","timestamp":"2026-07-01T10:00:00.000Z","cwd":"/tmp/proj"}}`,
		// first turn_context: model = gpt-5.4
		`{"timestamp":"2026-07-01T10:00:01.000Z","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.4"}}`,
		`{"timestamp":"2026-07-01T10:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
		// rate-limit-only line (info=null) → must be skipped entirely
		`{"timestamp":"2026-07-01T10:00:03.000Z","type":"event_msg","payload":{"type":"token_count","info":null}}`,
		// turn 1 usage under gpt-5.4: cumulative total == last (first turn)
		`{"timestamp":"2026-07-01T10:00:04.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":11790,"cached_input_tokens":9000,"output_tokens":200,"reasoning_output_tokens":10,"total_tokens":12000},"last_token_usage":{"input_tokens":11790,"cached_input_tokens":9000,"output_tokens":200,"reasoning_output_tokens":10,"total_tokens":12000}}}}`,
		// turn 2: codex re-emits the SAME cumulative total 3 more times (observed
		// real-world artifact — repeated snapshot lines before the next real
		// delta) — last_token_usage is 0 on the repeats, so they must be skipped.
		`{"timestamp":"2026-07-01T10:00:05.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":11790,"cached_input_tokens":9000,"output_tokens":200,"reasoning_output_tokens":10,"total_tokens":12000},"last_token_usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":0}}}}`,
		`{"timestamp":"2026-07-01T10:00:06.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":11790,"cached_input_tokens":9000,"output_tokens":200,"reasoning_output_tokens":10,"total_tokens":12000},"last_token_usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":0}}}}`,
		// switch model mid-session: user ran /model to gpt-5.5
		`{"timestamp":"2026-07-01T10:00:07.000Z","type":"turn_context","payload":{"turn_id":"t2","model":"gpt-5.5"}}`,
		// turn 3 under gpt-5.5: cumulative jumps by 26827, last_token_usage carries the delta
		`{"timestamp":"2026-07-01T10:00:08.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":38617,"cached_input_tokens":20000,"output_tokens":900,"reasoning_output_tokens":50,"total_tokens":39567},"last_token_usage":{"input_tokens":26827,"cached_input_tokens":11000,"output_tokens":700,"reasoning_output_tokens":40,"total_tokens":27567}}}}`,
		// turn 4 under gpt-5.5: another real delta
		`{"timestamp":"2026-07-01T10:00:09.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":53654,"cached_input_tokens":29000,"output_tokens":1200,"reasoning_output_tokens":60,"total_tokens":54914},"last_token_usage":{"input_tokens":15037,"cached_input_tokens":9000,"output_tokens":300,"reasoning_output_tokens":10,"total_tokens":15347}}}}`,
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	path := filepath.Join(dir, "rollout-2026-07-01T10-00-00-test-id.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	events, err := ScanCodexModelUsage(path)
	if err != nil {
		t.Fatalf("ScanCodexModelUsage: %v", err)
	}

	// The two all-zero last_token_usage repeats must be skipped → exactly 3
	// real usage events (turn1@gpt-5.4, turn3@gpt-5.5, turn4@gpt-5.5).
	if len(events) != 3 {
		t.Fatalf("expected 3 usage events (zero-delta repeats skipped), got %d: %+v", len(events), events)
	}

	if events[0].Model != "gpt-5.4" || events[0].InputTokens != 11790 || events[0].CacheReadTokens != 9000 {
		t.Errorf("event[0] (pre-switch) = %+v, want model=gpt-5.4 input=11790 cacheRead=9000", events[0])
	}
	// output folds reasoning_output_tokens: 200+10=210
	if events[0].OutputTokens != 210 {
		t.Errorf("event[0].OutputTokens = %d, want 210 (200+10 reasoning)", events[0].OutputTokens)
	}

	if events[1].Model != "gpt-5.5" || events[1].InputTokens != 26827 {
		t.Errorf("event[1] (post-switch) = %+v, want model=gpt-5.5 input=26827", events[1])
	}
	if events[2].Model != "gpt-5.5" || events[2].InputTokens != 15037 {
		t.Errorf("event[2] (post-switch) = %+v, want model=gpt-5.5 input=15037", events[2])
	}

	// Sum-by-model must equal the real per-turn deltas, NOT the cumulative totals
	// (naive-sum-of-totals or naive-max would both be wrong here).
	var gpt54In, gpt55In int
	for _, e := range events {
		switch e.Model {
		case "gpt-5.4":
			gpt54In += e.InputTokens
		case "gpt-5.5":
			gpt55In += e.InputTokens
		}
	}
	if gpt54In != 11790 {
		t.Errorf("gpt-5.4 total input = %d, want 11790 (dedup of 4 repeated cumulative snapshots)", gpt54In)
	}
	if gpt55In != 26827+15037 {
		t.Errorf("gpt-5.5 total input = %d, want %d", gpt55In, 26827+15037)
	}
}
