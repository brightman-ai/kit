package workstream

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestTextEventJSON(t *testing.T) {
	ev := TextEvent("hello")
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["kind"] != "text" {
		t.Fatalf("kind = %v", parsed["kind"])
	}
	if parsed["content"] != "hello" {
		t.Fatalf("content = %v", parsed["content"])
	}
	if _, ok := parsed["tool"]; ok {
		t.Fatal("tool should be absent")
	}
}

func TestToolStartEventNoDoubleEncoding(t *testing.T) {
	ev := ToolStartEvent("call-1", "browser_page_read", json.RawMessage(`{"chunk_id":"c1"}`))
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"input":{"chunk_id":"c1"}`) {
		t.Fatalf("tool input was double encoded: %s", string(data))
	}
}

func TestSkillStartEventNoDoubleEncoding(t *testing.T) {
	ev := SkillStartEvent("skill-1", "browser_research", json.RawMessage(`{"goal":"summarize"}`))
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"skill":{"id":"skill-1","name":"browser_research","input":{"goal":"summarize"}`) {
		t.Fatalf("skill input was double encoded: %s", string(data))
	}
}

func TestNonLLMEvents(t *testing.T) {
	events := []Event{
		ContextStartEvent(ContextData{Kind: "browser_page", Title: "Example"}),
		ArtifactDeltaEvent(ArtifactData{Name: "index.html", Delta: "<main>"}),
		ProjectionDoneEvent(ProjectionData{Kind: "workspace", Target: "Notes"}),
		PermissionRequestEvent(PermissionData{Capabilities: []string{"host_command"}, Summary: "Open link"}),
	}
	kinds := []Kind{ContextStart, ArtifactDelta, ProjectionDone, PermissionRequest}
	for i, ev := range events {
		if ev.Kind != kinds[i] {
			t.Fatalf("event %d kind = %s, want %s", i, ev.Kind, kinds[i])
		}
		if ev.At == "" {
			t.Fatalf("event %d missing timestamp", i)
		}
	}
}

func TestSeqEmitter(t *testing.T) {
	var events []Event
	emit := SeqEmitter(Collect(&events))
	emit(TextEvent("a"))
	emit(DoneEvent("stop", 0, 0))
	if events[0].Meta["seq"] != 1 {
		t.Fatalf("first seq = %v", events[0].Meta["seq"])
	}
	if events[1].Meta["seq"] != 2 {
		t.Fatalf("second seq = %v", events[1].Meta["seq"])
	}
}

func TestHeartbeatEmitter(t *testing.T) {
	events := make(chan Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emit := HeartbeatEmitter(ctx, func(ev Event) bool {
		events <- ev
		return true
	}, 10*time.Millisecond)
	var first Event
	select {
	case first = <-events:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for heartbeat")
	}
	emit(TextEvent("done"))
	var second Event
	select {
	case second = <-events:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for text event")
	}
	cancel()
	if first.Kind != Status || first.Status != "running" {
		t.Fatalf("first event = %#v, want running status", first)
	}
	if second.Kind != Text {
		t.Fatalf("second event = %#v, want text", second)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
