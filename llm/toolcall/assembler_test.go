package toolcall

import (
	"testing"
)

func TestAssembler_SingleToolCall(t *testing.T) {
	a := NewAssembler()

	// First delta: ID + name + partial args
	a.Feed(Delta{
		Index: 0,
		ID:    "call_abc",
		Type:  "function",
		Function: struct {
			Name      string `json:"name,omitempty"`
			Arguments string `json:"arguments,omitempty"`
		}{Name: "get_weather", Arguments: `{"loc`},
	})

	// Second delta: more args
	a.Feed(Delta{
		Index: 0,
		Function: struct {
			Name      string `json:"name,omitempty"`
			Arguments string `json:"arguments,omitempty"`
		}{Arguments: `ation": "SF`},
	})

	// Third delta: final args
	a.Feed(Delta{
		Index: 0,
		Function: struct {
			Name      string `json:"name,omitempty"`
			Arguments string `json:"arguments,omitempty"`
		}{Arguments: `"}`},
	})

	calls := a.Complete()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}

	tc := calls[0]
	if tc.ID != "call_abc" {
		t.Errorf("ID = %q, want %q", tc.ID, "call_abc")
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("Name = %q, want %q", tc.Function.Name, "get_weather")
	}
	if tc.Function.Arguments != `{"location": "SF"}` {
		t.Errorf("Arguments = %q, want %q", tc.Function.Arguments, `{"location": "SF"}`)
	}
	if tc.Type != "function" {
		t.Errorf("Type = %q, want %q", tc.Type, "function")
	}
}

func TestAssembler_ParallelToolCalls(t *testing.T) {
	a := NewAssembler()

	// Two tool calls arriving interleaved
	a.Feed(Delta{Index: 0, ID: "call_1", Function: struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}{Name: "search", Arguments: `{"q"`}})

	a.Feed(Delta{Index: 1, ID: "call_2", Function: struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}{Name: "read", Arguments: `{"id"`}})

	a.Feed(Delta{Index: 0, Function: struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}{Arguments: `: "test"}`}})

	a.Feed(Delta{Index: 1, Function: struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}{Arguments: `: 42}`}})

	calls := a.Complete()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}

	// Should be sorted by index
	if calls[0].Index != 0 || calls[1].Index != 1 {
		t.Errorf("calls not sorted by index")
	}
	if calls[0].Function.Name != "search" {
		t.Errorf("call[0].Name = %q", calls[0].Function.Name)
	}
	if calls[0].Function.Arguments != `{"q": "test"}` {
		t.Errorf("call[0].Args = %q", calls[0].Function.Arguments)
	}
	if calls[1].Function.Name != "read" {
		t.Errorf("call[1].Name = %q", calls[1].Function.Name)
	}
	if calls[1].Function.Arguments != `{"id": 42}` {
		t.Errorf("call[1].Args = %q", calls[1].Function.Arguments)
	}
}

func TestAssembler_DefaultType(t *testing.T) {
	a := NewAssembler()
	a.Feed(Delta{Index: 0, ID: "c1", Function: struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}{Name: "fn", Arguments: "{}"}})

	calls := a.Complete()
	if len(calls) != 1 {
		t.Fatal("expected 1")
	}
	if calls[0].Type != "function" {
		t.Errorf("Type = %q, want 'function'", calls[0].Type)
	}
}

func TestAssembler_Reset(t *testing.T) {
	a := NewAssembler()
	a.Feed(Delta{Index: 0, ID: "c1", Function: struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}{Name: "fn", Arguments: "{}"}})

	if a.Pending() != 1 {
		t.Errorf("Pending = %d, want 1", a.Pending())
	}

	a.Reset()
	if a.Pending() != 0 {
		t.Errorf("after Reset, Pending = %d, want 0", a.Pending())
	}

	calls := a.Complete()
	if len(calls) != 0 {
		t.Errorf("after Reset, Complete returned %d calls", len(calls))
	}
}

func TestAssembler_SkipsIncompleteEntries(t *testing.T) {
	a := NewAssembler()
	// Feed delta with no name (incomplete)
	a.Feed(Delta{Index: 0, Function: struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}{Arguments: `{"x":1}`}})

	calls := a.Complete()
	if len(calls) != 0 {
		t.Errorf("expected 0 calls for nameless entry, got %d", len(calls))
	}
}

func TestAssembler_EmptyReturnsNil(t *testing.T) {
	a := NewAssembler()
	calls := a.Complete()
	if calls != nil {
		t.Errorf("expected nil, got %v", calls)
	}
}
