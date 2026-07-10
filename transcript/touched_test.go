package transcript

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestExtractTouched covers the allowlist + jail invariants: only edit tools name a
// file, Read counts only for images, Bash is excluded, out-of-root is clamped away,
// dedup keeps the newest tool, and agent subflow children are walked.
func TestExtractTouched(t *testing.T) {
	root := t.TempDir()
	// real files (existence-checked); one dir (must be dropped).
	mustWrite(t, filepath.Join(root, "a.go"), "package a")
	mustWrite(t, filepath.Join(root, "img.png"), "PNG")
	mustWrite(t, filepath.Join(root, "b.txt"), "b")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "sub", "c.go"), "package c")

	t0 := time.UnixMilli(1000)
	t1 := time.UnixMilli(2000)
	tr := &Transcript{Turns: []Turn{
		{At: &t0, Blocks: []Block{
			// edit tool → counts
			{Type: BlockTool, ToolName: "Write", ToolInput: map[string]any{"file_path": "a.go"}},
			// Read of non-image → excluded
			{Type: BlockTool, ToolName: "Read", ToolInput: map[string]any{"file_path": "b.txt"}},
			// Read of image → counts
			{Type: BlockTool, ToolName: "Read", ToolInput: map[string]any{"file_path": "img.png"}},
			// Bash → excluded (no file_path key anyway)
			{Type: BlockTool, ToolName: "Bash", ToolInput: map[string]any{"command": "rm -rf /"}},
			// out-of-root absolute → clamped away
			{Type: BlockTool, ToolName: "Edit", ToolInput: map[string]any{"file_path": "/etc/passwd"}},
			// agent subflow child edit → walked
			{Type: BlockAgent, ToolName: "Agent", Children: []Block{
				{Type: BlockTool, ToolName: "Edit", ToolInput: map[string]any{"file_path": "sub/c.go"}},
			}},
		}},
		// newer turn re-edits a.go → dedup keeps this tool (Edit) + newest ts
		{At: &t1, Blocks: []Block{
			{Type: BlockTool, ToolName: "Edit", ToolInput: map[string]any{"file_path": "a.go"}},
		}},
	}}

	got := ExtractTouched(tr, root)
	byPath := map[string]TouchedFile{}
	for _, f := range got {
		byPath[f.Path] = f
	}

	if _, ok := byPath["b.txt"]; ok {
		t.Error("Read of non-image b.txt should be excluded")
	}
	if _, ok := byPath[filepath.Join("..", "etc", "passwd")]; ok {
		t.Error("out-of-root /etc/passwd must be clamped away")
	}
	for _, want := range []string{"a.go", "img.png", filepath.Join("sub", "c.go")} {
		if _, ok := byPath[want]; !ok {
			t.Errorf("expected touched file %q, missing from %v", want, keys(byPath))
		}
	}
	if f := byPath["a.go"]; f.Tool != "Edit" || f.TouchedAt != 2000 {
		t.Errorf("dedup should keep newest a.go tool=Edit ts=2000, got tool=%q ts=%d", f.Tool, f.TouchedAt)
	}
	// newest-first ordering: a.go (ts 2000) before the ts-1000 files.
	if len(got) > 0 && got[0].Path != "a.go" {
		t.Errorf("expected newest-first: a.go first, got %q", got[0].Path)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]TouchedFile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
