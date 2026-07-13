package transcript

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeClaudeWindowFixture(t *testing.T, path string, from, to int, appendFile bool) {
	t.Helper()
	flag := os.O_CREATE | os.O_WRONLY
	if appendFile {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for i := from; i <= to; i++ {
		if err := enc.Encode(map[string]any{
			"type": "user", "timestamp": "2026-07-13T00:00:00Z",
			"message": map[string]any{"role": "user", "content": "intent " + itoa(i)},
		}); err != nil {
			t.Fatal(err)
		}
		if err := enc.Encode(map[string]any{
			"type": "assistant", "timestamp": "2026-07-13T00:00:01Z",
			"message": map[string]any{
				"id": "msg-" + itoa(i), "role": "assistant", "stop_reason": "end_turn",
				"content": []any{map[string]any{"type": "text", "text": "answer " + itoa(i)}},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeTranscriptWindowTailAndBackwardCursor(t *testing.T) {
	root := t.TempDir()
	project := "/workspace/project"
	dir := filepath.Join(root, EncodeProjectDir(project))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sess.jsonl")
	writeClaudeWindowFixture(t, path, 1, 30, false)
	src := &ClaudeSource{Root: root}

	tail, err := src.LoadTranscriptWindow(context.Background(), SessionRef{ProjectDir: project, ID: "sess"}, WindowRequest{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(tail.Transcript.Runs); got != 3 {
		t.Fatalf("tail runs=%d, want 3", got)
	}
	if tail.Transcript.Runs[0].UserIntent == nil || tail.Transcript.Runs[0].UserIntent.Text != "intent 28" {
		t.Fatalf("tail begins at wrong run: %+v", tail.Transcript.Runs[0])
	}
	st, _ := os.Stat(path)
	if !tail.HasMore || tail.Before <= 0 || tail.BytesParsed >= st.Size() {
		t.Fatalf("window was not bounded: %+v file=%d", tail, st.Size())
	}

	before := tail.Before
	older, err := src.LoadTranscriptWindow(context.Background(), SessionRef{ProjectDir: project, ID: "sess"}, WindowRequest{
		Before: &before, Limit: 3, Generation: tail.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(older.Transcript.Runs); got != 3 || older.Transcript.Runs[0].UserIntent.Text != "intent 25" {
		t.Fatalf("older window is not contiguous: %+v", older.Transcript.Runs)
	}
}

func TestClaudeTranscriptWindowGenerationSurvivesAppendAndResetsOnReplace(t *testing.T) {
	root := t.TempDir()
	project := "/workspace/project"
	dir := filepath.Join(root, EncodeProjectDir(project))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sess.jsonl")
	writeClaudeWindowFixture(t, path, 1, 5, false)
	src := &ClaudeSource{Root: root}
	first, err := src.LoadTranscriptWindow(context.Background(), SessionRef{ProjectDir: project, ID: "sess"}, WindowRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	writeClaudeWindowFixture(t, path, 6, 6, true)
	appended, err := src.LoadTranscriptWindow(context.Background(), SessionRef{ProjectDir: project, ID: "sess"}, WindowRequest{Limit: 2, Generation: first.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if appended.Reset || appended.Generation != first.Generation {
		t.Fatalf("append invalidated stable generation: first=%+v appended=%+v", first, appended)
	}

	oldCursor := first.Before
	writeClaudeWindowFixture(t, path, 90, 91, false)
	replaced, err := src.LoadTranscriptWindow(context.Background(), SessionRef{ProjectDir: project, ID: "sess"}, WindowRequest{
		Before: &oldCursor, Limit: 2, Generation: first.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replaced.Reset || replaced.Generation == first.Generation {
		t.Fatalf("replacement did not reset cursor: %+v", replaced)
	}
}

func TestCodexTranscriptWindowParsesOnlyTail(t *testing.T) {
	root := t.TempDir()
	project := "/workspace/project"
	dir := filepath.Join(root, "sessions", "2026", "07", "13")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-07-13-thread.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(map[string]any{
		"timestamp": "2026-07-13T00:00:00Z", "type": "session_meta",
		"payload": map[string]any{"type": "session_meta", "id": "thread", "cwd": project},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		rows := []map[string]any{
			{"timestamp": "2026-07-13T00:00:01Z", "type": "event_msg", "payload": map[string]any{"type": "task_started"}},
			{"timestamp": "2026-07-13T00:00:02Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "intent " + itoa(i)}}}},
			{"timestamp": "2026-07-13T00:00:03Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer " + itoa(i)}}}},
			{"timestamp": "2026-07-13T00:00:04Z", "type": "event_msg", "payload": map[string]any{"type": "task_complete"}},
		}
		for _, row := range rows {
			if err := enc.Encode(row); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	src := &CodexSource{Root: root}
	window, err := src.LoadTranscriptWindow(context.Background(), SessionRef{ProjectDir: project, ID: "thread"}, WindowRequest{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(window.Transcript.Runs); got != 4 {
		t.Fatalf("codex tail runs=%d, want 4", got)
	}
	if window.Transcript.Runs[0].UserIntent == nil || window.Transcript.Runs[0].UserIntent.Text != "intent 17" {
		t.Fatalf("codex tail starts at wrong run: %+v", window.Transcript.Runs[0])
	}
	st, _ := os.Stat(path)
	if !window.HasMore || window.BytesParsed >= st.Size() {
		t.Fatalf("codex window was not bounded: %+v file=%d", window, st.Size())
	}
}
