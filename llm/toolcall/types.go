// Package toolcall reassembles OpenAI-compatible streaming tool-call deltas.
// The wire vocabulary itself belongs to kit/llm; this package owns only the
// stateful assembler.
package toolcall

import "github.com/brightman-ai/kit/llm"

// Aliases keep the historical import path source-compatible without defining a
// second ToolCall/Delta model.
type (
	Delta            = llm.ToolCallDelta
	ToolCall         = llm.ToolCall
	ToolCallFunction = llm.ToolCallFunction
)
