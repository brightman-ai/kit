package transcript

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestClaudeSource_RealTranscript parses a real claude transcript of THIS repo
// when present, printing the parse statistics (CHG-014 runtime evidence). It is
// skipped automatically when the SSOT directory is absent (e.g. fresh CI boxes),
// so it never fails a clean checkout — it is a live-evidence probe, not a gate.
func TestClaudeSource_RealTranscript(t *testing.T) {
	projectDir := os.Getenv("KIT_REALDATA_PROJECT")
	if projectDir == "" {
		t.Skip("KIT_REALDATA_PROJECT unset — skipping real-data live-evidence probe")
	}
	src := NewClaudeSource()
	if _, err := os.Stat(src.projectDirPath(projectDir)); err != nil {
		t.Skipf("no real claude SSOT for %s (%v) — skipping live evidence probe", projectDir, err)
	}

	ctx := context.Background()
	metas, err := src.ListSessions(ctx, projectDir)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	t.Logf("LIST: %d claude sessions for %s", len(metas), projectDir)
	for _, m := range metas {
		t.Logf("  - %s | turns=%-3d | %q", m.ID[:8], m.TurnCount, m.Title)
	}

	// Pick the largest jsonl as the complex-transcript proof.
	largest := pickLargest(t, src.projectDirPath(projectDir))
	if largest == "" {
		t.Skip("no jsonl found")
	}
	tr, err := src.LoadTranscript(ctx, SessionRef{ProjectDir: projectDir, ID: largest})
	if err != nil {
		t.Fatalf("LoadTranscript(%s): %v", largest, err)
	}

	blockTypes := map[string]int{}
	agentTurns, agentBlocks := 0, 0
	toolNames := map[string]int{}
	var sampleAgent *Block
	for ti := range tr.Turns {
		hasAgent := false
		for bi := range tr.Turns[ti].Blocks {
			b := &tr.Turns[ti].Blocks[bi]
			blockTypes[b.Type]++
			switch b.Type {
			case BlockAgent:
				hasAgent = true
				agentBlocks++
				toolNames["Agent:"+b.SubagentType]++
				if sampleAgent == nil {
					sampleAgent = b
				}
			case BlockTool:
				toolNames[b.ToolName]++
			}
		}
		if hasAgent {
			agentTurns++
		}
	}

	keys := make([]string, 0, len(blockTypes))
	for k := range blockTypes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	t.Logf("===== COMPLEX TRANSCRIPT %s =====", largest[:8])
	t.Logf("title           : %q", tr.Title)
	t.Logf("turns           : %d", len(tr.Turns))
	t.Logf("turns w/ Agent  : %d (subagent dispatches)", agentTurns)
	t.Logf("agent blocks    : %d", agentBlocks)
	t.Logf("block dist      :")
	for _, k := range keys {
		t.Logf("    %-12s %d", k, blockTypes[k])
	}
	t.Logf("token meta      : in=%v out=%v cacheRead=%v",
		tr.Meta["input_tokens"], tr.Meta["output_tokens"], tr.Meta["cache_read_tokens"])
	if sampleAgent != nil {
		t.Logf("agent sample    : subagent=%q desc=%q resultLen=%d",
			sampleAgent.SubagentType, sampleAgent.Description, len(sampleAgent.ToolResult))
	}

	// Sanity: a complex transcript must have parsed turns and typed blocks.
	if len(tr.Turns) == 0 {
		t.Fatal("expected non-zero turns from a real transcript")
	}
	if blockTypes[BlockText] == 0 && blockTypes[BlockTool] == 0 {
		t.Fatal("expected text/tool blocks from a real transcript")
	}
}

func pickLargest(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestSize int64
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() > bestSize {
			bestSize = info.Size()
			best = e.Name()
		}
	}
	if best == "" {
		return ""
	}
	return best[:len(best)-len(".jsonl")]
}
