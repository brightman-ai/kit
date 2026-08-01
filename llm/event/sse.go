package event

import (
	ssekit "github.com/brightman-ai/kit/llm/ssekit"
	"github.com/brightman-ai/kit/workstream"
)

func NewSSEEmitter(w *ssekit.Writer) Emitter { return workstream.NewSSEEmitter(w) }
