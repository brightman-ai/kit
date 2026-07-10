package transcript

import (
	"context"
	"os"
	"sort"
	"testing"
)

// TestCodexSource_RealTranscript parses real codex rollout transcripts of THIS
// repo when present, printing parse statistics (CHG-014 runtime evidence). It is
// skipped automatically when ~/.codex/sessions is absent (fresh CI boxes), so it
// never fails a clean checkout — a live-evidence probe, not a gate.
func TestCodexSource_RealTranscript(t *testing.T) {
	projectDir := os.Getenv("KIT_REALDATA_PROJECT")
	if projectDir == "" {
		t.Skip("KIT_REALDATA_PROJECT unset — skipping real-data live-evidence probe")
	}
	src := NewCodexSource()
	if _, err := os.Stat(src.sessionsDir()); err != nil {
		t.Skipf("no real codex SSOT (%v) — skipping live evidence probe", err)
	}

	ctx := context.Background()
	metas, err := src.ListSessions(ctx, projectDir)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	t.Logf("LIST: %d codex sessions for %s", len(metas), projectDir)
	if len(metas) == 0 {
		t.Skip("no codex sessions for this project dir — nothing to prove")
	}

	// Newest first, then print the first 3 (id + title proof).
	sort.SliceStable(metas, func(i, j int) bool {
		return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
	})
	for i, m := range metas {
		if i >= 3 {
			break
		}
		t.Logf("  - %s | turns=%-3d | %q", shortID(m.ID), m.TurnCount, m.Title)
	}

	// Load the first session's transcript and tally the parsed block types.
	tr, err := src.LoadTranscript(ctx, SessionRef{ProjectDir: projectDir, ID: metas[0].ID})
	if err != nil {
		t.Fatalf("LoadTranscript(%s): %v", metas[0].ID, err)
	}
	blockTypes := map[string]int{}
	toolNames := map[string]int{}
	toolsWithResult := 0
	for ti := range tr.Turns {
		for bi := range tr.Turns[ti].Blocks {
			b := &tr.Turns[ti].Blocks[bi]
			blockTypes[b.Type]++
			if b.Type == BlockTool {
				toolNames[b.ToolName]++
				if b.ToolResult != "" {
					toolsWithResult++
				}
			}
		}
	}
	keys := make([]string, 0, len(blockTypes))
	for k := range blockTypes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	t.Logf("===== TRANSCRIPT %s =====", shortID(metas[0].ID))
	t.Logf("title           : %q", tr.Title)
	t.Logf("turns           : %d", len(tr.Turns))
	t.Logf("block dist      :")
	for _, k := range keys {
		t.Logf("    %-12s %d", k, blockTypes[k])
	}
	t.Logf("tool calls w/ result: %d", toolsWithResult)
	tnames := make([]string, 0, len(toolNames))
	for k := range toolNames {
		tnames = append(tnames, k)
	}
	sort.Strings(tnames)
	t.Logf("tool names      : %v", tnames)

	if len(tr.Turns) == 0 {
		t.Fatal("expected non-zero turns from a real codex transcript")
	}
	if blockTypes[BlockUser] == 0 {
		t.Fatal("expected at least one user bubble from a real codex transcript")
	}
}
