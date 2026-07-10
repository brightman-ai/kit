package transcript

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeDWFile writes a minimal dw-<id>.jsonl with one user + one assistant turn
// carrying thinking/tool/usage blocks (the v1.1 shape the writer produces).
func writeDWFile(t *testing.T, dir, id, userText string) string {
	t.Helper()
	path := filepath.Join(dir, "dw-"+id+".jsonl")
	lines := []string{
		`{"format":"deepwork.native_transcript.v1.1","type":"user","sessionId":"` + id + `","timestamp":"2026-06-17T01:00:00Z","message":{"role":"user","content":[{"type":"text","text":"` + userText + `"}]}}`,
		`{"format":"deepwork.native_transcript.v1.1","type":"assistant","sessionId":"` + id + `","timestamp":"2026-06-17T01:00:05Z","message":{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"let me reason"},` +
			`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}},` +
			`{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"file.txt"}]},` +
			`{"type":"text","text":"here is the answer"}` +
			`],"usage":{"input_tokens":120,"output_tokens":42,"cache_read_tokens":10}}}`,
		`{"format":"deepwork.native_transcript.v1.1","type":"result","sessionId":"` + id + `","timestamp":"2026-06-17T01:00:06Z","metrics":{"ttft_ms":300,"duration_ms":1100}}`,
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestDeepworkListSessions_DirectoryAsIndex is the P3 核心: with NO DB provider
// (simulating a deleted/empty DB), ListSessions must still enumerate every
// dw-<id>.jsonl from the transcript directory (目录即索引).
func TestDeepworkListSessions_DirectoryAsIndex(t *testing.T) {
	dir := t.TempDir()
	writeDWFile(t, dir, "501", "first question")
	writeDWFile(t, dir, "502", "second question")
	// a non-dw file must be ignored
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// provider == nil → the DB is gone; only the directory remains.
	src := NewDeepworkSourceWithDir(nil, 7, dir)
	metas, err := src.ListSessions(context.Background(), "/some/project")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("want 2 sessions from directory scan, got %d", len(metas))
	}
	byID := map[string]SessionMeta{}
	for _, m := range metas {
		byID[m.ID] = m
	}
	if m, ok := byID["501"]; !ok || m.Title != "first question" || m.TurnCount != 1 {
		t.Fatalf("501 meta wrong: %+v ok=%v", m, ok)
	}
	if m := byID["501"]; m.SsotPath != filepath.Join(dir, "dw-501.jsonl") {
		t.Fatalf("501 ssot path wrong: %q", m.SsotPath)
	}
}

// TestDeepworkLoadTranscript_FromFile verifies full block recovery from the file
// (thinking + tool+result + text + usage) — the content half of "删 DB 不丢内容".
func TestDeepworkLoadTranscript_FromFile(t *testing.T) {
	dir := t.TempDir()
	writeDWFile(t, dir, "777", "explain")

	src := NewDeepworkSourceWithDir(nil, 1, dir)
	tr, err := src.LoadTranscript(context.Background(), SessionRef{ID: "777"})
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	if len(tr.Turns) != 2 {
		t.Fatalf("want 2 turns (user+assistant), got %d", len(tr.Turns))
	}
	// user turn
	if tr.Turns[0].Role != "user" || tr.Turns[0].Blocks[0].Type != BlockUser {
		t.Fatalf("user turn malformed: %+v", tr.Turns[0])
	}
	// assistant turn: thinking, tool(with result), text, usage
	asst := tr.Turns[1]
	var sawThinking, sawTool, sawText, sawUsage bool
	for _, b := range asst.Blocks {
		switch b.Type {
		case BlockThinking:
			sawThinking = b.Text == "let me reason"
		case BlockTool:
			sawTool = b.ToolName == "Bash" && b.ToolResult == "file.txt"
		case BlockText:
			sawText = b.Text == "here is the answer"
		case BlockUsage:
			// usageMap stores ints (the @ce/workArea usageInt consumer handles
			// both int and float64); assert via a type-agnostic read.
			sawUsage = usageVal(b.Usage, "input_tokens") == 120 && usageVal(b.Usage, "output_tokens") == 42
		}
	}
	if !sawThinking || !sawTool || !sawText || !sawUsage {
		t.Fatalf("blocks missing: thinking=%v tool=%v text=%v usage=%v blocks=%+v",
			sawThinking, sawTool, sawText, sawUsage, asst.Blocks)
	}
	// usage totals surfaced on Meta (workArea reconstruction source).
	if got := tr.Meta["input_tokens"]; got != 120 {
		t.Fatalf("meta input_tokens want 120, got %v", got)
	}
}

// TestDeepworkLoadTranscript_ModelAndDurationInlined is the SSOT footer-parity guard:
// the replay usage block must carry the turn's MODEL (from message.model) and 总耗时
// (from the result line's duration) so a replayed turn's rmeta footer shows the SAME
// model + wall clock the live stream did (no live≠replay drift). Both otherwise died
// on the read path: model was never surfaced, and the duration lived on the skipped
// result line.
func TestDeepworkLoadTranscript_ModelAndDurationInlined(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dw-888.jsonl")
	lines := "" +
		`{"format":"deepwork.native_transcript.v1.1","type":"user","sessionId":"888","timestamp":"2026-06-17T01:00:00Z","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}` + "\n" +
		`{"format":"deepwork.native_transcript.v1.1","type":"assistant","sessionId":"888","timestamp":"2026-06-17T01:00:05Z","message":{"role":"assistant","model":"claude-sonnet-5","content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":10,"output_tokens":3,"ttft_ms":200}}}` + "\n" +
		`{"format":"deepwork.native_transcript.v1.1","type":"result","sessionId":"888","timestamp":"2026-06-17T01:00:06Z","duration_ms":4200,"metrics":{"ttft_ms":200,"duration_ms":4200}}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	src := NewDeepworkSourceWithDir(nil, 1, dir)
	tr, err := src.LoadTranscript(context.Background(), SessionRef{ID: "888"})
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	asst := tr.Turns[len(tr.Turns)-1]
	var usage map[string]interface{}
	for _, b := range asst.Blocks {
		if b.Type == BlockUsage {
			usage = b.Usage
		}
	}
	if usage == nil {
		t.Fatalf("assistant turn has no usage block: %+v", asst.Blocks)
	}
	if got, _ := usage["model"].(string); got != "claude-sonnet-5" {
		t.Errorf("usage.model: got %v, want claude-sonnet-5 — replay footer would show no model", usage["model"])
	}
	if usageVal(usage, "duration_ms") != 4200 {
		t.Errorf("usage.duration_ms: got %v, want 4200 — replay footer 总耗时 would be「—」", usage["duration_ms"])
	}
	// 总耗时 must never read SMALLER than TTFT (impossible) — the result duration is the
	// wall clock, ttft its subset.
	if usageVal(usage, "duration_ms") < usageVal(usage, "ttft_ms") {
		t.Errorf("总耗时 %v < TTFT %v — impossible", usage["duration_ms"], usage["ttft_ms"])
	}
	if got, _ := tr.Meta["model"].(string); got != "claude-sonnet-5" {
		t.Errorf("tr.Meta[model]: got %v, want claude-sonnet-5 (session-level fallback)", tr.Meta["model"])
	}
}

// usageVal reads an int/float64 usage value type-agnostically (parity with the
// webui usageInt consumer).
func usageVal(u map[string]interface{}, key string) int {
	switch n := u[key].(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// TestDeepworkListSessions_DBOverlayMerge verifies the DB row is authoritative
// (title rename) while orphan files (DB-unknown) are still recovered from disk.
func TestDeepworkListSessions_DBOverlayMerge(t *testing.T) {
	dir := t.TempDir()
	writeDWFile(t, dir, "10", "from db known")  // DB knows this
	writeDWFile(t, dir, "20", "orphan on disk") // DB does NOT know this

	prov := &fakeProvider{rows: []DeepworkSessionMeta{
		{ID: 10, Title: "DB Title For 10", TurnCount: 9},
	}}
	src := NewDeepworkSourceWithDir(prov, 1, dir)
	metas, err := src.ListSessions(context.Background(), "/p")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("want 2 (1 db + 1 orphan), got %d", len(metas))
	}
	byID := map[string]SessionMeta{}
	for _, m := range metas {
		byID[m.ID] = m
	}
	if byID["10"].Title != "DB Title For 10" || byID["10"].TurnCount != 9 {
		t.Fatalf("DB row should win for known session: %+v", byID["10"])
	}
	if byID["20"].Title != "orphan on disk" {
		t.Fatalf("orphan should be recovered from file: %+v", byID["20"])
	}
}

// TestDeepworkListSessions_NoCrossWorkspaceLeak (CHG-016 R3): the transcript dir is a
// single FLAT shared dir, so a file belonging to ANOTHER workspace (globally known, but
// not in THIS workspace's rows) must NOT be recovered as an orphan. Workspace 1 owns
// session 10; session 30's file is on disk but owned by another workspace. Only 10 shows.
func TestDeepworkListSessions_NoCrossWorkspaceLeak(t *testing.T) {
	dir := t.TempDir()
	writeDWFile(t, dir, "10", "ours")             // ws1's session
	writeDWFile(t, dir, "30", "another ws's run") // on shared disk, owned by another ws

	prov := &fakeProvider{
		rows:      []DeepworkSessionMeta{{ID: 10, Title: "DB Title For 10", TurnCount: 3}},
		globalIDs: []int64{10, 30}, // DB knows both globally; 30 belongs elsewhere
	}
	src := NewDeepworkSourceWithDir(prov, 1, dir)
	metas, err := src.ListSessions(context.Background(), "/p")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("want 1 (only ws1's session 10; 30 belongs to another workspace, must not leak), got %d: %+v", len(metas), metas)
	}
	if metas[0].ID != "10" {
		t.Fatalf("expected only session 10, got %+v", metas[0])
	}
}

type fakeProvider struct {
	rows  []DeepworkSessionMeta
	turns []DeepworkTurn
	// globalIDs: ids known to the DB across ALL workspaces/sources. nil → defaults to
	// this workspace's rows (so a file not in rows is a genuine orphan). Set explicitly
	// to model "file belongs to ANOTHER workspace" (globally known, not in our rows).
	globalIDs []int64
}

func (f *fakeProvider) ListWorkspaceSessions(_ context.Context, _ int64) ([]DeepworkSessionMeta, error) {
	return f.rows, nil
}
func (f *fakeProvider) KnownSessionIDs(_ context.Context) (map[string]struct{}, error) {
	ids := map[string]struct{}{}
	if f.globalIDs != nil {
		for _, id := range f.globalIDs {
			ids[strconv.FormatInt(id, 10)] = struct{}{}
		}
		return ids, nil
	}
	for _, r := range f.rows {
		ids[strconv.FormatInt(r.ID, 10)] = struct{}{}
	}
	return ids, nil
}
func (f *fakeProvider) LoadSessionTurns(_ context.Context, _ int64) ([]DeepworkTurn, error) {
	return f.turns, nil
}
