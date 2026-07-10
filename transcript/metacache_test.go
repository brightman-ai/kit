package transcript

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 2026-07-03 轮询风暴修复：scanMeta 走 (path,size,mtime) 记忆化。
// 语义：同 size+mtime → 命中缓存不重解析；size 或 mtime 变 → 失效重扫。
// 观测手法：改写文件内容但把 size+mtime 恢复原值 → 返回旧结果 = 证明命中；
// 再把 mtime 前拨 → 返回新结果 = 证明失效。
func TestCodexScanMetaCacheHitAndInvalidate(t *testing.T) {
	root, id := writeCodexFixture(t)
	t.Setenv("DW_CODEX_HOME", root)
	src := NewCodexSource()

	files, err := src.rolloutFiles()
	if err != nil || len(files) != 1 {
		t.Fatalf("rolloutFiles: %v, n=%d", err, len(files))
	}
	path := files[0]

	m1, _, ok := src.scanMeta(path)
	if !ok || m1.ID != id {
		t.Fatalf("first scan: ok=%v id=%q", ok, m1.ID)
	}
	if m1.Title != "patch the file please" {
		t.Fatalf("first scan title=%q", m1.Title)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// 同长度换标题（size 不变），mtime 恢复 → 应命中缓存返回旧标题
	raw, _ := os.ReadFile(path)
	swapped := strings.Replace(string(raw), "patch the file please", "PATCH THE FILE PLEASE", 1)
	if len(swapped) != len(raw) {
		t.Fatalf("fixture swap must preserve size: %d vs %d", len(swapped), len(raw))
	}
	if err := os.WriteFile(path, []byte(swapped), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	m2, _, ok := src.scanMeta(path)
	if !ok {
		t.Fatal("second scan not ok")
	}
	if m2.Title != m1.Title {
		t.Fatalf("同 size+mtime 应命中缓存: got %q want %q", m2.Title, m1.Title)
	}

	// mtime 前拨 → 失效重扫 → 新标题
	if err := os.Chtimes(path, st.ModTime().Add(2*time.Second), st.ModTime().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	m3, _, ok := src.scanMeta(path)
	if !ok {
		t.Fatal("third scan not ok")
	}
	if m3.Title != "PATCH THE FILE PLEASE" {
		t.Fatalf("mtime 变应失效重扫: got %q", m3.Title)
	}
}

// claude 侧同一缓存管线：ListSessions 两次，第二次未变文件不重解析（用同 size+mtime
// 内容替换观测），mtime 变则取到新值。
func TestClaudeScanMetaCacheHitAndInvalidate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DW_CLAUDE_PROJECTS", home)
	src := NewClaudeSource()

	projectDir := "/tmp/proj-x"
	dir := src.projectDirPath(projectDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "aaaa1111-2222-3333-4444-555566667777.jsonl")
	line := `{"type":"user","timestamp":"2026-07-01T10:00:00.000Z","message":{"role":"user","content":"标题甲甲甲"}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	list1, err := src.ListSessions(context.Background(), projectDir)
	if err != nil || len(list1) != 1 {
		t.Fatalf("list1: %v n=%d", err, len(list1))
	}
	if list1[0].Title != "标题甲甲甲" {
		t.Fatalf("list1 title=%q", list1[0].Title)
	}

	st, _ := os.Stat(path)
	swapped := strings.Replace(line, "标题甲甲甲", "标题乙乙乙", 1)
	if len(swapped) != len(line) {
		t.Fatalf("swap must preserve size")
	}
	if err := os.WriteFile(path, []byte(swapped+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	list2, _ := src.ListSessions(context.Background(), projectDir)
	if len(list2) != 1 || list2[0].Title != "标题甲甲甲" {
		t.Fatalf("同 size+mtime 应命中缓存: %+v", list2)
	}

	if err := os.Chtimes(path, st.ModTime().Add(2*time.Second), st.ModTime().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	list3, _ := src.ListSessions(context.Background(), projectDir)
	if len(list3) != 1 || list3[0].Title != "标题乙乙乙" {
		t.Fatalf("mtime 变应失效重扫: %+v", list3)
	}
}
