package stream

import (
	"encoding/json"
	"testing"

	event "github.com/brightman-ai/kit/llm/event"
)

func TestClaudeDecoderPartialToolCallEmitsOnceAtBlockStop(t *testing.T) {
	decoder := NewClaudeDecoder()

	lines := [][]byte{
		[]byte(`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1"}}}`),
		[]byte(`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_1","name":"Bash"}}}`),
		[]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls"}}}`),
		[]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" -la\"}"}}}`),
		[]byte(`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`),
		[]byte(`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"tool_use","id":"tool_1","name":"Bash","input":{"command":"ls -la"}}]}}`),
	}

	var events []event.Event
	for _, line := range lines {
		events = append(events, decoder.DecodeLine(line)...)
	}

	var tools []event.Event
	for _, ev := range events {
		if ev.Kind == event.ToolStart {
			tools = append(tools, ev)
		}
	}
	if len(tools) != 1 {
		t.Fatalf("tool_start count=%d, want 1; events=%v", len(tools), events)
	}
	if tools[0].Tool == nil || tools[0].Tool.ID != "tool_1" || tools[0].Tool.Name != "Bash" {
		t.Fatalf("unexpected tool event: %+v", tools[0].Tool)
	}
	var input map[string]string
	if err := json.Unmarshal(tools[0].Tool.Input, &input); err != nil {
		t.Fatalf("tool input json: %v", err)
	}
	if input["command"] != "ls -la" {
		t.Fatalf("command=%q, want ls -la", input["command"])
	}
}

func TestClaudeDecoderToolCallFinalAssistantBeforeBlockStopEmitsOnce(t *testing.T) {
	decoder := NewClaudeDecoder()

	lines := [][]byte{
		[]byte(`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1"}}}`),
		[]byte(`{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool_1","name":"Bash","input":{}}}}`),
		[]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"printf ok\"}"}}}`),
		[]byte(`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"tool_use","id":"tool_1","name":"Bash","input":{"command":"printf ok"}}]}}`),
		[]byte(`{"type":"stream_event","event":{"type":"content_block_stop","index":1}}`),
	}

	var count int
	for _, line := range lines {
		for _, ev := range decoder.DecodeLine(line) {
			if ev.Kind == event.ToolStart {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("tool_start count=%d, want 1", count)
	}
}

func TestClaudeDecoderPartialTextSkipsFinalAssistantDuplicate(t *testing.T) {
	decoder := NewClaudeDecoder()

	lines := [][]byte{
		[]byte(`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_2"}}}`),
		[]byte(`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`),
		[]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}}`),
		[]byte(`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`),
		[]byte(`{"type":"assistant","message":{"id":"msg_2","content":[{"type":"text","text":"hello"}]}}`),
	}

	var text string
	for _, line := range lines {
		for _, ev := range decoder.DecodeLine(line) {
			if ev.Kind == event.Text {
				text += ev.Content
			}
		}
	}
	if text != "hello" {
		t.Fatalf("text=%q, want hello", text)
	}
}

func TestClaudeDecoderToolResultArrayContent(t *testing.T) {
	decoder := NewClaudeDecoder()

	events := decoder.DecodeLine([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool_1","content":[{"type":"text","text":"ok"}]}]}}`))
	if len(events) != 1 {
		t.Fatalf("events len=%d, want 1", len(events))
	}
	if events[0].Kind != event.ToolResult {
		t.Fatalf("kind=%q, want %q", events[0].Kind, event.ToolResult)
	}
	if events[0].Tool == nil || events[0].Tool.Output != "ok" {
		t.Fatalf("unexpected tool result: %+v", events[0].Tool)
	}
}

func TestClaudeDecoderAssistantUsage(t *testing.T) {
	decoder := NewClaudeDecoder()

	events := decoder.DecodeLine([]byte(`{"type":"assistant","message":{"id":"msg_usage","usage":{"input_tokens":100,"output_tokens":25,"cache_read_input_tokens":40},"content":[]}}`))
	if len(events) != 1 {
		t.Fatalf("events len=%d, want 1; events=%+v", len(events), events)
	}
	if events[0].Kind != event.Usage {
		t.Fatalf("kind=%q, want %q", events[0].Kind, event.Usage)
	}
	if events[0].Usage == nil {
		t.Fatal("usage nil")
	}
	if events[0].Usage.InputTokens != 100 || events[0].Usage.OutputTokens != 25 || events[0].Usage.CacheReadInputTokens != 40 {
		t.Fatalf("usage=%+v", events[0].Usage)
	}
	if events[0].Usage.TotalTokens != 165 {
		t.Fatalf("total=%d, want 165", events[0].Usage.TotalTokens)
	}
}
