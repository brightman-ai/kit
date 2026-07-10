package transcript

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestClaudeCoalesceSplitMessage pins the message.id coalescing invariant: claude writes
// ONE assistant message's thinking/text/tool_use blocks as SEPARATE jsonl lines that all
// share message.id (possibly across an intervening tool_result user line — the observed
// non-consecutive shape) and each REPEAT the full usage. They must fold into ONE turn (one
// bubble), and the usage must count ONCE — not once per split line.
//
// Regression it guards: a single logical answer rendered as N bubbles (an empty
// thinking-only "ghost" + the text) and turn token totals inflated N×.
func TestClaudeCoalesceSplitMessage(t *testing.T) {
	dir := t.TempDir()
	projectDir := "/x/proj"
	src := &ClaudeSource{Root: dir}
	pdir := src.projectDirPath(projectDir)
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}

	usageA := map[string]any{"input_tokens": 10, "output_tokens": 100, "cache_read_input_tokens": 5}
	usageB := map[string]any{"input_tokens": 20, "output_tokens": 50, "cache_read_input_tokens": 5}
	amsg := func(id string, content []any, usage map[string]any) map[string]any {
		return map[string]any{"id": id, "model": "claude-sonnet-5", "role": "assistant", "content": content, "usage": usage}
	}
	lines := []map[string]any{
		{"type": "user", "timestamp": "2026-07-10T00:00:00Z", "message": map[string]any{"role": "user", "content": "写一篇散文"}},
		// message A: thinking + text on TWO lines, same id, usage duplicated.
		{"type": "assistant", "timestamp": "2026-07-10T00:00:01Z", "message": amsg("msg_A", []any{map[string]any{"type": "thinking", "thinking": "先想想"}}, usageA)},
		{"type": "assistant", "timestamp": "2026-07-10T00:00:02Z", "message": amsg("msg_A", []any{map[string]any{"type": "text", "text": "答案正文"}}, usageA)},
		// message B: tool_use then (after a tool_result user line) text — same id, NON-consecutive.
		{"type": "assistant", "timestamp": "2026-07-10T00:00:03Z", "message": amsg("msg_B", []any{map[string]any{"type": "tool_use", "id": "tu_1", "name": "Grep", "input": map[string]any{"pattern": "x"}}}, usageB)},
		{"type": "user", "timestamp": "2026-07-10T00:00:04Z", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "命中3处"}}}},
		{"type": "assistant", "timestamp": "2026-07-10T00:00:05Z", "message": amsg("msg_B", []any{map[string]any{"type": "text", "text": "工具后总结"}}, usageB)},
	}
	var buf []byte
	for _, l := range lines {
		b, _ := json.Marshal(l)
		buf = append(append(buf, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(pdir, "sess1.jsonl"), buf, 0o644); err != nil {
		t.Fatal(err)
	}

	tr, err := src.LoadTranscript(context.Background(), SessionRef{ProjectDir: projectDir, ID: "sess1"})
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}

	// 3 coalesced turns (user + msgA + msgB), NOT 5 bubbles.
	if len(tr.Turns) != 3 {
		t.Fatalf("want 3 coalesced turns, got %d: %v", len(tr.Turns), turnsSummary(tr))
	}

	// msgA: thinking + text folded into one turn, exactly one usage block.
	a := tr.Turns[1]
	if a.Role != "assistant" {
		t.Fatalf("turn1 role = %s, want assistant", a.Role)
	}
	if n := countBlocks(a, BlockThinking); n != 1 {
		t.Fatalf("msgA thinking blocks = %d, want 1 (%v)", n, turnsSummary(tr))
	}
	if n := countBlocks(a, BlockText); n != 1 {
		t.Fatalf("msgA text blocks = %d, want 1", n)
	}
	if n := countBlocks(a, BlockUsage); n != 1 {
		t.Fatalf("msgA usage blocks = %d, want 1 (dedup)", n)
	}

	// msgB: tool (with tool_result attached ACROSS the coalesce) + text, one usage.
	b := tr.Turns[2]
	if n := countBlocks(b, BlockTool); n != 1 {
		t.Fatalf("msgB tool blocks = %d, want 1", n)
	}
	toolResult := ""
	for i := range b.Blocks {
		if b.Blocks[i].Type == BlockTool {
			toolResult = b.Blocks[i].ToolResult
		}
	}
	if toolResult != "命中3处" {
		t.Fatalf("tool_result not attached across coalesce: %q", toolResult)
	}
	if n := countBlocks(b, BlockText); n != 1 {
		t.Fatalf("msgB text blocks = %d, want 1", n)
	}
	if n := countBlocks(b, BlockUsage); n != 1 {
		t.Fatalf("msgB usage blocks = %d, want 1 (dedup)", n)
	}

	// Usage counted ONCE per message.id: out = 100(A)+50(B)=150, NOT 250/300 from the
	// duplicated split lines; in = 30; cache = 10.
	if tr.Meta["output_tokens"] != 150 {
		t.Fatalf("output_tokens = %v, want 150 (dedup, not inflated)", tr.Meta["output_tokens"])
	}
	if tr.Meta["input_tokens"] != 30 {
		t.Fatalf("input_tokens = %v, want 30", tr.Meta["input_tokens"])
	}
	if tr.Meta["cache_read_tokens"] != 10 {
		t.Fatalf("cache_read_tokens = %v, want 10", tr.Meta["cache_read_tokens"])
	}
}

func countBlocks(turn Turn, typ string) int {
	n := 0
	for i := range turn.Blocks {
		if turn.Blocks[i].Type == typ {
			n++
		}
	}
	return n
}

func turnsSummary(tr *Transcript) []string {
	out := make([]string, 0, len(tr.Turns))
	for i := range tr.Turns {
		types := ""
		for j := range tr.Turns[i].Blocks {
			types += tr.Turns[i].Blocks[j].Type + ","
		}
		out = append(out, tr.Turns[i].Role+":["+types+"]")
	}
	return out
}
