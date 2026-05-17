package toolcall

import (
	"sort"
	"strings"
)

// Assembler accumulates incremental tool call deltas by index and produces
// complete ToolCall objects. It handles:
//   - Split function.name across deltas (rare but spec-legal)
//   - Incremental function.arguments concatenation (the common case)
//   - Parallel tool calls with different indices
//   - Out-of-order delta delivery (by index)
//
// Usage:
//
//	a := toolcall.NewAssembler()
//	for chunk := range stream {
//	    for _, delta := range chunk.ToolCalls {
//	        a.Feed(delta)
//	    }
//	}
//	completed := a.Complete()
type Assembler struct {
	entries map[int]*entry
}

type entry struct {
	id       string
	typ      string
	name     strings.Builder
	args     strings.Builder
	hasName  bool
}

// NewAssembler creates a new tool call assembler.
func NewAssembler() *Assembler {
	return &Assembler{entries: make(map[int]*entry)}
}

// Feed processes a single tool call delta. Call this for each delta in each
// streaming chunk's tool_calls array.
func (a *Assembler) Feed(d Delta) {
	e, ok := a.entries[d.Index]
	if !ok {
		e = &entry{}
		a.entries[d.Index] = e
	}

	// ID: first non-empty wins (typically only in first delta for this index)
	if d.ID != "" && e.id == "" {
		e.id = d.ID
	}

	// Type: first non-empty wins
	if d.Type != "" && e.typ == "" {
		e.typ = d.Type
	}

	// Name: accumulate (usually arrives in one delta, but spec allows split)
	if d.Function.Name != "" {
		e.name.WriteString(d.Function.Name)
		e.hasName = true
	}

	// Arguments: always concatenate (this is the common incremental case)
	if d.Function.Arguments != "" {
		e.args.WriteString(d.Function.Arguments)
	}
}

// Complete returns all accumulated tool calls sorted by index.
// Call this after the stream ends (finish_reason = "tool_calls" or "stop").
// Only entries with a non-empty function name are returned.
func (a *Assembler) Complete() []ToolCall {
	if len(a.entries) == 0 {
		return nil
	}

	result := make([]ToolCall, 0, len(a.entries))
	for idx, e := range a.entries {
		if !e.hasName {
			continue // incomplete entry (no function name ever arrived)
		}
		typ := e.typ
		if typ == "" {
			typ = "function" // OpenAI default
		}
		result = append(result, ToolCall{
			Index: idx,
			ID:    e.id,
			Type:  typ,
			Function: ToolCallFunction{
				Name:      e.name.String(),
				Arguments: e.args.String(),
			},
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Index < result[j].Index
	})
	return result
}

// Pending returns the number of in-progress tool call entries.
func (a *Assembler) Pending() int {
	return len(a.entries)
}

// Reset clears all accumulated state. Use between LLM call rounds in a tool loop.
func (a *Assembler) Reset() {
	a.entries = make(map[int]*entry)
}
