package ssekit

import (
	"bytes"
	"testing"
)

func TestWriter_WriteData(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriterFromIO(&buf)

	if err := w.WriteData([]byte(`{"kind":"text","content":"hello"}`)); err != nil {
		t.Fatal(err)
	}

	want := "data: {\"kind\":\"text\",\"content\":\"hello\"}\n\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteData:\n got: %q\nwant: %q", got, want)
	}
}

func TestWriter_WriteEvent(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriterFromIO(&buf)

	if err := w.WriteEvent("message", []byte(`{"text":"hi"}`)); err != nil {
		t.Fatal(err)
	}

	want := "event: message\ndata: {\"text\":\"hi\"}\n\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteEvent:\n got: %q\nwant: %q", got, want)
	}
}

func TestWriter_WriteJSON(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriterFromIO(&buf)

	type payload struct {
		Kind    string `json:"kind"`
		Content string `json:"content"`
	}
	if err := w.WriteJSON(payload{Kind: "text", Content: "ok"}); err != nil {
		t.Fatal(err)
	}

	want := "data: {\"kind\":\"text\",\"content\":\"ok\"}\n\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteJSON:\n got: %q\nwant: %q", got, want)
	}
}

func TestWriter_WriteComment(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriterFromIO(&buf)

	if err := w.WriteComment("keepalive"); err != nil {
		t.Fatal(err)
	}

	want := ": keepalive\n\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteComment:\n got: %q\nwant: %q", got, want)
	}
}
