// Package llmstream contains provider stream decoders and transport lifecycle
// helpers for the unified LLM event model.
package stream

import (
	"time"

	event "github.com/brightman-ai/kit/llm/event"
)

// Decoder converts provider-specific stream frames into unified LLM events.
// DecodeLine may return multiple events because one provider frame can contain
// several semantic blocks, such as text followed by a tool call.
type Decoder interface {
	DecodeLine(line []byte) []event.Event
	Flush() []event.Event
}

// DecoderFunc adapts a stateless line parser to Decoder.
type DecoderFunc func(line []byte) []event.Event

func (f DecoderFunc) DecodeLine(line []byte) []event.Event {
	return f(line)
}

func (f DecoderFunc) Flush() []event.Event {
	return nil
}

// StartedEvent reports that the local agent process has been spawned. It is
// emitted by adapters before provider stdout produces its first model delta.
func StartedEvent(source string) event.Event {
	return event.StatusEvent("started").WithMeta("source", source)
}

// RunningEvent is a heartbeat/progress signal for streams whose upstream model
// is alive but temporarily quiet.
func RunningEvent(elapsed time.Duration) event.Event {
	return event.StatusEvent("running").WithMeta("elapsed_ms", elapsed.Milliseconds())
}
