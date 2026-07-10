package transcript

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestParseTaskNotification verifies the `<task-notification>` envelope parser
// against the real claude schema (status ∈ completed|failed|killed + summary).
func TestParseTaskNotification(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		wantNil    bool
		wantStatus string
		wantID     string
		wantSum    string
	}{
		{
			name: "completed",
			text: "<task-notification>\n<task-id>bw2eo1mkg</task-id>\n" +
				"<tool-use-id>toolu_x</tool-use-id>\n<output-file>/tmp/x</output-file>\n" +
				"<status>completed</status>\n" +
				"<summary>Agent \"OD paradigm\" completed</summary>\n</task-notification>",
			wantStatus: "completed", wantID: "bw2eo1mkg",
			wantSum: `Agent "OD paradigm" completed`,
		},
		{
			name: "failed",
			text: "<task-notification>\n<task-id>bo1pbyyq7</task-id>\n" +
				"<status>failed</status>\n" +
				"<summary>Background command failed with exit code 144</summary>\n</task-notification>",
			wantStatus: "failed", wantID: "bo1pbyyq7",
			wantSum: "Background command failed with exit code 144",
		},
		{name: "plain user text is not a notification", text: "请帮我修一下这个 bug", wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tn := parseTaskNotification(tc.text)
			if tc.wantNil {
				if tn != nil {
					t.Fatalf("expected nil for plain text, got %+v", tn)
				}
				return
			}
			if tn == nil {
				t.Fatal("expected a parsed notification, got nil")
			}
			if tn.Status != tc.wantStatus {
				t.Errorf("status: got %q want %q", tn.Status, tc.wantStatus)
			}
			if tn.TaskID != tc.wantID {
				t.Errorf("taskID: got %q want %q", tn.TaskID, tc.wantID)
			}
			if tn.Summary != tc.wantSum {
				t.Errorf("summary: got %q want %q", tn.Summary, tc.wantSum)
			}
		})
	}
}

// TestLoadTranscript_TaskNotificationAndAgentDuration parses a synthetic jsonl
// covering both P3b features end-to-end:
//   - a `<task-notification>` user line → a BlockTaskNotification (NOT a bubble)
//   - an Agent tool_use + later tool_result → DurationMs derived from the
//     timestamp delta (tokens stay 0 → honest "—").
func TestLoadTranscript_TaskNotificationAndAgentDuration(t *testing.T) {
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	projectDir := "/fake/proj"
	encDir := filepath.Join(projects, EncodeProjectDir(projectDir))
	if err := os.MkdirAll(encDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Agent dispatched at 12:00:00, result at 12:01:40 → 100_000 ms duration.
	jsonl := `{"type":"assistant","timestamp":"2026-06-10T12:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"dispatching"},{"type":"tool_use","id":"toolu_ag1","name":"Agent","input":{"subagent_type":"Explore","description":"探索输入架构","prompt":"go look"}}]}}
{"type":"user","timestamp":"2026-06-10T12:01:40.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_ag1","content":"## Summary\n done"}]}}
{"type":"user","timestamp":"2026-06-10T12:01:41.000Z","message":{"role":"user","content":"<task-notification>\n<task-id>bw2eo1mkg</task-id>\n<status>completed</status>\n<summary>Agent \"探索输入架构\" completed</summary>\n</task-notification>"}}
{"type":"user","timestamp":"2026-06-10T12:02:00.000Z","message":{"role":"user","content":"接下来请继续"}}
`
	id := "session-p3b"
	if err := os.WriteFile(filepath.Join(encDir, id+".jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	src := &ClaudeSource{Root: projects}
	tr, err := src.LoadTranscript(context.Background(), SessionRef{ProjectDir: projectDir, ID: id})
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}

	var agent *Block
	var notif *Block
	var realUser *Block
	for ti := range tr.Turns {
		for bi := range tr.Turns[ti].Blocks {
			b := &tr.Turns[ti].Blocks[bi]
			switch b.Type {
			case BlockAgent:
				agent = b
			case BlockTaskNotification:
				notif = b
			case BlockUser:
				realUser = b
			}
		}
	}

	// 1) task-notification → its own typed block, NOT a plain user bubble.
	if notif == nil {
		t.Fatal("expected a task-notification block")
	}
	if notif.NotifyStatus != "completed" {
		t.Errorf("notify status: got %q want completed", notif.NotifyStatus)
	}
	if notif.TaskID != "bw2eo1mkg" {
		t.Errorf("notify taskID: got %q", notif.TaskID)
	}
	if notif.Text != `Agent "探索输入架构" completed` {
		t.Errorf("notify summary: got %q", notif.Text)
	}

	// The plain user follow-up must still be a normal bubble (regression guard).
	if realUser == nil || realUser.Text != "接下来请继续" {
		t.Errorf("expected the plain user bubble to survive, got %+v", realUser)
	}

	// 2) Agent block carries a derived duration; tokens stay 0 (honest "—").
	if agent == nil {
		t.Fatal("expected an agent block")
	}
	if agent.SubagentType != "Explore" {
		t.Errorf("subagent type: got %q want Explore", agent.SubagentType)
	}
	if agent.DurationMs != 100_000 {
		t.Errorf("agent duration: got %d want 100000 (12:00:00→12:01:40)", agent.DurationMs)
	}
	if agent.InTokens != 0 || agent.OutTokens != 0 {
		t.Errorf("agent tokens must stay 0 (claude does not inline subagent usage): in=%d out=%d",
			agent.InTokens, agent.OutTokens)
	}
	t.Logf("agent: type=%q durationMs=%d (in/out tokens honestly 0→ \"—\")",
		agent.SubagentType, agent.DurationMs)
	t.Logf("task-notification: status=%q summary=%q", notif.NotifyStatus, notif.Text)
}
