package transcript

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Boundary fixtures (request §7). Each writes a REAL-schema jsonl and asserts the
// projection a human would recognise: one intent → one run.
// ---------------------------------------------------------------------------

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf []byte
	for _, l := range lines {
		buf = append(buf, l...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadCodex(t *testing.T, lines []string) *Transcript {
	t.Helper()
	root := t.TempDir()
	writeLines(t, filepath.Join(root, "sessions", "2026", "07", "12", "rollout-2026-07-12T00-00-00-fixture-0000-0000-0000-000000000001.jsonl"), lines)
	src := &CodexSource{Root: root}
	tr, err := src.LoadTranscript(context.Background(), SessionRef{ID: "fixture-0000-0000-0000-000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	tr.Runs = ProjectAgentRuns(tr)
	return tr
}

func loadClaude(t *testing.T, lines []string) *Transcript {
	t.Helper()
	root := t.TempDir()
	proj := "/tmp/fixture-proj"
	writeLines(t, filepath.Join(root, EncodeProjectDir(proj), "sess1.jsonl"), lines)
	src := &ClaudeSource{Root: root}
	tr, err := src.LoadTranscript(context.Background(), SessionRef{ProjectDir: proj, ID: "sess1"})
	if err != nil {
		t.Fatal(err)
	}
	tr.Runs = ProjectAgentRuns(tr)
	return tr
}

func countSegments(r AgentRun, kind string) int {
	n := 0
	for _, b := range r.Segments {
		if b.Type == kind {
			n++
		}
	}
	return n
}

// codex-tool-loop-12 (request §7.1): 1 user, reasoning, 12 tool calls/results,
// 6 token_count, final text, task_complete → EXACTLY one run.
func TestProjectAgentRuns_CodexToolLoop12(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-07-12T10:00:00.000Z","type":"session_meta","payload":{"type":"session_meta","id":"fixture-0000-0000-0000-000000000001","cwd":"/tmp/p","timestamp":"2026-07-12T10:00:00.000Z"}}`,
		`{"timestamp":"2026-07-12T10:00:01.000Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-07-12T10:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"帮我修复登录 bug"}]}}`,
		`{"timestamp":"2026-07-12T10:00:03.000Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"先定位"}]}}`,
	}
	ts := time.Date(2026, 7, 12, 10, 0, 4, 0, time.UTC)
	for i := 0; i < 12; i++ {
		at := ts.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		lines = append(lines,
			`{"timestamp":"`+at+`","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"c`+itoa(i)+`","arguments":"{\"cmd\":\"ls\"}"}}`,
			`{"timestamp":"`+at+`","type":"response_item","payload":{"type":"function_call_output","call_id":"c`+itoa(i)+`","output":"ok"}}`,
		)
		if i%2 == 0 { // 6 token_count events
			lines = append(lines, `{"timestamp":"`+at+`","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":10,"cached_input_tokens":5}}}}`)
		}
	}
	lines = append(lines,
		`{"timestamp":"2026-07-12T10:01:00.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"已修复。"}]}}`,
		`{"timestamp":"2026-07-12T10:01:01.000Z","type":"event_msg","payload":{"type":"task_complete"}}`,
	)

	tr := loadCodex(t, lines)
	if len(tr.Runs) != 1 {
		t.Fatalf("want 1 AgentRun for 1 human intent, got %d (raw turns=%d)", len(tr.Runs), len(tr.Turns))
	}
	r := tr.Runs[0]
	if r.Index != 1 || r.UserIntent == nil || r.UserIntent.Text != "帮我修复登录 bug" {
		t.Fatalf("bad intent: %+v", r.UserIntent)
	}
	if got := countSegments(r, BlockTool); got != 12 {
		t.Errorf("tool segments = %d, want 12", got)
	}
	if got := countSegments(r, BlockThinking); got != 1 {
		t.Errorf("thinking segments = %d, want 1", got)
	}
	if got := countSegments(r, BlockText); got != 0 {
		t.Errorf("text segments = %d, want 0 (the answer is FinalAnswer, not a process segment)", got)
	}
	if got := countSegments(r, BlockUsage); got != 0 {
		t.Errorf("usage must be absorbed into the run aggregate, found %d usage segments", got)
	}
	// 6 token_count deltas × (100 in / 10 out) → the run's single aggregate.
	if r.Usage == nil || r.Usage.InputTokens != 600 {
		t.Errorf("run input_tokens = %v, want 600 (6 deltas × 100)", r.Usage)
	}
	if r.Usage.OutputTokens != 60 {
		t.Errorf("run output_tokens = %d, want 60", r.Usage.OutputTokens)
	}
	if r.Status != RunCompleted {
		t.Errorf("status = %q, want completed (task_complete seen)", r.Status)
	}
	// Order is the transcript's, verbatim: thinking → 12 tools. The answer is NOT a
	// segment — it is the run's FinalAnswer, so a collapsed process can never hide it.
	if r.Segments[0].Type != BlockThinking || r.Segments[len(r.Segments)-1].Type != BlockTool {
		t.Errorf("segment order broken: first=%s last=%s", r.Segments[0].Type, r.Segments[len(r.Segments)-1].Type)
	}
	if len(r.FinalAnswer) != 1 || r.FinalAnswer[0].Text != "已修复。" {
		t.Fatalf("FinalAnswer must carry the terminal iteration's text, got %+v", r.FinalAnswer)
	}
}

// claude-multi-iteration (request §7.2): 1 user, 3 assistant messages with DIFFERENT
// message.ids separated by user-role tool_result lines, repeated usage frames →
// still ONE run, usage counted once per message.
func TestProjectAgentRuns_ClaudeMultiIteration(t *testing.T) {
	tr := loadClaude(t, []string{
		`{"type":"user","timestamp":"2026-07-12T10:00:00.000Z","message":{"role":"user","content":"改一下首页"}}`,
		`{"type":"assistant","timestamp":"2026-07-12T10:00:01.000Z","message":{"id":"msg_1","model":"claude-opus-4-8","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"看看文件"},{"type":"tool_use","id":"t1","name":"Read","input":{"path":"a.ts"}}],"usage":{"input_tokens":100,"output_tokens":10}}}`,
		`{"type":"user","timestamp":"2026-07-12T10:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file body"}]}}`,
		`{"type":"assistant","timestamp":"2026-07-12T10:00:03.000Z","message":{"id":"msg_2","model":"claude-opus-4-8","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t2","name":"Edit","input":{"path":"a.ts"}}],"usage":{"input_tokens":200,"output_tokens":20}}}`,
		`{"type":"user","timestamp":"2026-07-12T10:00:04.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"edited"}]}}`,
		// split lines of ONE message (msg_3) repeating usage → must count ONCE
		`{"type":"assistant","timestamp":"2026-07-12T10:00:05.000Z","message":{"id":"msg_3","model":"claude-opus-4-8","stop_reason":"end_turn","content":[{"type":"text","text":"改好了。"}],"usage":{"input_tokens":300,"output_tokens":30}}}`,
		`{"type":"assistant","timestamp":"2026-07-12T10:00:05.500Z","message":{"id":"msg_3","model":"claude-opus-4-8","stop_reason":"end_turn","content":[{"type":"text","text":"另外补充一句。"}],"usage":{"input_tokens":300,"output_tokens":30}}}`,
	})
	if len(tr.Runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(tr.Runs))
	}
	r := tr.Runs[0]
	if got := countSegments(r, BlockTool); got != 2 {
		t.Errorf("tool segments = %d, want 2", got)
	}
	if got := countSegments(r, BlockText); got != 0 {
		t.Errorf("text segments = %d, want 0 (both end_turn texts are the answer)", got)
	}
	if len(r.FinalAnswer) != 2 {
		t.Errorf("FinalAnswer blocks = %d, want 2 (the end_turn message's split text lines)", len(r.FinalAnswer))
	}
	// usage: 100 + 200 + 300 (msg_3 counted ONCE despite its split lines)
	if r.Usage == nil || r.Usage.InputTokens != 600 {
		t.Errorf("run input_tokens = %v, want 600 (dup usage frame must not double-count)", r.Usage)
	}
	if r.Status != RunCompleted {
		t.Errorf("status = %q, want completed (end_turn)", r.Status)
	}
	if r.Usage.Model != "claude-opus-4-8" {
		t.Errorf("run model = %q, want claude-opus-4-8", r.Usage.Model)
	}
}

// steer-midrun (request §7.3): codex queues human input INTO a running task (real
// data: 3 such lines inside one task_started→task_complete). They are amendments of
// the SAME run, not new rounds.
func TestProjectAgentRuns_SteerIsAmendmentNotNewRun(t *testing.T) {
	tr := loadCodex(t, []string{
		`{"timestamp":"2026-07-12T10:00:00.000Z","type":"session_meta","payload":{"type":"session_meta","id":"fixture-0000-0000-0000-000000000001","cwd":"/tmp/p","timestamp":"2026-07-12T10:00:00.000Z"}}`,
		`{"timestamp":"2026-07-12T10:00:01.000Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-07-12T10:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"跑一下测试"}]}}`,
		`{"timestamp":"2026-07-12T10:00:03.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"c1","arguments":"{}"}}`,
		`{"timestamp":"2026-07-12T10:00:04.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"ok"}}`,
		// steer #1 and #2 — arrive while the task is still running
		`{"timestamp":"2026-07-12T10:02:00.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"顺便跑 lint"}]}}`,
		`{"timestamp":"2026-07-12T10:05:00.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"别改格式"}]}}`,
		`{"timestamp":"2026-07-12T10:06:00.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"都跑完了。"}]}}`,
		`{"timestamp":"2026-07-12T10:06:01.000Z","type":"event_msg","payload":{"type":"task_complete"}}`,
		// a NEW intent after the yield → a new run (task_started precedes it, as codex writes it)
		`{"timestamp":"2026-07-12T10:06:30.000Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-07-12T10:07:00.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"提交吧"}]}}`,
	})
	if len(tr.Runs) != 2 {
		t.Fatalf("want 2 runs (1 steered + 1 new intent), got %d", len(tr.Runs))
	}
	r := tr.Runs[0]
	if len(r.Amendments) != 2 {
		t.Fatalf("want 2 amendments folded into run #1, got %d", len(r.Amendments))
	}
	if r.Amendments[0].Text != "顺便跑 lint" || r.Amendments[1].Text != "别改格式" {
		t.Errorf("amendment text lost: %+v", r.Amendments)
	}
	if tr.Runs[1].Index != 2 || tr.Runs[1].UserIntent.Text != "提交吧" {
		t.Errorf("second run wrong: %+v", tr.Runs[1])
	}
	// The run count IS the round count: a steer must not inflate it.
	if tr.Runs[1].Index != 2 {
		t.Errorf("round numbering broken: %d", tr.Runs[1].Index)
	}
}

// attention-boundaries (request §7.4): an ESC interrupt is the runtime's ABORT fact,
// not a human message. It closes the run honestly (interrupted, never "completed")
// and the human's retype afterwards opens a NEW run — the exact shape found in the
// real 60 MB claude transcript (7 interrupt markers).
func TestProjectAgentRuns_ClaudeInterruptClosesRun(t *testing.T) {
	tr := loadClaude(t, []string{
		`{"type":"user","timestamp":"2026-07-12T10:00:00.000Z","message":{"role":"user","content":"跑长任务"}}`,
		`{"type":"assistant","timestamp":"2026-07-12T10:00:01.000Z","message":{"id":"msg_1","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}],"usage":{"input_tokens":50,"output_tokens":5}}}`,
		`{"type":"user","timestamp":"2026-07-12T10:00:30.000Z","message":{"role":"user","content":"[Request interrupted by user]"}}`,
		`{"type":"user","timestamp":"2026-07-12T10:00:34.000Z","message":{"role":"user","content":"停,先做别的"}}`,
		`{"type":"assistant","timestamp":"2026-07-12T10:00:35.000Z","message":{"id":"msg_2","stop_reason":"end_turn","content":[{"type":"text","text":"好的。"}],"usage":{"input_tokens":60,"output_tokens":6}}}`,
	})
	if len(tr.Runs) != 2 {
		t.Fatalf("want 2 runs (interrupted + new intent), got %d", len(tr.Runs))
	}
	if tr.Runs[0].Status != RunInterrupted {
		t.Errorf("run #1 status = %q, want interrupted (never a fake completed)", tr.Runs[0].Status)
	}
	if tr.Runs[1].UserIntent == nil || tr.Runs[1].UserIntent.Text != "停,先做别的" {
		t.Fatalf("post-interrupt retype must open a NEW run, got %+v", tr.Runs[1].UserIntent)
	}
	if len(tr.Runs[0].Amendments) != 0 {
		t.Errorf("post-interrupt retype must NOT be an amendment: %+v", tr.Runs[0].Amendments)
	}
	// The interrupt marker itself is never a user bubble.
	for _, r := range tr.Runs {
		if r.UserIntent != nil && isInterruptMarker(r.UserIntent.Text) {
			t.Errorf("interrupt marker leaked as a human bubble: %q", r.UserIntent.Text)
		}
	}
}

// A transcript that simply STOPS mid-tool-loop (session killed) must degrade
// honestly — unterminated, never "completed", never a fabricated final answer.
func TestProjectAgentRuns_UnterminatedIsHonest(t *testing.T) {
	tr := loadClaude(t, []string{
		`{"type":"user","timestamp":"2026-07-12T10:00:00.000Z","message":{"role":"user","content":"开始"}}`,
		`{"type":"assistant","timestamp":"2026-07-12T10:00:01.000Z","message":{"id":"m1","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}],"usage":{"input_tokens":10,"output_tokens":1}}}`,
	})
	if len(tr.Runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(tr.Runs))
	}
	if tr.Runs[0].Status != RunUnterminated || !tr.Runs[0].Diagnostic.Unterminated {
		t.Errorf("status = %q (diag=%+v), want unterminated", tr.Runs[0].Status, tr.Runs[0].Diagnostic)
	}
}

// context_compacted survives as a segment (Codex GUI shows「上下文已自动压缩」); it is
// never a conversation turn.
func TestProjectAgentRuns_CodexCompactionIsSegment(t *testing.T) {
	tr := loadCodex(t, []string{
		`{"timestamp":"2026-07-12T10:00:00.000Z","type":"session_meta","payload":{"type":"session_meta","id":"fixture-0000-0000-0000-000000000001","cwd":"/tmp/p","timestamp":"2026-07-12T10:00:00.000Z"}}`,
		`{"timestamp":"2026-07-12T10:00:01.000Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-07-12T10:00:015.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"长任务"}]}}`,
		`{"timestamp":"2026-07-12T10:00:02.000Z","type":"event_msg","payload":{"type":"context_compacted"}}`,
		`{"timestamp":"2026-07-12T10:00:03.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
		`{"timestamp":"2026-07-12T10:00:04.000Z","type":"event_msg","payload":{"type":"task_complete"}}`,
	})
	if len(tr.Runs) != 1 {
		t.Fatalf("compaction must not open a turn; got %d runs", len(tr.Runs))
	}
	if got := countSegments(tr.Runs[0], BlockCompaction); got != 1 {
		t.Errorf("compaction segment = %d, want 1 (must survive an expanded trace)", got)
	}
}

// Idempotence + determinism: projecting twice yields identical ids/order/counts
// (the UI keys its expansion state on run.ID; a reload must not re-bind it).
func TestProjectAgentRuns_Deterministic(t *testing.T) {
	tr := loadClaude(t, []string{
		`{"type":"user","timestamp":"2026-07-12T10:00:00.000Z","message":{"role":"user","content":"a"}}`,
		`{"type":"assistant","timestamp":"2026-07-12T10:00:01.000Z","message":{"id":"m1","stop_reason":"end_turn","content":[{"type":"text","text":"b"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"user","timestamp":"2026-07-12T10:00:02.000Z","message":{"role":"user","content":"c"}}`,
		`{"type":"assistant","timestamp":"2026-07-12T10:00:03.000Z","message":{"id":"m2","stop_reason":"end_turn","content":[{"type":"text","text":"d"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
	})
	again := ProjectAgentRuns(tr)
	if len(again) != len(tr.Runs) {
		t.Fatalf("non-deterministic run count: %d vs %d", len(again), len(tr.Runs))
	}
	for i := range again {
		if again[i].ID != tr.Runs[i].ID || again[i].Index != tr.Runs[i].Index ||
			len(again[i].Segments) != len(tr.Runs[i].Segments) {
			t.Fatalf("run %d drifted between projections", i)
		}
	}
	if tr.Runs[0].ID == tr.Runs[1].ID {
		t.Errorf("run ids must be unique: %q", tr.Runs[0].ID)
	}
}

// ---------------------------------------------------------------------------
// Design-review counterexamples (codex, 2026-07-12). Each finding is nailed down as
// a regression assertion so the class of bug cannot come back.
// ---------------------------------------------------------------------------

// F1: a MISSING task_complete must not swallow the next independent request into the
// previous round. codex's `task_started` is the run-start fact that makes this decidable
// — the first cut of this design ignored it and guessed from "is a run open?".
func TestProjectAgentRuns_MissingTerminalDoesNotSwallowNextIntent(t *testing.T) {
	tr := loadCodex(t, []string{
		`{"timestamp":"2026-07-12T10:00:00.000Z","type":"session_meta","payload":{"type":"session_meta","id":"fixture-0000-0000-0000-000000000001","cwd":"/tmp/p","timestamp":"2026-07-12T10:00:00.000Z"}}`,
		`{"timestamp":"2026-07-12T10:00:01.000Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-07-12T10:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"第一件事"}]}}`,
		`{"timestamp":"2026-07-12T10:00:03.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"c1","arguments":"{}"}}`,
		// …no task_complete (crash / kill). A NEW task starts:
		`{"timestamp":"2026-07-12T10:05:00.000Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-07-12T10:05:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"第二件事"}]}}`,
		`{"timestamp":"2026-07-12T10:05:02.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"好"}]}}`,
		`{"timestamp":"2026-07-12T10:05:03.000Z","type":"event_msg","payload":{"type":"task_complete"}}`,
	})
	if len(tr.Runs) != 2 {
		t.Fatalf("want 2 runs (the un-terminated one + the new intent), got %d", len(tr.Runs))
	}
	if tr.Runs[0].Status != RunUnterminated {
		t.Errorf("run #1 status = %q, want unterminated (no fake completed)", tr.Runs[0].Status)
	}
	if len(tr.Runs[0].Amendments) != 0 {
		t.Errorf("the next independent request must NOT be swallowed as a steer: %+v", tr.Runs[0].Amendments)
	}
	if tr.Runs[1].UserIntent == nil || tr.Runs[1].UserIntent.Text != "第二件事" || tr.Runs[1].Index != 2 {
		t.Errorf("run #2 wrong: %+v", tr.Runs[1])
	}
	// F2: the tool whose result never arrived did NOT succeed.
	if tr.Runs[0].Diagnostic.PendingTools != 1 {
		t.Errorf("pending_tools = %d, want 1 (an interrupted tool must not read as done)", tr.Runs[0].Diagnostic.PendingTools)
	}
	if tr.Runs[0].Segments[0].ResultSeen {
		t.Errorf("a tool with no result must have ResultSeen=false (else the UI fabricates success)")
	}
}

// F3: an ABORTED run's trailing narration must NOT be promoted to a final answer, and a
// notification arriving after a real answer must not demote it back into the process.
// (This is why FinalAnswer is a domain fact from the runtime's yield, not "the last text".)
func TestProjectAgentRuns_FinalAnswerIsDomainFactNotPosition(t *testing.T) {
	aborted := loadCodex(t, []string{
		`{"timestamp":"2026-07-12T10:00:00.000Z","type":"session_meta","payload":{"type":"session_meta","id":"fixture-0000-0000-0000-000000000001","cwd":"/tmp/p","timestamp":"2026-07-12T10:00:00.000Z"}}`,
		`{"timestamp":"2026-07-12T10:00:01.000Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-07-12T10:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"做事"}]}}`,
		`{"timestamp":"2026-07-12T10:00:03.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"我先看看这些文件…"}]}}`,
		`{"timestamp":"2026-07-12T10:00:04.000Z","type":"event_msg","payload":{"type":"turn_aborted"}}`,
	})
	if len(aborted.Runs) != 1 || aborted.Runs[0].Status != RunInterrupted {
		t.Fatalf("want 1 interrupted run, got %+v", aborted.Runs)
	}
	if len(aborted.Runs[0].FinalAnswer) != 0 {
		t.Errorf("an aborted run's narration must not impersonate an answer: %+v", aborted.Runs[0].FinalAnswer)
	}
	if got := countSegments(aborted.Runs[0], BlockText); got != 1 {
		t.Errorf("the narration stays in the process trace (got %d text segments)", got)
	}

	// A real answer, followed by bookkeeping (usage) then task_complete → still the answer.
	answered := loadClaude(t, []string{
		`{"type":"user","timestamp":"2026-07-12T10:00:00.000Z","message":{"role":"user","content":"问题"}}`,
		`{"type":"assistant","timestamp":"2026-07-12T10:00:01.000Z","message":{"id":"m1","stop_reason":"tool_use","content":[{"type":"text","text":"我先查一下。"},{"type":"tool_use","id":"t1","name":"Read","input":{}}],"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"user","timestamp":"2026-07-12T10:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}`,
		`{"type":"assistant","timestamp":"2026-07-12T10:00:03.000Z","message":{"id":"m2","stop_reason":"end_turn","content":[{"type":"text","text":"答案是 42。"}],"usage":{"input_tokens":20,"output_tokens":2}}}`,
	})
	r := answered.Runs[0]
	if len(r.FinalAnswer) != 1 || r.FinalAnswer[0].Text != "答案是 42。" {
		t.Fatalf("FinalAnswer = %+v, want the end_turn text", r.FinalAnswer)
	}
	// The mid-run narration is process, not answer — even though it is also text.
	if got := countSegments(r, BlockText); got != 1 || r.Segments[0].Text != "我先查一下。" {
		t.Errorf("mid-run narration must stay in the trace, got %+v", r.Segments)
	}
}

// F6: usage is a TYPED aggregate with a per-field policy. Summing every number in the map
// (the first cut) inflated ttft/duration into values nobody measured and truncated cost.
func TestProjectAgentRuns_UsageFieldPolicy(t *testing.T) {
	tr := loadClaude(t, []string{
		`{"type":"user","timestamp":"2026-07-12T10:00:00.000Z","message":{"role":"user","content":"跑"}}`,
		`{"type":"assistant","timestamp":"2026-07-12T10:00:01.000Z","message":{"id":"m1","model":"claude-opus-4-8","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}],"usage":{"input_tokens":100,"output_tokens":10,"ttft_ms":800}}}`,
		`{"type":"user","timestamp":"2026-07-12T10:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}`,
		`{"type":"assistant","timestamp":"2026-07-12T10:00:03.000Z","message":{"id":"m2","model":"claude-sonnet-5","stop_reason":"end_turn","content":[{"type":"text","text":"完成"}],"usage":{"input_tokens":200,"output_tokens":20,"ttft_ms":900}}}`,
	})
	u := tr.Runs[0].Usage
	if u == nil {
		t.Fatal("no usage")
	}
	if u.InputTokens != 300 || u.OutputTokens != 30 {
		t.Errorf("tokens must SUM: got in=%d out=%d, want 300/30", u.InputTokens, u.OutputTokens)
	}
	if u.TTFTMs == nil || *u.TTFTMs != 800 {
		t.Errorf("ttft must be the FIRST iteration's (800), not a sum: %v", u.TTFTMs)
	}
	// A run that switched models reports both, honestly — and the model in force at the end.
	if u.Model != "claude-sonnet-5" || len(u.Models) != 2 {
		t.Errorf("model attribution wrong: model=%q models=%v", u.Model, u.Models)
	}
}

// The real 60 MB claude transcript: 17 real human intents (the number a person would count)
// vs 552 assistant adapter turns. Guarded by KIT_REALDATA_PROJECT so CI without the file
// skips rather than fails.
func TestProjectAgentRuns_RealClaudeTranscript(t *testing.T) {
	proj := os.Getenv("KIT_REALDATA_PROJECT")
	if proj == "" {
		t.Skip("set KIT_REALDATA_PROJECT to run against the real ~/.claude transcript")
	}
	src := NewClaudeSource()
	metas, err := src.ListSessions(context.Background(), proj)
	if err != nil || len(metas) == 0 {
		t.Skipf("no real claude sessions for %s (%v)", proj, err)
	}
	// biggest session = the interesting one
	big := metas[0]
	for _, m := range metas {
		if m.TurnCount > big.TurnCount {
			big = m
		}
	}
	tr, err := src.LoadTranscript(context.Background(), SessionRef{ProjectDir: proj, ID: big.ID})
	if err != nil {
		t.Fatal(err)
	}
	runs := ProjectAgentRuns(tr)
	if len(runs) == 0 {
		t.Fatal("no runs projected from a real transcript")
	}
	rawAsst := 0
	for _, tn := range tr.Turns {
		if tn.Role == "assistant" {
			rawAsst++
		}
	}
	// The whole point: runs ≪ assistant turns (each intent absorbs its tool loop).
	if len(runs) >= rawAsst {
		t.Errorf("projection did not aggregate: %d runs vs %d assistant turns", len(runs), rawAsst)
	}
	for i, r := range runs {
		if r.UserIntent == nil && !r.Diagnostic.NoIntent {
			t.Errorf("run %d has no intent but is not flagged", i)
		}
		if isInterruptMarker(intentText(r)) {
			t.Errorf("run %d: an interrupt marker leaked in as a human bubble", i)
		}
	}
	t.Logf("real claude: %d runs from %d assistant turns (%d raw turns)", len(runs), rawAsst, len(tr.Turns))
}

func intentText(r AgentRun) string {
	if r.UserIntent == nil {
		return ""
	}
	return r.UserIntent.Text
}
