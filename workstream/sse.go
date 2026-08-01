package workstream

import ssekit "github.com/brightman-ai/kit/llm/ssekit"

func NewSSEEmitter(w *ssekit.Writer) Emitter {
	return func(ev Event) bool {
		return w.WriteJSON(ev) == nil
	}
}
