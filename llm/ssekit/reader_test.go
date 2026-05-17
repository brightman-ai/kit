package ssekit

import (
	"io"
	"strings"
	"testing"
)

func TestReader_BasicDataEvent(t *testing.T) {
	input := "data: hello world\n\n"
	r := NewReader(strings.NewReader(input))

	ev, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Data != "hello world" {
		t.Errorf("Data = %q, want %q", ev.Data, "hello world")
	}
	if ev.Type != "" {
		t.Errorf("Type = %q, want empty", ev.Type)
	}

	_, err = r.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestReader_NamedEvent(t *testing.T) {
	input := "event: message\ndata: {\"text\":\"hi\"}\n\n"
	r := NewReader(strings.NewReader(input))

	ev, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "message" {
		t.Errorf("Type = %q, want %q", ev.Type, "message")
	}
	if ev.Data != `{"text":"hi"}` {
		t.Errorf("Data = %q", ev.Data)
	}
}

func TestReader_MultiLineData(t *testing.T) {
	input := "data: line1\ndata: line2\ndata: line3\n\n"
	r := NewReader(strings.NewReader(input))

	ev, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Data != "line1\nline2\nline3" {
		t.Errorf("Data = %q, want multi-line", ev.Data)
	}
}

func TestReader_SkipsComments(t *testing.T) {
	input := ": this is a comment\ndata: actual data\n\n"
	r := NewReader(strings.NewReader(input))

	ev, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Data != "actual data" {
		t.Errorf("Data = %q, want %q", ev.Data, "actual data")
	}
}

func TestReader_MultipleEvents(t *testing.T) {
	input := "data: first\n\ndata: second\n\n"
	r := NewReader(strings.NewReader(input))

	ev1, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev1.Data != "first" {
		t.Errorf("ev1.Data = %q", ev1.Data)
	}

	ev2, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev2.Data != "second" {
		t.Errorf("ev2.Data = %q", ev2.Data)
	}
}

func TestReader_OpenAIDone(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	r := NewReader(strings.NewReader(input))

	ev1, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ev1.Data, "choices") {
		t.Errorf("ev1 should contain choices")
	}

	ev2, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev2.Data != "[DONE]" {
		t.Errorf("ev2.Data = %q, want [DONE]", ev2.Data)
	}
}

func TestReader_SkipsEmptyEvents(t *testing.T) {
	input := "\n\ndata: real\n\n"
	r := NewReader(strings.NewReader(input))

	ev, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Data != "real" {
		t.Errorf("Data = %q, want %q", ev.Data, "real")
	}
}
