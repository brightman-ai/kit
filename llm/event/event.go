// Package event is the compatibility import path for the original LLM-only
// event API.
//
// Deprecated: import github.com/brightman-ai/kit/workstream instead.  The
// workstream package owns the event vocabulary and wire shape; this package is
// deliberately only aliases, so old consumers do not create a second protocol
// while they migrate.
package event

import "github.com/brightman-ai/kit/workstream"

// Types are aliases, not copies. An event emitted through this legacy path is
// exactly a workstream event and can cross either API without conversion.
type (
	Kind      = workstream.Kind
	Event     = workstream.Event
	ToolData  = workstream.ToolData
	UsageData = workstream.UsageData
	DoneData  = workstream.DoneData
	Emitter   = workstream.Emitter
)

// The legacy LLM subset remains source-compatible. The complete vocabulary is
// defined once in workstream; new kinds must never be added here.
const (
	Status     = workstream.Status
	Text       = workstream.Text
	Thinking   = workstream.Thinking
	ToolStart  = workstream.ToolStart
	ToolResult = workstream.ToolResult
	Usage      = workstream.Usage
	Done       = workstream.Done
	Error      = workstream.Error
	Raw        = workstream.Raw
)

var (
	StatusEvent      = workstream.StatusEvent
	TextEvent        = workstream.TextEvent
	ThinkingEvent    = workstream.ThinkingEvent
	ToolStartEvent   = workstream.ToolStartEvent
	ToolResultEvent  = workstream.ToolResultEvent
	UsageEvent       = workstream.UsageEvent
	DoneEvent        = workstream.DoneEvent
	ErrorEvent       = workstream.ErrorEvent
	RawEvent         = workstream.RawEvent
	Burst            = workstream.Burst
	Collect          = workstream.Collect
	Discard          = workstream.Discard
	SeqEmitter       = workstream.SeqEmitter
	TimestampEmitter = workstream.TimestampEmitter
)
