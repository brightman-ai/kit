package transcript

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeCodexFixture writes a synthetic codex rollout-*.jsonl under a temp
// DW_CODEX_HOME and returns the embedded session id. The fixture exercises the
// two CHG-acceptance gaps: an apply_patch custom_tool_call (path embedded in the
// patch body) and an event_msg/token_count carrying per-turn usage.
func writeCodexFixture(t *testing.T) (root, id string) {
	t.Helper()
	root = t.TempDir()
	id = "0199aaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	dir := filepath.Join(root, "sessions", "2026", "06", "18")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cwd := "/home/ubuntu/code/stwork/deepwork-pro"
	lines := []string{
		`{"timestamp":"2026-06-18T10:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","timestamp":"2026-06-18T10:00:00.000Z","cwd":"` + cwd + `"}}`,
		// real user turn
		`{"timestamp":"2026-06-18T10:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"patch the file please"}]}}`,
		// apply_patch custom_tool_call — path is inside the patch body
		`{"timestamp":"2026-06-18T10:00:02.000Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"call_patch1","input":"*** Begin Patch\n*** Update File: internal/sessionsource/codex.go\n@@\n-old\n+new\n*** End Patch"}}`,
		`{"timestamp":"2026-06-18T10:00:03.000Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_patch1","output":"Success. Updated the file."}}`,
		// assistant reply
		`{"timestamp":"2026-06-18T10:00:04.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}}`,
		// rate-limit-only token_count (info=null) → MUST be ignored, no usage block
		`{"timestamp":"2026-06-18T10:00:05.000Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex"}}}`,
		// real per-turn usage token_count
		`{"timestamp":"2026-06-18T10:00:06.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":5000,"cached_input_tokens":4000,"output_tokens":200,"reasoning_output_tokens":40,"total_tokens":5240},"last_token_usage":{"input_tokens":1200,"cached_input_tokens":1000,"output_tokens":80,"reasoning_output_tokens":20,"total_tokens":1300}}}}`,
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	path := filepath.Join(dir, "rollout-2026-06-18T10-00-00-"+id+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return root, id
}

// TestCodexSource_SyntheticUsageAndPatchPath asserts the two CHG-acceptance
// gaps are closed against a controlled synthetic turn:
//  1. event_msg/token_count → a usage block (claude-shaped keys) + Meta totals.
//  2. apply_patch → tool_input["path"] extracted from the patch body.
func TestCodexSource_SyntheticUsageAndPatchPath(t *testing.T) {
	root, id := writeCodexFixture(t)
	t.Setenv("DW_CODEX_HOME", root)

	src := NewCodexSource()
	tr, err := src.LoadTranscript(context.Background(), SessionRef{
		ProjectDir: "/home/ubuntu/code/stwork/deepwork-pro", ID: id,
	})
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}

	var (
		usageBlocks   int
		gotUsage      map[string]interface{}
		patchPath     string
		patchHasInput bool
	)
	for ti := range tr.Turns {
		for bi := range tr.Turns[ti].Blocks {
			b := &tr.Turns[ti].Blocks[bi]
			switch b.Type {
			case BlockUsage:
				usageBlocks++
				gotUsage = b.Usage
			case BlockTool:
				if b.ToolName == "apply_patch" {
					if p, ok := b.ToolInput["path"].(string); ok {
						patchPath = p
					}
					_, patchHasInput = b.ToolInput["input"]
				}
			}
		}
	}

	// ── Gap 1: usage block from token_count ──────────────────────────────────
	if usageBlocks != 1 {
		t.Fatalf("expected exactly 1 usage block (rate-limit-only token_count must be skipped), got %d", usageBlocks)
	}
	if got := intField(gotUsage, "input_tokens"); got != 1200 {
		t.Errorf("usage input_tokens: want 1200, got %d (usage=%v)", got, gotUsage)
	}
	// output folds reasoning_output_tokens: 80 + 20 = 100
	if got := intField(gotUsage, "output_tokens"); got != 100 {
		t.Errorf("usage output_tokens (incl reasoning): want 100, got %d (usage=%v)", got, gotUsage)
	}
	// codex cached_input_tokens → SSOT key cache_read_input_tokens
	if got := intField(gotUsage, "cache_read_input_tokens"); got != 1000 {
		t.Errorf("usage cache_read_input_tokens: want 1000, got %d (usage=%v)", got, gotUsage)
	}
	if _, leaked := gotUsage["cached_input_tokens"]; leaked {
		t.Errorf("codex-native key cached_input_tokens leaked; must remap to cache_read_input_tokens (usage=%v)", gotUsage)
	}

	// ── Meta totals (footer session sum) ─────────────────────────────────────
	if got := intField(tr.Meta, "input_tokens"); got != 1200 {
		t.Errorf("Meta input_tokens: want 1200, got %d", got)
	}
	if got := intField(tr.Meta, "output_tokens"); got != 100 {
		t.Errorf("Meta output_tokens: want 100, got %d", got)
	}
	if got := intField(tr.Meta, "cache_read_tokens"); got != 1000 {
		t.Errorf("Meta cache_read_tokens: want 1000, got %d", got)
	}

	// ── Gap 2: apply_patch path extraction ───────────────────────────────────
	if patchPath != "internal/sessionsource/codex.go" {
		t.Errorf("apply_patch tool_input.path: want %q, got %q", "internal/sessionsource/codex.go", patchPath)
	}
	if !patchHasInput {
		t.Errorf("apply_patch tool_input must still carry the raw patch under \"input\" (path is additive)")
	}
}

// TestCodexPatchPath unit-tests the path extractor against every File-marker
// variant observed in the real corpus (Update/Add/Delete/Move + the no-marker
// honest-empty case).
func TestCodexPatchPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"*** Begin Patch\n*** Update File: a/b.go\n@@\n+x\n*** End Patch", "a/b.go"},
		{"*** Begin Patch\n*** Add File: tests/e2e/README.md\n+hello\n*** End Patch", "tests/e2e/README.md"},
		{"*** Begin Patch\n*** Delete File: old/gone.txt\n*** End Patch", "old/gone.txt"},
		{"*** Begin Patch\n*** Move to: new/path.go\n*** End Patch", "new/path.go"},
		{"*** Begin Patch\n@@\n+no marker here\n*** End Patch", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := codexPatchPath(c.in); got != c.want {
			t.Errorf("codexPatchPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
