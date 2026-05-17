package event

import (
	ssekit "github.com/brightman-ai/kit/llm/ssekit"
)

// NewSSEEmitter creates an Emitter that writes each Event as an SSE data line.
// Returns false (backpressure) when the SSE write fails (client disconnected).
func NewSSEEmitter(w *ssekit.Writer) Emitter {
	return func(ev Event) bool {
		return w.WriteJSON(ev) == nil
	}
}
