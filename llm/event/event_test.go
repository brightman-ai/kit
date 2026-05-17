package event

import (
	"encoding/json"
	"testing"
)

func TestTextEvent_JSON(t *testing.T) {
	ev := TextEvent("hello")
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	// Should produce clean JSON without double encoding
	want := `{"kind":"text","content":"hello"}`
	if string(data) != want {
		t.Errorf("JSON:\n got: %s\nwant: %s", data, want)
	}
}

func TestStatusEvent_JSON(t *testing.T) {
	ev := StatusEvent("running")
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"kind":"status","status":"running"}`
	if string(data) != want {
		t.Errorf("JSON:\n got: %s\nwant: %s", data, want)
	}
}

func TestThinkingEvent_JSON(t *testing.T) {
	ev := ThinkingEvent("let me think...")
	data, _ := json.Marshal(ev)
	var parsed map[string]any
	json.Unmarshal(data, &parsed)

	if parsed["kind"] != "thinking" {
		t.Errorf("kind = %v", parsed["kind"])
	}
	if parsed["content"] != "let me think..." {
		t.Errorf("content = %v", parsed["content"])
	}
	// Ensure no spurious fields
	if _, ok := parsed["tool"]; ok {
		t.Error("tool field should be absent")
	}
	if _, ok := parsed["done"]; ok {
		t.Error("done field should be absent")
	}
}

func TestToolStartEvent_JSON(t *testing.T) {
	ev := ToolStartEvent("call_1", "get_weather", json.RawMessage(`{"city":"SF"}`))
	data, _ := json.Marshal(ev)

	var parsed map[string]any
	json.Unmarshal(data, &parsed)

	if parsed["kind"] != "tool_start" {
		t.Errorf("kind = %v", parsed["kind"])
	}
	tool, ok := parsed["tool"].(map[string]any)
	if !ok {
		t.Fatal("tool field missing or wrong type")
	}
	if tool["id"] != "call_1" {
		t.Errorf("tool.id = %v", tool["id"])
	}
	if tool["name"] != "get_weather" {
		t.Errorf("tool.name = %v", tool["name"])
	}
}

func TestToolStartEvent_NoDoubleEncoding(t *testing.T) {
	// This is the critical test — ToolData.Input is json.RawMessage.
	// When Event is marshaled, Input should be embedded as raw JSON, NOT double-encoded.
	ev := ToolStartEvent("c1", "fn", json.RawMessage(`{"key":"value"}`))
	data, _ := json.Marshal(ev)

	s := string(data)
	// Should contain {"key":"value"} as-is, not escaped
	if !contains(s, `"input":{"key":"value"}`) {
		t.Errorf("double encoding detected:\n %s", s)
	}
}

func TestDoneEvent_JSON(t *testing.T) {
	ev := DoneEvent("stop", 100, 50)
	data, _ := json.Marshal(ev)

	var parsed map[string]any
	json.Unmarshal(data, &parsed)

	done, ok := parsed["done"].(map[string]any)
	if !ok {
		t.Fatal("done field missing")
	}
	if done["reason"] != "stop" {
		t.Errorf("reason = %v", done["reason"])
	}
	if done["input_tokens"] != float64(100) {
		t.Errorf("input_tokens = %v", done["input_tokens"])
	}
}

func TestUsageEvent_JSON(t *testing.T) {
	ev := UsageEvent(UsageData{
		InputTokens:          100,
		ThinkingTokens:       20,
		OutputTokens:         50,
		CacheReadInputTokens: 300,
		TotalTokens:          470,
	})
	data, _ := json.Marshal(ev)

	var parsed map[string]any
	json.Unmarshal(data, &parsed)

	if parsed["kind"] != "usage" {
		t.Errorf("kind = %v", parsed["kind"])
	}
	usage, ok := parsed["usage"].(map[string]any)
	if !ok {
		t.Fatal("usage field missing")
	}
	if usage["input_tokens"] != float64(100) {
		t.Errorf("input_tokens = %v", usage["input_tokens"])
	}
	if usage["thinking_tokens"] != float64(20) {
		t.Errorf("thinking_tokens = %v", usage["thinking_tokens"])
	}
}

func TestErrorEvent_JSON(t *testing.T) {
	ev := ErrorEvent("something went wrong")
	data, _ := json.Marshal(ev)

	var parsed map[string]any
	json.Unmarshal(data, &parsed)

	if parsed["kind"] != "error" {
		t.Errorf("kind = %v", parsed["kind"])
	}
	if parsed["content"] != "something went wrong" {
		t.Errorf("content = %v", parsed["content"])
	}
}

func TestWithSource(t *testing.T) {
	ev := TextEvent("hi").WithSource("claude")
	if ev.Source != "claude" {
		t.Errorf("Source = %q", ev.Source)
	}
	// Original event not mutated (value copy)
	orig := TextEvent("hi")
	if orig.Source != "" {
		t.Error("original mutated")
	}
}

func TestWithMeta(t *testing.T) {
	ev := TextEvent("hi").WithMeta("seq", 42).WithMeta("cost_usd", 0.01)
	if ev.Meta["seq"] != 42 {
		t.Errorf("Meta[seq] = %v", ev.Meta["seq"])
	}
	if ev.Meta["cost_usd"] != 0.01 {
		t.Errorf("Meta[cost_usd] = %v", ev.Meta["cost_usd"])
	}
}

func TestBurst(t *testing.T) {
	events := Burst("complete response")
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Kind != Text || events[0].Content != "complete response" {
		t.Errorf("event[0] = %+v", events[0])
	}
	if events[1].Kind != Done {
		t.Errorf("event[1].Kind = %v", events[1].Kind)
	}
}

func TestBurst_Empty(t *testing.T) {
	events := Burst("")
	if len(events) != 1 {
		t.Fatalf("expected 1 event (just Done), got %d", len(events))
	}
	if events[0].Kind != Done {
		t.Errorf("Kind = %v", events[0].Kind)
	}
}

func TestCollect(t *testing.T) {
	var events []Event
	emit := Collect(&events)

	emit(TextEvent("a"))
	emit(TextEvent("b"))
	emit(DoneEvent("stop", 0, 0))

	if len(events) != 3 {
		t.Errorf("collected %d events, want 3", len(events))
	}
}

func TestSeqEmitter(t *testing.T) {
	var events []Event
	emit := SeqEmitter(Collect(&events))

	emit(TextEvent("a"))
	emit(TextEvent("b"))

	if events[0].Meta["seq"] != 1 {
		t.Errorf("first seq = %v", events[0].Meta["seq"])
	}
	if events[1].Meta["seq"] != 2 {
		t.Errorf("second seq = %v", events[1].Meta["seq"])
	}
}

func TestSafeEmitter_ConcurrentUse(t *testing.T) {
	var events []Event
	emit := SafeEmitter(Collect(&events))

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			emit(TextEvent("x"))
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	if len(events) != 10 {
		t.Errorf("expected 10 events, got %d", len(events))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsImpl(s, substr))
}

func containsImpl(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
