package transcript

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// plantCodexTree writes nFiles synthetic rollout-*.jsonl across nDirs project
// cwds. Each file gets a session_meta first line + bodyLines large body lines so
// a full parse (scanMeta) is expensive while a first-line read is cheap — the
// exact shape that made GET /api/workspaces O(workspaces × bytes).
func plantCodexTree(t testing.TB, root string, nDirs, perDir, bodyLines int) []string {
	t.Helper()
	sessions := filepath.Join(root, "sessions", "2026", "06", "14")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	bigText := strings.Repeat("x", 4096) // ~4 KB per body line
	dirs := make([]string, nDirs)
	for d := 0; d < nDirs; d++ {
		cwd := fmt.Sprintf("/home/u/proj-%d", d)
		dirs[d] = cwd
		for k := 0; k < perDir; k++ {
			id := fmt.Sprintf("0000%04d-0000-0000-0000-00000000%04d", d, k)
			name := "rollout-2026-06-14T00-00-00-" + id + ".jsonl"
			var b strings.Builder
			fmt.Fprintf(&b, `{"timestamp":"2026-06-14T00:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":%q,"timestamp":"2026-06-14T00:00:00Z"}}`+"\n", id, cwd)
			for i := 0; i < bodyLines; i++ {
				fmt.Fprintf(&b, `{"timestamp":"2026-06-14T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}}`+"\n", bigText)
			}
			if err := os.WriteFile(filepath.Join(sessions, name), []byte(b.String()), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dirs
}

// TestCodexCountByDir_Correctness verifies the cheap sweep returns the same
// per-dir counts the (expensive) ListSessions path would, and that the cache
// invalidates on a new file.
func TestCodexCountByDir_Correctness(t *testing.T) {
	root := t.TempDir()
	dirs := plantCodexTree(t, root, 4, 3, 5) // 4 dirs × 3 files = 12 files
	src := &CodexSource{Root: root}
	ctx := context.Background()

	got, err := src.CountSessionsByDir(ctx)
	if err != nil {
		t.Fatalf("CountSessionsByDir: %v", err)
	}
	for _, cwd := range dirs {
		if got[cwd] != 3 {
			t.Errorf("cwd %s: got %d, want 3", cwd, got[cwd])
		}
		// Cross-check against the full-parse path.
		metas, _ := src.ListSessions(ctx, cwd)
		if len(metas) != got[cwd] {
			t.Errorf("cwd %s: sweep=%d ListSessions=%d (must agree)", cwd, got[cwd], len(metas))
		}
	}

	// Cached call returns same result.
	got2, _ := src.CountSessionsByDir(ctx)
	if got2[dirs[0]] != 3 {
		t.Errorf("cached sweep mismatch: %d", got2[dirs[0]])
	}

	// New file under an existing cwd → fingerprint flips → count updates.
	time.Sleep(2 * time.Millisecond)
	extra := filepath.Join(root, "sessions", "2026", "06", "14",
		"rollout-2026-06-14T00-00-00-99990000-0000-0000-0000-000000009999.jsonl")
	os.WriteFile(extra, []byte(`{"type":"session_meta","payload":{"id":"x","cwd":"`+dirs[0]+`"}}`+"\n"), 0o644)
	src.cacheExpireAt = time.Time{} // force TTL miss so fingerprint is re-checked
	got3, _ := src.CountSessionsByDir(ctx)
	if got3[dirs[0]] != 4 {
		t.Errorf("after planting 1 file: got %d, want 4 (cache failed to invalidate)", got3[dirs[0]])
	}
}

// BenchmarkWorkspaceCount_OldVsNew contrasts the two strategies on the same
// planted tree: OLD = call ListSessions(rootDir) once per workspace (full parse
// of every file each call → O(W×bytes)); NEW = one CountSessionsByDir sweep
// (first-line only) serving all workspaces.
//
// Run: go test ./internal/sessionsource/ -run x -bench WorkspaceCount -benchmem
func BenchmarkWorkspaceCount_OldVsNew(b *testing.B) {
	root := b.TempDir()
	// 20 workspaces × 20 files = 400 files, 40 body lines each (~160 KB/file).
	dirs := plantCodexTree(b, root, 20, 20, 40)
	ctx := context.Background()

	b.Run("OLD_perWorkspace_fullParse", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			src := &CodexSource{Root: root} // fresh = no cache, mirrors old buildAggregator
			total := 0
			for _, cwd := range dirs { // one ListSessions per workspace
				metas, _ := src.ListSessions(ctx, cwd)
				total += len(metas)
			}
			if total != len(dirs)*20 {
				b.Fatalf("bad total %d", total)
			}
		}
	})

	b.Run("NEW_singleSweep_firstLine", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			src := &CodexSource{Root: root}
			byDir, _ := src.CountSessionsByDir(ctx) // one sweep
			total := 0
			for _, cwd := range dirs { // O(1) lookups
				total += byDir[cwd]
			}
			if total != len(dirs)*20 {
				b.Fatalf("bad total %d", total)
			}
		}
	})
}
