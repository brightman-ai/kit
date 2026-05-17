// Package event defines the unified event model for LLM streaming in deepwork.
// All 9 Portals (Chat, Browser Sidebar, Open Design, Studio, Council, Claw,
// Companion, Workspace Run, CLI Escape) consume the same Event type regardless
// of source family (CLI Observer / LLM API Orchestrator / WebChat DOM Polling).
//
// Design: TH-0503-k8v (8 rounds, 12 Codex reviews, 15 DDC, 7 BRR, 2 EUREKA).
package event

import (
	"encoding/json"
	"time"
)

// Kind classifies the semantic content of an LLM streaming event.
type Kind string

const (
	// Status is a non-content progress signal for transport/agent lifecycle.
	Status Kind = "status"
	// Text is an incremental text content delta (打字机效果).
	Text Kind = "text"
	// Thinking is an incremental reasoning/CoT delta.
	// Maps to: OpenAI reasoning, Anthropic thinking_delta, DeepSeek/GLM reasoning_content.
	Thinking Kind = "thinking"
	// ToolStart signals a complete tool call (provider buffers incremental args internally).
	ToolStart Kind = "tool_start"
	// ToolResult signals a tool execution result.
	ToolResult Kind = "tool_result"
	// Usage reports token counters while a stream is still running or after a
	// provider emits cumulative accounting outside the final done frame.
	Usage Kind = "usage"
	// Done signals stream completion with usage/cost/reason.
	Done Kind = "done"
	// Error signals a recoverable error (stream may continue).
	Error Kind = "error"
	// Raw is an escape hatch for unknown/new event types from providers.
	Raw Kind = "raw"
)

// Event is the universal streaming event for all deepwork Portals.
// Exactly one of Content/Tool/DoneInfo/RawData is meaningful per Kind.
type Event struct {
	Kind     Kind            `json:"kind"`
	Status   string          `json:"status,omitempty"`  // Status: waiting / running / tool / etc.
	Content  string          `json:"content,omitempty"` // Text/Thinking/Error: direct string
	Tool     *ToolData       `json:"tool,omitempty"`    // ToolStart/ToolResult: structured
	Usage    *UsageData      `json:"usage,omitempty"`   // Usage: token accounting update
	DoneInfo *DoneData       `json:"done,omitempty"`    // Done: usage + reason
	Source   string          `json:"source,omitempty"`  // Multi-source attribution (Council)
	RawData  json.RawMessage `json:"raw,omitempty"`     // Raw: passthrough unknown events
	Meta     map[string]any  `json:"meta,omitempty"`    // Application extensions (seq, cost_usd, duration_ms)
}

// ToolData holds tool call or tool result information.
type ToolData struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input,omitempty"`       // ToolStart: complete arguments JSON
	Output     string          `json:"output,omitempty"`      // ToolResult: execution output
	IsError    bool            `json:"is_error,omitempty"`    // ToolResult: tool failed
	DurationMs int             `json:"duration_ms,omitempty"` // ToolResult: execution time
}

// DoneData holds stream completion information including usage.
type DoneData struct {
	Reason       string `json:"reason,omitempty"`        // stop / tool_use / length / cancel / error
	InputTokens  int    `json:"input_tokens,omitempty"`  // LLM: prompt tokens
	OutputTokens int    `json:"output_tokens,omitempty"` // LLM: completion tokens
}

// UsageData holds provider token accounting. Providers may emit this
// incrementally, cumulatively, or only at completion.
type UsageData struct {
	InputTokens              int  `json:"input_tokens,omitempty"`
	ThinkingTokens           int  `json:"thinking_tokens,omitempty"`
	OutputTokens             int  `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int  `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int  `json:"cache_creation_input_tokens,omitempty"`
	TotalTokens              int  `json:"total_tokens,omitempty"`
	Estimated                bool `json:"estimated,omitempty"`
}

// ─── Constructors ──────────────────────────────────────────
// These are the ONLY recommended way to create Events.
// They guarantee Kind-field alignment and prevent misuse.

// TextEvent creates a text delta event.
func TextEvent(content string) Event {
	return Event{Kind: Text, Content: content}
}

// StatusEvent creates a non-content progress event.
func StatusEvent(status string) Event {
	return Event{Kind: Status, Status: status}
}

// ThinkingEvent creates a thinking/reasoning delta event.
func ThinkingEvent(content string) Event {
	return Event{Kind: Thinking, Content: content}
}

// ToolStartEvent creates a tool call event with complete arguments.
func ToolStartEvent(id, name string, input json.RawMessage) Event {
	return Event{Kind: ToolStart, Tool: &ToolData{ID: id, Name: name, Input: input}}
}

// ToolResultEvent creates a tool execution result event.
func ToolResultEvent(id, name, output string, isErr bool, durMs int) Event {
	return Event{Kind: ToolResult, Tool: &ToolData{
		ID: id, Name: name, Output: output, IsError: isErr, DurationMs: durMs,
	}}
}

// UsageEvent creates a token accounting update.
func UsageEvent(data UsageData) Event {
	return Event{Kind: Usage, Usage: &data}
}

// DoneEvent creates a stream completion event.
func DoneEvent(reason string, inputTokens, outputTokens int) Event {
	return Event{Kind: Done, DoneInfo: &DoneData{
		Reason: reason, InputTokens: inputTokens, OutputTokens: outputTokens,
	}}
}

// ErrorEvent creates an error event.
func ErrorEvent(msg string) Event {
	return Event{Kind: Error, Content: msg}
}

// RawEvent creates a passthrough event for unknown provider data.
func RawEvent(data json.RawMessage) Event {
	return Event{Kind: Raw, RawData: data}
}

// WithSource returns a copy of the event with Source set.
// Used by Council to tag which LLM participant produced this event.
func (e Event) WithSource(source string) Event {
	e.Source = source
	return e
}

// WithMeta returns a copy of the event with Meta entries added.
// Existing Meta entries are preserved; new entries override on collision.
func (e Event) WithMeta(key string, value any) Event {
	if e.Meta == nil {
		e.Meta = make(map[string]any)
	}
	e.Meta[key] = value
	return e
}

// ─── Emitter ──────────────────────────────────────────
// Emitter is the universal callback for streaming events.
// Returns false to signal the producer should stop (backpressure / client disconnect).
type Emitter func(Event) bool

// ─── Helpers ──────────────────────────────────────────

// Burst converts a complete (non-streaming) response into a sequence of events.
// Useful for WebChat DOM polling, non-streaming API fallback, cached responses.
func Burst(content string) []Event {
	events := make([]Event, 0, 2)
	if content != "" {
		events = append(events, TextEvent(content))
	}
	events = append(events, DoneEvent("stop", 0, 0))
	return events
}

// Collect returns an Emitter that appends events to the given slice. For testing.
func Collect(events *[]Event) Emitter {
	return func(ev Event) bool {
		*events = append(*events, ev)
		return true
	}
}

// Discard returns an Emitter that silently drops all events. For benchmarks.
func Discard() Emitter {
	return func(Event) bool { return true }
}

// SeqEmitter wraps an Emitter to stamp monotonic sequence numbers into Meta["seq"].
func SeqEmitter(emit Emitter) Emitter {
	seq := 0
	return func(ev Event) bool {
		seq++
		ev = ev.WithMeta("seq", seq)
		return emit(ev)
	}
}

// TimestampEmitter wraps an Emitter to stamp current time into Meta["ts"].
func TimestampEmitter(emit Emitter) Emitter {
	return func(ev Event) bool {
		ev = ev.WithMeta("ts", time.Now().UnixMilli())
		return emit(ev)
	}
}
